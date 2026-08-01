package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"codeterminal/protocol"
)

// Handler is a built-in (Lane A) tool's implementation: a Go function in the
// daemon. Registered with closures over whatever daemon state it needs, so this
// package stays free of daemon dependencies.
type Handler func(ctx context.Context, args json.RawMessage) (Result, error)

// Builtin pairs a Lane A tool's description with its implementation.
type Builtin struct {
	Tool    Tool
	Handler Handler
}

// Policy is the resolved decision for one tool: deny, ask, or allow. The
// registry resolves it; only the loop acts on it.
type Policy string

const (
	PolicyDeny  Policy = "deny"
	PolicyAsk   Policy = "ask"
	PolicyAllow Policy = "allow"
)

// PolicyResolver answers "what may this tool do?" for one server/tool pair. The
// daemon implements it over its config; the registry never reads config itself.
//
// The contract that matters: an unknown server or an unlisted tool must resolve
// to something restrictive, never to PolicyAllow. The registry enforces the
// server half itself (see Call) rather than trusting every implementation to
// remember.
type PolicyResolver interface {
	PolicyFor(server, tool string) Policy
}

// Registry is the loop's single tool surface across both lanes.
//
// It owns: which tools exist, what policy applies to each, which are advertised
// to the model, and dispatch. It does NOT own consent (the loop asks) or
// scrubbing (daemon/toolresult.go) -- a registry that also decided those would
// be the only thing anyone could review to know what the daemon may run.
type Registry struct {
	mu       sync.RWMutex
	builtins map[string]Builtin // by tool name
	servers  map[string]Client  // by server name
	policy   PolicyResolver

	// maxAdvertised bounds the tool list handed to the model. Not a resource
	// limit -- a QUALITY one. The Phase 0 eval measured tool-selection accuracy
	// falling from 100% with one tool on the menu to 85.7% with five
	// (docs/TOOLCALL_RELIABILITY_2026-07-31.md), so a wide menu is not a
	// neutral convenience: it makes the model choose worse.
	maxAdvertised int

	// dropped records tools omitted from the advertised set, for reporting.
	// Silent truncation would look identical to a server that never offered
	// the tool.
	dropped []string
}

func NewRegistry(policy PolicyResolver, maxAdvertised int) *Registry {
	return &Registry{
		builtins:      map[string]Builtin{},
		servers:       map[string]Client{},
		policy:        policy,
		maxAdvertised: maxAdvertised,
	}
}

// RegisterBuiltin adds one Lane A tool. Its Server is forced to
// BuiltinServerName and its lane/confinement to the first-party values -- a
// caller cannot register a "builtin" that reports itself unconfined, or an
// unconfined tool that reports itself builtin.
func (r *Registry) RegisterBuiltin(b Builtin) error {
	if b.Handler == nil {
		return fmt.Errorf("builtin %q has no handler", b.Tool.Name)
	}
	if b.Tool.Name == "" {
		return fmt.Errorf("builtin has no name")
	}

	b.Tool.Server = BuiltinServerName
	b.Tool.Lane = protocol.LaneFirstParty
	b.Tool.Confined = true

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.builtins[b.Tool.Name]; exists {
		return fmt.Errorf("builtin %q is registered twice", b.Tool.Name)
	}
	r.builtins[b.Tool.Name] = b
	return nil
}

// AddServer registers a connected Lane B client under its configured name.
func (r *Registry) AddServer(name string, c Client) error {
	if err := ValidateServerName(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.servers[name]; exists {
		return fmt.Errorf("server %q is registered twice", name)
	}
	r.servers[name] = c
	return nil
}

// Advertised returns the tools to offer the model this turn, in deterministic
// order, bounded by maxAdvertised.
//
// TWO FILTERS, BOTH DELIBERATE:
//
//  1. A tool whose policy is deny is never advertised. A tool the model cannot
//     call should not be on the menu competing for its attention -- and the
//     Phase 0 finding that the model reaches for a plausible-looking tool when
//     uncertain makes an uncallable entry actively harmful, not merely useless.
//  2. Built-ins are kept ahead of external tools when the cap bites. They are
//     the confined, non-mutating ones, so if something has to be dropped it
//     should not be the safe half.
//
// A server that cannot be listed does not fail the turn: its tools are missing
// and its error is returned alongside, for the caller to surface as a
// protocol.DegradedMCPServer.
func (r *Registry) Advertised(ctx context.Context) ([]Tool, []error) {
	r.mu.RLock()
	builtins := make([]Builtin, 0, len(r.builtins))
	for _, b := range r.builtins {
		builtins = append(builtins, b)
	}
	servers := make(map[string]Client, len(r.servers))
	for name, c := range r.servers {
		servers[name] = c
	}
	policy := r.policy
	maxAdvertised := r.maxAdvertised
	r.mu.RUnlock()

	var builtinTools []Tool
	for _, b := range builtins {
		if policyOf(policy, BuiltinServerName, b.Tool.Name) == PolicyDeny {
			continue
		}
		builtinTools = append(builtinTools, b.Tool)
	}
	SortTools(builtinTools)

	var externalTools []Tool
	var errs []error
	for _, name := range sortedKeys(servers) {
		tools, err := servers[name].ListTools(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, tool := range tools {
			if policyOf(policy, name, tool.Name) == PolicyDeny {
				continue
			}
			externalTools = append(externalTools, tool)
		}
	}
	SortTools(externalTools)

	all := append(builtinTools, externalTools...)

	r.mu.Lock()
	r.dropped = nil
	if maxAdvertised > 0 && len(all) > maxAdvertised {
		for _, tool := range all[maxAdvertised:] {
			r.dropped = append(r.dropped, tool.QualifiedName())
		}
		all = all[:maxAdvertised]
	}
	r.mu.Unlock()

	return all, errs
}

// Dropped names the tools the advertised cap left out on the last Advertised
// call, so the caller can say so rather than let them vanish.
func (r *Registry) Dropped() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.dropped...)
}

// Lookup finds one tool by qualified name, with its resolved policy. Used by the
// loop to build the approval prompt before anything runs.
func (r *Registry) Lookup(ctx context.Context, qualified string) (Tool, Policy, error) {
	server, name, err := SplitQualifiedName(qualified)
	if err != nil {
		return Tool{}, PolicyDeny, err
	}

	r.mu.RLock()
	builtin, isBuiltin := r.builtins[name]
	client, isServer := r.servers[server]
	policy := r.policy
	r.mu.RUnlock()

	if server == BuiltinServerName {
		if !isBuiltin {
			return Tool{}, PolicyDeny, fmt.Errorf("no built-in tool named %q", name)
		}
		return builtin.Tool, policyOf(policy, server, name), nil
	}

	if !isServer {
		// An unknown server resolves to deny, not to an error the caller might
		// treat as recoverable. The model naming a server that is not
		// configured is the shape a prompt-injection attempt would take.
		return Tool{}, PolicyDeny, fmt.Errorf("no configured MCP server named %q", server)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		return Tool{}, PolicyDeny, err
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool, policyOf(policy, server, name), nil
		}
	}
	return Tool{}, PolicyDeny, fmt.Errorf("server %q offers no tool named %q", server, name)
}

// Call dispatches an APPROVED tool call. It never consults consent -- the
// caller has already obtained it -- but it does re-check policy, because
// PolicyDeny must be unreachable no matter what path arrived here.
func (r *Registry) Call(ctx context.Context, qualified string, args json.RawMessage) (Result, error) {
	server, name, err := SplitQualifiedName(qualified)
	if err != nil {
		return Result{}, err
	}

	r.mu.RLock()
	builtin, isBuiltin := r.builtins[name]
	client, isServer := r.servers[server]
	policy := r.policy
	r.mu.RUnlock()

	// Belt and braces. The loop is supposed to have refused a denied tool long
	// before this, but "the caller checks" is how a check gets skipped by the
	// second caller.
	if policyOf(policy, server, name) == PolicyDeny {
		return Result{}, fmt.Errorf("tool %q is denied by configuration", qualified)
	}

	if server == BuiltinServerName {
		if !isBuiltin {
			return Result{}, fmt.Errorf("no built-in tool named %q", name)
		}
		return builtin.Handler(ctx, args)
	}
	if !isServer {
		return Result{}, fmt.Errorf("%w: no configured MCP server named %q", ErrServerUnavailable, server)
	}
	return client.CallTool(ctx, name, args)
}

// Close shuts down every Lane B server. Built-ins need no teardown: they are
// function calls, not processes.
func (r *Registry) Close() error {
	r.mu.Lock()
	servers := r.servers
	r.servers = map[string]Client{}
	r.mu.Unlock()

	var firstErr error
	for _, name := range sortedKeys(servers) {
		if err := servers[name].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// policyOf resolves a policy, defaulting to deny when there is no resolver at
// all. A registry built without one is a programming error, and the safe
// reading of "nobody said" is not "yes".
func policyOf(resolver PolicyResolver, server, tool string) Policy {
	if resolver == nil {
		return PolicyDeny
	}
	switch p := resolver.PolicyFor(server, tool); p {
	case PolicyDeny, PolicyAsk, PolicyAllow:
		return p
	default:
		// An unrecognised policy is deny. The config loader already refuses
		// these, so reaching here means a resolver invented one, and inventing
		// a permission is not a thing that should work.
		return PolicyDeny
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
