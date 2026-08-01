package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"codeterminal/protocol"
)

// Configuration for agent mode: the built-in tools, the external MCP servers,
// and the budget that bounds one turn.
//
// THE TWO LANES ARE STRUCTURAL, NOT A FIELD.
//
// `mcp.builtin` is Lane A: tools implemented as Go functions inside this daemon.
// They are confined because there is no subprocess to escape -- the five gates
// are on the actual call path -- and because none of them mutates the
// filesystem at all (the write tool proposes an edit into the existing review
// flow rather than writing; see mcp/builtin).
//
// `mcp.servers` is Lane B: external MCP servers, each an ordinary subprocess
// running with the user's full privileges. Nothing in this product can confine
// one. Every entry here is third-party BY DEFINITION, which is why there is no
// "lane" field to set: an earlier draft had one, and a field that must say
// "third_party" for safety is a field that can be typo'd into claiming
// something else. Putting the server in this map IS the declaration.
//
// THE POLARITY RULE.
//
// Every default resolves to the most restrictive behaviour, so an absent or
// partial "mcp" section -- including one in a models.json written before this
// existed -- means "no tools run":
//
//   - no "mcp" section at all      -> agent mode off
//   - "mcp" present, enabled unset -> agent mode off
//   - a server not listed          -> its tools cannot be called
//   - a tool not listed            -> policy "ask", never "allow"
//   - a Lane B server without an acknowledgement -> refused, with an explanation
//
// This mirrors ZDRConfig's weaken-bool discipline (see config.go): for anything
// touching the safety story, "off" must be reachable only by an explicit,
// auditable line the user wrote. It is applied harder here because a weakened
// ZDR setting sends data somewhere it should not go, whereas a weakened setting
// here RUNS A PROGRAM on the user's machine.
type MCPConfig struct {
	// Enabled turns agent mode on. False (the zero value) means the daemon
	// behaves exactly as it did before MCP existed: nothing is spawned, no
	// tools are advertised, and the turn is single-shot.
	Enabled bool `json:"enabled,omitempty"`

	// Builtin is Lane A policy. The tools themselves are compiled in; this only
	// says what may run without asking.
	Builtin MCPBuiltinConfig `json:"builtin,omitempty"`

	// Servers is Lane B, keyed by a short local name. The name is the user's
	// word for the server -- it appears in approval prompts and audit records,
	// never the server's own claim about itself.
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`

	// Budget bounds one agent turn.
	Budget MCPBudgetConfig `json:"budget,omitempty"`
}

// MCPBuiltinConfig is Lane A. There is no command, no env and no
// acknowledgement here: these tools are this daemon's own code, subject to the
// same confinement as every other path through it.
type MCPBuiltinConfig struct {
	// Disabled turns off the built-in tools while leaving agent mode on, for a
	// user who wants only their own MCP servers.
	Disabled bool `json:"disabled,omitempty"`

	// Tools maps a built-in tool name to "deny", "ask" or "allow". Unlisted is
	// "ask".
	Tools map[string]string `json:"tools,omitempty"`
}

// MCPServerConfig describes one Lane B server: how to launch it and what its
// tools may do.
type MCPServerConfig struct {
	// Command is the executable to run, Args its arguments. Never a shell
	// string: the daemon execs the binary directly with Args as a []string, so
	// there is no shell to quote for and no metacharacter to escape.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// Env names environment variables to pass through from the daemon's own
	// environment, for a server that legitimately needs a credential of its
	// own. It is an ALLOW-LIST OF NAMES, not a map of values: config files get
	// committed, and a config format that invites you to paste a token into it
	// is a config format that leaks tokens.
	//
	// Nothing outside this list reaches the child beyond PATH and HOME (see
	// mcp.ServerEnv). The model-API keys are never inheritable, whatever this
	// list says -- see mcp.ForbiddenEnvNames.
	Env []string `json:"env,omitempty"`

	// Tools maps a tool name to "deny", "ask" or "allow". Unlisted is "ask":
	// the safe default is to involve the human, not to guess that a tool the
	// daemon was never told about is either harmless or forbidden.
	Tools map[string]string `json:"tools,omitempty"`

	// AcknowledgedUnconfined must be true for this server to start.
	//
	// Not a checkbox for its own sake. A Lane B server is a subprocess with the
	// user's full privileges: the five-gate pipeline constrains the daemon's
	// OWN writer, and can no more confine somebody else's process than a lock
	// on your door confines your guest. Requiring this means nobody arrives at
	// that arrangement by copying a config snippet; they arrive at it by typing
	// the word.
	AcknowledgedUnconfined bool `json:"acknowledged_unconfined,omitempty"`

	// Disabled turns one configured server off without deleting its block.
	Disabled bool `json:"disabled,omitempty"`
}

// MCPBudgetConfig bounds a single agent turn. Every field is "0 means use the
// default" (the migration-free convention RetrievalConfig established), and
// every default is small enough that a runaway loop is caught by the first
// ceiling it reaches rather than by the user's bill.
type MCPBudgetConfig struct {
	MaxIterations      int `json:"max_iterations,omitempty"`
	TurnTimeoutSeconds int `json:"turn_timeout_seconds,omitempty"`
	MaxToolResultBytes int `json:"max_tool_result_bytes,omitempty"`
	MaxTotalToolBytes  int `json:"max_total_tool_bytes,omitempty"`
	MaxAdvertisedTools int `json:"max_advertised_tools,omitempty"`

	// MaxMessageBytes bounds ONE JSON-RPC message read from a Lane B server.
	//
	// The odd one out: every other field here bounds what the daemon SENDS or
	// SPENDS, and this one bounds what it is willing to ALLOCATE on behalf of
	// somebody else's process. It exists because the MCP SDK's stdio transport
	// is a bare json.Decoder with no limit, so without it a server can make the
	// daemon hold as much memory as it feels like -- and tools/list is read
	// before any approval prompt exists, so no consent step stands in front of
	// it. See mcp.DefaultMaxMessageBytes for the measurement.
	MaxMessageBytes int `json:"max_message_bytes,omitempty"`
}

// Tool policies. The vocabulary is closed: anything else in a config file is a
// hard error, not a warning, because a policy the daemon cannot read is a
// policy the user believes is in force.
const (
	PolicyDeny  = "deny"
	PolicyAsk   = "ask"
	PolicyAllow = "allow"
)

// Budget defaults and ceilings.
//
// The ceilings are not paranoia about the user; they are what stops a config
// typo (max_iterations: 800) from turning one prompt into a bill. Like
// retrieval's maxTopK, an over-large value is clamped WITH a warning rather
// than honoured silently or rejected outright.
//
// defaultMaxAdvertisedTools is the one number here that came from a
// measurement rather than a judgement. The Phase 0 tool-calling eval
// (docs/TOOLCALL_RELIABILITY_2026-07-31.md) found selection accuracy falling
// from 100% with one tool on the menu to 85.7% with five: a wide menu makes the
// model pick worse, so the advertised set is something to BOUND rather than
// merely assemble.
const (
	defaultMaxIterations      = 8
	maxMaxIterations          = 50
	defaultTurnTimeoutSeconds = 600
	maxTurnTimeoutSeconds     = 3600
	defaultMaxToolResultBytes = 32 * 1024
	maxMaxToolResultBytes     = 1024 * 1024
	defaultMaxTotalToolBytes  = 128 * 1024
	maxMaxTotalToolBytes      = 4 * 1024 * 1024
	defaultMaxAdvertisedTools = 12
	maxMaxAdvertisedTools     = 64

	// The ceiling, not the default: mcp.DefaultMaxMessageBytes owns that, next
	// to the measurement that justifies it. 32 MiB is high enough that no
	// honest server hits it and low enough that reaching it is roughly 400 MB
	// of heap rather than an unbounded amount.
	maxMaxMessageBytes = 32 * 1024 * 1024
)

var (
	knownMCPKeys        = []string{"enabled", "builtin", "servers", "budget"}
	knownMCPBuiltinKeys = []string{"disabled", "tools"}
	knownMCPServerKeys  = []string{"command", "args", "env", "tools", "acknowledged_unconfined", "disabled"}
	knownMCPBudgetKeys  = []string{"max_iterations", "turn_timeout_seconds", "max_tool_result_bytes",
		"max_total_tool_bytes", "max_advertised_tools", "max_message_bytes"}
)

// The resolved* accessors apply the "0 means default" convention. All are
// value receivers and safe on a zero Config a test may build directly.
func (b MCPBudgetConfig) resolvedMaxIterations() int {
	if b.MaxIterations <= 0 {
		return defaultMaxIterations
	}
	return b.MaxIterations
}

func (b MCPBudgetConfig) resolvedTurnTimeout() time.Duration {
	s := b.TurnTimeoutSeconds
	if s <= 0 {
		s = defaultTurnTimeoutSeconds
	}
	return time.Duration(s) * time.Second
}

func (b MCPBudgetConfig) resolvedMaxToolResultBytes() int {
	if b.MaxToolResultBytes <= 0 {
		return defaultMaxToolResultBytes
	}
	return b.MaxToolResultBytes
}

func (b MCPBudgetConfig) resolvedMaxTotalToolBytes() int {
	if b.MaxTotalToolBytes <= 0 {
		return defaultMaxTotalToolBytes
	}
	return b.MaxTotalToolBytes
}

func (b MCPBudgetConfig) resolvedMaxAdvertisedTools() int {
	if b.MaxAdvertisedTools <= 0 {
		return defaultMaxAdvertisedTools
	}
	return b.MaxAdvertisedTools
}

// resolvedMaxMessageBytes returns 0 for "unset", which mcp.Connect reads as
// mcp.DefaultMaxMessageBytes. Deliberately NOT resolved to the default here:
// the number belongs next to the measurement that justifies it, in the package
// that owns the transport, not in the config loader.
func (b MCPBudgetConfig) resolvedMaxMessageBytes() int { return b.MaxMessageBytes }

// policyForTool returns the configured policy for one tool, defaulting to
// PolicyAsk. Shared by both lanes so the "unlisted means ask" rule has exactly
// one implementation -- two copies is how the lanes drift apart.
func policyForTool(tools map[string]string, name string) string {
	if p, ok := tools[name]; ok {
		return p
	}
	return PolicyAsk
}

func (b MCPBuiltinConfig) policyFor(tool string) string { return policyForTool(b.Tools, tool) }
func (s MCPServerConfig) policyFor(tool string) string  { return policyForTool(s.Tools, tool) }

// Lane and Confined are constants per lane rather than fields, which is the
// whole point of the config reshape: they cannot be set, so they cannot be set
// wrong. These values reach the user on ToolApprovalRequest, so they must never
// be optimistic.
func (MCPBuiltinConfig) Lane() string   { return protocol.LaneFirstParty }
func (MCPBuiltinConfig) Confined() bool { return true }
func (MCPServerConfig) Lane() string    { return protocol.LaneThirdParty }
func (MCPServerConfig) Confined() bool  { return false }

// validateMCP is called from Config.Validate. It returns a hard error for
// anything the daemon cannot honour unambiguously, because the failure mode of
// a misread policy is running a program the user meant to forbid.
//
// The error/warning split follows the file's existing rule: a config that
// cannot be honoured AT ALL is an error, one that is usable but imperfectly
// understood is a warning. A policy value of "allowe" is the first kind --
// there is no safe reading of it.
func (c *Config) validateMCP() error {
	m := c.MCP

	if err := validateToolPolicies("mcp.builtin", m.Builtin.Tools); err != nil {
		return err
	}

	if !m.Enabled {
		// Nothing spawns and no tool can run, so an unusable server block
		// cannot hurt anyone yet. It is still worth saying, since the user will
		// flip enabled at some point and should not discover it then.
		for _, name := range sortedServerNames(m.Servers) {
			if err := validateServerBlock(m.Servers[name]); err != nil {
				c.warnf("mcp.servers.%s is not usable (agent mode is off, so nothing is running it yet): %v", name, err)
			}
		}
		return nil
	}

	if len(m.Servers) == 0 && m.Builtin.Disabled {
		c.warnf("mcp.enabled is true but the built-in tools are disabled and no servers are configured; " +
			"agent mode has no tools to offer and will behave like an ordinary turn")
	}

	for _, name := range sortedServerNames(m.Servers) {
		srv := m.Servers[name]
		if srv.Disabled {
			continue
		}
		if err := validateServerBlock(srv); err != nil {
			return fmt.Errorf("mcp.servers.%s: %w", name, err)
		}
		if err := validateToolPolicies("mcp.servers."+name, srv.Tools); err != nil {
			return err
		}
		if !srv.AcknowledgedUnconfined {
			return fmt.Errorf("mcp.servers.%s is an external MCP server, which this product cannot confine: "+
				"it is a subprocess with your full privileges, and the five-gate pipeline constrains only this "+
				"daemon's own writer. Set \"acknowledged_unconfined\": true on that server to run it anyway. "+
				"(The built-in tools under mcp.builtin need no such acknowledgement -- they are this daemon's own "+
				"code and cannot escape it.)", name)
		}
	}
	return nil
}

func validateServerBlock(s MCPServerConfig) error {
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("has no \"command\" to run")
	}
	for _, env := range s.Env {
		if strings.Contains(env, "=") {
			return fmt.Errorf("env entry %q looks like a NAME=VALUE pair; this list takes variable NAMES to "+
				"pass through from the daemon's environment, so that secrets stay out of your config file", env)
		}
	}
	return nil
}

func validateToolPolicies(where string, tools map[string]string) error {
	for _, tool := range sortedToolNames(tools) {
		switch tools[tool] {
		case PolicyDeny, PolicyAsk, PolicyAllow:
		default:
			return fmt.Errorf("%s: tool %q has policy %q, which is not one of %q, %q or %q -- "+
				"an unreadable policy is not treated as a default, because you would believe it was in force",
				where, tool, tools[tool], PolicyDeny, PolicyAsk, PolicyAllow)
		}
	}
	return nil
}

// clampMCPRanges bounds explicitly-set budget values, warning for each change.
// A zero is left alone: it means "use the default" and is resolved by the
// resolved* accessors, not here.
func (c *Config) clampMCPRanges() {
	b := &c.MCP.Budget
	clamp := func(field string, v *int, max int) {
		if *v < 0 {
			c.warnf("mcp.budget.%s %d is negative; using the default", field, *v)
			*v = 0
		} else if *v > max {
			c.warnf("mcp.budget.%s %d exceeds the maximum %d; clamped to %d", field, *v, max, max)
			*v = max
		}
	}
	clamp("max_iterations", &b.MaxIterations, maxMaxIterations)
	clamp("turn_timeout_seconds", &b.TurnTimeoutSeconds, maxTurnTimeoutSeconds)
	clamp("max_tool_result_bytes", &b.MaxToolResultBytes, maxMaxToolResultBytes)
	clamp("max_total_tool_bytes", &b.MaxTotalToolBytes, maxMaxTotalToolBytes)
	clamp("max_advertised_tools", &b.MaxAdvertisedTools, maxMaxAdvertisedTools)
	clamp("max_message_bytes", &b.MaxMessageBytes, maxMaxMessageBytes)

	// A per-result cap above the whole-turn cap is not wrong so much as
	// meaningless -- the turn cap would always bite first -- and it usually
	// means the two were set without reading each other.
	if b.MaxToolResultBytes > 0 && b.MaxTotalToolBytes > 0 && b.MaxToolResultBytes > b.MaxTotalToolBytes {
		c.warnf("mcp.budget.max_tool_result_bytes (%d) is larger than max_total_tool_bytes (%d), so the per-result cap can never apply",
			b.MaxToolResultBytes, b.MaxTotalToolBytes)
	}

	// A message limit below the per-result cap means the transport refuses the
	// server before the result cap ever gets to trim it: results that the user
	// explicitly sized for would arrive as a dead server instead. Warned rather
	// than corrected, because which of the two numbers is the mistake is the
	// user's call, not ours.
	if b.MaxMessageBytes > 0 && b.MaxToolResultBytes > 0 && b.MaxMessageBytes < b.MaxToolResultBytes {
		c.warnf("mcp.budget.max_message_bytes (%d) is smaller than max_tool_result_bytes (%d), so a "+
			"result that size is refused as an over-long message rather than truncated",
			b.MaxMessageBytes, b.MaxToolResultBytes)
	}
}

// warnMCPPolicySurface reports configuration that is legal but worth seeing,
// because these are the lines that decide whether a human is asked before a
// program runs. Warnings, not errors: the user is allowed to make this choice
// -- they are not allowed to make it without it being visible.
func (c *Config) warnMCPPolicySurface() {
	if !c.MCP.Enabled {
		return
	}

	// Lane A always-allowed tools are worth one line, but not the alarm Lane B
	// gets: these are confined, non-mutating, and ours.
	if allowed := allowedTools(c.MCP.Builtin.Tools); len(allowed) > 0 && !c.MCP.Builtin.Disabled {
		c.warnf("mcp.builtin: %d built-in tool(s) will run WITHOUT asking you: %s (these are confined and do not write to your files)",
			len(allowed), strings.Join(allowed, ", "))
	}

	for _, name := range sortedServerNames(c.MCP.Servers) {
		srv := c.MCP.Servers[name]
		if srv.Disabled {
			continue
		}
		allowed := allowedTools(srv.Tools)
		if len(allowed) > 0 {
			c.warnf("mcp.servers.%s: %d tool(s) are set to %q and will run WITHOUT asking you: %s. "+
				"That server is unconfined, so those calls are neither gated by a human nor constrained by the edit pipeline",
				name, len(allowed), PolicyAllow, strings.Join(allowed, ", "))
		}
		if len(srv.Env) > 0 {
			c.warnf("mcp.servers.%s inherits %d environment variable(s) from the daemon: %s",
				name, len(srv.Env), strings.Join(srv.Env, ", "))
		}
	}
}

func allowedTools(tools map[string]string) []string {
	var allowed []string
	for _, tool := range sortedToolNames(tools) {
		if tools[tool] == PolicyAllow {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}

// sortedServerNames and sortedToolNames give every loop over these maps a
// stable order, so warnings and `mcp list` output do not reshuffle between runs
// (Go map iteration order is randomised).
func sortedServerNames(m map[string]MCPServerConfig) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func sortedToolNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
