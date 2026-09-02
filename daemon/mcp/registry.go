package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

	// CONFINED IS ASSERTED FOR EVERY BUILT-IN EXCEPT ONE THAT EXECUTES CODE.
	//
	// The blanket `b.Tool.Confined = true` that used to sit here was correct for
	// the read-and-propose tools -- they are this daemon's own code behind the
	// same gates as every other path through it -- and FALSE for sandbox_exec,
	// which runs go/npm/make/cargo. A Makefile recipe is shell; `go run`
	// compiles and runs anything in the tree. Whether that is contained depends
	// on whether the HOST supplies bwrap or docker, which is a runtime fact a
	// constant cannot express.
	//
	// The flag travels on ToolApprovalRequest and is what the user is told, so
	// asserting it here obtained consent under false pretences: the TUI printed
	// "anything it changes goes through the same review you use for edits" for a
	// command that runs immediately, possibly on the host with full privileges.
	// protocol.ToolApprovalRequest.Confined states the rule this broke -- clients
	// must render the honest answer "plainly rather than softening it".
	//
	// A tool that executes code carries whatever it resolved (see
	// mcp.Confines); everything else is still asserted, so a new built-in cannot
	// forget to be honest by omission.
	//
	// ReachesNetwork is the SECOND exemption, and it was added because the
	// first one was written as though "escapes" could only ever mean "escapes
	// into the host". web_search and web_fetch run no subprocess and write no
	// file, so every structural test above passes them -- and stamping them
	// Confined would print "anything it changes goes through the same review
	// you use for edits" over a call that ships the user's query to a third
	// party and pulls an attacker-controlled page into the model's context.
	// See Tool.ReachesNetwork for why that sentence is worse than false.
	if !b.Tool.ExecutesCode && !b.Tool.ReachesNetwork && !b.Tool.LaunchesSubprocess {
		b.Tool.Confined = true
	}

	// THE TWO EXEMPTIONS ARE NOT SYMMETRIC, and a test caught the asymmetry
	// being missed. ExecutesCode means "confinement is a fact about the HOST",
	// so the tool's own resolved value is taken and may legitimately be true --
	// sandbox_exec under bwrap really is confined. ReachesNetwork admits no
	// such reading: a tool that talks to the internet is not confined on any
	// host, under any configuration, ever.
	//
	// Merely declining to ASSERT confinement therefore left a hole, because a
	// caller could still assert it themselves. Registering
	// Tool{ReachesNetwork: true, Confined: true} sailed through and printed the
	// reassuring sentence -- the precise outcome this whole exemption exists to
	// prevent, reached from the other direction. So it is FORCED false rather
	// than left alone: for this class the honest value is a constant, and a
	// constant should not be a field anyone can fill in wrong.
	if b.Tool.ReachesNetwork {
		b.Tool.Confined = false
	}

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

// Canonicalize resolves a tool name the MODEL supplied to the exact qualified
// name this registry dispatches on.
//
// THE PROBLEM. Measured against a live model over this repository: 8 refusals
// across 3 orchestrated turns, and roughly 5 of one turn's 14 model calls, were
// spent on the model asking for "search_code" when the registry knows it as
// "builtin__search_code". Every one was refused, re-prompted, and retried. The
// name was never ambiguous and never unsafe -- it was unqualified, and the tax
// was paid in iterations the user's budget bought for real work.
//
// THE RULE, and why it is drawn exactly here:
//
//	A bare name resolves to the FIRST-PARTY BUILTIN LANE, or it does not
//	resolve at all. It NEVER reaches a third-party server.
//
// The obvious alternative -- resolve a bare name to whichever configured server
// uniquely offers it -- is the one to reject, and it is worth being explicit
// about why, because it looks more useful. It makes the meaning of a bare name
// depend on the SET OF INSTALLED SERVERS: adding one could silently change what
// an existing bare name resolves to, or make a name that worked yesterday
// ambiguous today. That is PATH shadowing, and a security surface that mutates
// with configuration is not one anybody can review.
//
// Lane A is safe to alias into precisely because its namespace cannot be
// influenced by anyone: the builtins are registered at startup from compiled-in
// code, and ValidateServerName already REFUSES an external server called
// "builtin" so it cannot shadow a confined tool with an unconfined one. This
// function is a second consumer of that existing invariant rather than a new
// assumption.
//
// So the dangerous direction -- the model means a confined built-in and reaches
// an unconfined third-party tool -- is impossible by construction. The only
// misfire left is the reverse: a model that meant some server's "read_file" and
// gets the built-in one. That lands on the MORE restricted tool, which is
// workspace-confined and policy-gated, and the result tells the model what it
// actually got.
//
// WHAT THIS IS NOT. It is not a policy decision and it must never become one.
// The canonical name is what the caller then resolves policy, role scoping and
// consent against, and what the human is shown on the approval prompt. Aliasing
// a name and THEN checking permission on the raw string is how a convenience
// becomes a bypass; see resolveExecutable, where exactly one variable carries
// the resolved name into all four.
//
// No case folding, no fuzzy matching, no trimming: an exact match against a
// registered builtin, or nothing. This codebase has been bitten by case-
// insensitive path matching before, and a tool name is dispatched on.
//
// aliased reports that a bare name WAS rewritten, so the caller can log and
// count it -- the tax stays measurable after it stops being paid.
func (r *Registry) Canonicalize(name string) (canonical string, aliased bool) {
	// Already qualified (or malformed in a way only exact matching should
	// judge): hand it back untouched. A name containing the separator is the
	// model addressing a specific server, and second-guessing that is how it
	// would reach a server it did not name.
	if strings.Contains(name, "__") {
		return name, false
	}

	r.mu.RLock()
	_, isBuiltin := r.builtins[name]
	r.mu.RUnlock()

	if !isBuiltin {
		// Not a built-in. Left exactly as it arrived so it is refused by name
		// downstream rather than quietly becoming something else.
		return name, false
	}
	return BuiltinServerName + QualifiedNameSeparator + name, true
}

// Lookup finds one tool by qualified name, with its resolved policy. Used by the
// loop to build the approval prompt before anything runs.
//
// It matches EXACTLY. Callers that want to accept a bare name run it through
// Canonicalize first, so that the rewrite is a visible step in the caller
// rather than a leniency buried in dispatch.
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
