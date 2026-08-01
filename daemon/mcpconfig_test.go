package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// loadMCPConfig writes a models.json and loads it, returning the config and any
// HARD error -- unlike config_warnings_test.go's loadConfigBody, which fails the
// test on a load error, several cases here are specifically about configs the
// daemon must REFUSE. validTiers, warningsMentioning and loadConfigBody itself
// are reused from config_warnings_test.go rather than duplicated.
func loadMCPConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return LoadConfig(path)
}

// THE POLARITY RULE. Every one of these is a way the "absent means restrictive"
// default could be lost, and each would be silent: the daemon would run
// something the user never authorised and report nothing.
func TestMCPDefaultsAreRestrictive(t *testing.T) {
	t.Run("no mcp section means agent mode is off", func(t *testing.T) {
		cfg, err := loadMCPConfig(t, `{"config_version":1,`+validTiers+"}")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.MCP.Enabled {
			t.Error("a models.json with no mcp section reported agent mode ENABLED; " +
				"every config written before this feature existed would silently start spawning servers")
		}
		if len(cfg.MCP.Servers) != 0 {
			t.Errorf("expected no servers, got %d", len(cfg.MCP.Servers))
		}
	})

	t.Run("mcp section present but enabled unset means off", func(t *testing.T) {
		cfg, err := loadMCPConfig(t, `{"config_version":1,`+validTiers+`,"mcp":{"servers":{}}}`)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.MCP.Enabled {
			t.Error("an mcp section without \"enabled\":true reported agent mode ENABLED")
		}
	})

	t.Run("an unlisted tool is ask, never allow", func(t *testing.T) {
		srv := MCPServerConfig{Tools: map[string]string{"known": PolicyAllow}}
		if got := srv.policyFor("never_mentioned"); got != PolicyAsk {
			t.Errorf("policyFor(unlisted tool) = %q, want %q. An unlisted tool must involve the human: "+
				"the daemon does not get to decide a tool it was never told about is safe", got, PolicyAsk)
		}
	})

	t.Run("a server with no tools map asks about everything", func(t *testing.T) {
		srv := MCPServerConfig{}
		if got := srv.policyFor("anything"); got != PolicyAsk {
			t.Errorf("policyFor on a server with no tools map = %q, want %q", got, PolicyAsk)
		}
	})

	// The lane is no longer a field anyone can set -- it is decided by WHICH
	// map the entry lives in. This is the reshape's whole point: a field that
	// must say "third_party" for safety is a field that can be typo'd into
	// claiming something else.
	t.Run("lane and confinement are structural, not settable", func(t *testing.T) {
		if got := (MCPServerConfig{}).Lane(); got != protocol.LaneThirdParty {
			t.Errorf("every mcp.servers entry must be %q, got %q", protocol.LaneThirdParty, got)
		}
		if (MCPServerConfig{}).Confined() {
			t.Error("an external MCP server reported itself CONFINED; that value reaches the user on " +
				"the approval prompt and must never be optimistic")
		}
		if got := (MCPBuiltinConfig{}).Lane(); got != protocol.LaneFirstParty {
			t.Errorf("built-in tools must be %q, got %q", protocol.LaneFirstParty, got)
		}
		if !(MCPBuiltinConfig{}).Confined() {
			t.Error("built-in tools must report confined -- they are this daemon's own code")
		}
	})

	t.Run("an unlisted builtin tool is also ask", func(t *testing.T) {
		b := MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}}
		if got := b.policyFor("propose_edit"); got != PolicyAsk {
			t.Errorf("policyFor(unlisted builtin) = %q, want %q", got, PolicyAsk)
		}
	})
}

// A third-party server is a subprocess with the user's full privileges and
// nothing in this product confines it. Starting one must require the user to
// have typed the acknowledgement, not merely to have copied a config snippet.
func TestThirdPartyServerRequiresAcknowledgement(t *testing.T) {
	body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
		"scary":{"command":"/bin/echo"}}}}`

	_, err := loadMCPConfig(t, body)
	if err == nil {
		t.Fatal("a third_party server with no acknowledged_unconfined loaded successfully; " +
			"the daemon would spawn an unconfined process the user never explicitly consented to")
	}
	for _, want := range []string{"acknowledged_unconfined", "full privileges"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message %q does not mention %q -- it has to explain WHY, or the user "+
				"just adds the flag without understanding it", err, want)
		}
	}

	t.Run("with the acknowledgement it loads", func(t *testing.T) {
		ok := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
			"scary":{"command":"/bin/echo","acknowledged_unconfined":true}}}}`
		if _, err := loadMCPConfig(t, ok); err != nil {
			t.Fatalf("an acknowledged third_party server should load: %v", err)
		}
	})

	// The built-in tools are Lane A and need no acknowledgement, because there
	// is no subprocess to be unconfined. This is the reshape paying off: the
	// distinction is which map you are in, so it cannot be got wrong.
	t.Run("builtin tools need no acknowledgement", func(t *testing.T) {
		ok := "{" + validTiers + `,"mcp":{"enabled":true,
			"builtin":{"tools":{"read_file":"allow","propose_edit":"ask"}}}}`
		cfg, err := loadMCPConfig(t, ok)
		if err != nil {
			t.Fatalf("built-in tools should need no acknowledgement: %v", err)
		}
		if !cfg.MCP.Builtin.Confined() {
			t.Error("built-in tools must report confined")
		}
	})

	t.Run("a disabled server does not need one", func(t *testing.T) {
		ok := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
			"off":{"command":"/bin/echo","disabled":true}}}}`
		if _, err := loadMCPConfig(t, ok); err != nil {
			t.Fatalf("a disabled server is not going to run, so it should not block startup: %v", err)
		}
	})

	// The acknowledgement gate is about SPAWNING. With agent mode off nothing
	// spawns, so the file loads -- but the user still hears about it, because
	// discovering it at the moment they flip enabled is worse.
	t.Run("agent mode off downgrades the refusal to a warning", func(t *testing.T) {
		body := "{" + validTiers + `,"mcp":{"servers":{
			"scary":{}}}}`
		cfg, err := loadMCPConfig(t, body)
		if err != nil {
			t.Fatalf("with agent mode off this should load: %v", err)
		}
		if !mentions(cfg, "scary") {
			t.Errorf("expected a warning naming the unusable server; warnings were %v", cfg.Warnings())
		}
	})
}

// An unreadable policy is an error, not a default. The user believes the line
// they wrote is in force; silently substituting "ask" for "allowe" would be a
// kindness that hides a typo in the one setting that decides whether a human is
// consulted.
func TestUnreadablePolicyIsAHardError(t *testing.T) {
	for _, bad := range []string{"allowe", "ALLOW", "Allow", "yes", "true", "permit", ""} {
		t.Run("policy_"+bad, func(t *testing.T) {
			body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
				"s":{"command":"/bin/echo","acknowledged_unconfined":true,"tools":{"t":"` + bad + `"}}}}}`
			_, err := loadMCPConfig(t, body)
			if err == nil {
				t.Fatalf("policy %q was accepted; an unreadable policy must refuse the file", bad)
			}
			if !strings.Contains(err.Error(), "not one of") {
				t.Errorf("error %q should name the closed vocabulary", err)
			}
		})
	}

	for _, good := range []string{PolicyDeny, PolicyAsk, PolicyAllow} {
		t.Run("valid_"+good, func(t *testing.T) {
			body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
				"s":{"command":"/bin/echo","acknowledged_unconfined":true,"tools":{"t":"` + good + `"}}}}}`
			if _, err := loadMCPConfig(t, body); err != nil {
				t.Fatalf("policy %q should be accepted: %v", good, err)
			}
		})
	}
}

// The env list takes NAMES to pass through, never NAME=VALUE. Accepting the
// pair form would invite users to paste credentials into a file that gets
// committed.
func TestEnvListRejectsInlineValues(t *testing.T) {
	body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
		"s":{"command":"/bin/echo","acknowledged_unconfined":true,"env":["API_TOKEN=sk-secret"]}}}}`
	_, err := loadMCPConfig(t, body)
	if err == nil {
		t.Fatal("an env entry of the form NAME=VALUE was accepted; the config format must not invite " +
			"secrets to be pasted into a committed file")
	}
	if !strings.Contains(err.Error(), "NAME=VALUE") {
		t.Errorf("error %q should explain the expected shape", err)
	}

	ok := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
		"s":{"command":"/bin/echo","acknowledged_unconfined":true,"env":["API_TOKEN"]}}}}`
	if _, err := loadMCPConfig(t, ok); err != nil {
		t.Fatalf("a bare variable name should be accepted: %v", err)
	}
}

func TestServerWithoutCommandIsRefused(t *testing.T) {
	body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{"s":{"acknowledged_unconfined":true}}}}`
	if _, err := loadMCPConfig(t, body); err == nil {
		t.Fatal("a server with no command was accepted; there is nothing to run")
	}
}

// Budget values follow the retrieval convention: 0 means default, out-of-range
// is clamped WITH a warning rather than honoured or refused.
func TestBudgetDefaultsAndClamping(t *testing.T) {
	t.Run("zero resolves to the documented defaults", func(t *testing.T) {
		var b MCPBudgetConfig
		if got := b.resolvedMaxIterations(); got != defaultMaxIterations {
			t.Errorf("resolvedMaxIterations() = %d, want %d", got, defaultMaxIterations)
		}
		if got := b.resolvedTurnTimeout().Seconds(); int(got) != defaultTurnTimeoutSeconds {
			t.Errorf("resolvedTurnTimeout() = %vs, want %ds", got, defaultTurnTimeoutSeconds)
		}
		if got := b.resolvedMaxToolResultBytes(); got != defaultMaxToolResultBytes {
			t.Errorf("resolvedMaxToolResultBytes() = %d, want %d", got, defaultMaxToolResultBytes)
		}
		if got := b.resolvedMaxTotalToolBytes(); got != defaultMaxTotalToolBytes {
			t.Errorf("resolvedMaxTotalToolBytes() = %d, want %d", got, defaultMaxTotalToolBytes)
		}
	})

	t.Run("an absurd max_iterations is clamped and reported", func(t *testing.T) {
		body := "{" + validTiers + `,"mcp":{"budget":{"max_iterations":800}}}`
		cfg, err := loadMCPConfig(t, body)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.MCP.Budget.MaxIterations != maxMaxIterations {
			t.Errorf("max_iterations = %d, want it clamped to %d -- one config typo must not turn "+
				"one prompt into a bill", cfg.MCP.Budget.MaxIterations, maxMaxIterations)
		}
		if !mentions(cfg, "max_iterations") {
			t.Errorf("clamping must be reported, not silent; warnings were %v", cfg.Warnings())
		}
	})

	t.Run("a negative value falls back to the default with a warning", func(t *testing.T) {
		body := "{" + validTiers + `,"mcp":{"budget":{"turn_timeout_seconds":-5}}}`
		cfg, err := loadMCPConfig(t, body)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got := cfg.MCP.Budget.resolvedTurnTimeout().Seconds(); int(got) != defaultTurnTimeoutSeconds {
			t.Errorf("negative turn_timeout_seconds resolved to %vs, want the default %ds", got, defaultTurnTimeoutSeconds)
		}
		if !mentions(cfg, "turn_timeout_seconds") {
			t.Errorf("expected a warning; got %v", cfg.Warnings())
		}
	})
}

// A misspelled key means the setting the user thought they made is not in
// effect. The existing config loader turns that silence into a warning; the mcp
// section must be wired into the same machinery or every key under it is
// silently discarded.
func TestUnknownMCPKeysAreReported(t *testing.T) {
	body := "{" + validTiers + `,"mcp":{
		"enabled":true,
		"enbaled":true,
		"budget":{"max_iterations":4,"max_iteratons":9},
		"servers":{"s":{"command":"/bin/echo","acknowledged_unconfined":true,"toolz":{}}}}}`
	cfg, err := loadMCPConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []string{"mcp.enbaled", "mcp.budget.max_iteratons", "mcp.servers.s.toolz"} {
		if !mentions(cfg, want) {
			t.Errorf("misspelled key %q was silently discarded; warnings were %v", want, cfg.Warnings())
		}
	}
}

// Legal-but-loud configuration. The user is allowed to make these choices; they
// are not allowed to make them invisibly.
func TestPolicySurfaceIsReported(t *testing.T) {
	body := "{" + validTiers + `,"mcp":{"enabled":true,"servers":{
		"risky":{"command":"/bin/echo","acknowledged_unconfined":true,
		         "env":["GITHUB_TOKEN"],
		         "tools":{"exec":"allow","read":"ask"}}}}}`
	cfg, err := loadMCPConfig(t, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !mentions(cfg, "WITHOUT asking you") {
		t.Errorf("an always-allowed tool must be reported; warnings were %v", cfg.Warnings())
	}
	if !mentions(cfg, "neither gated by a human nor constrained") {
		t.Errorf("unconfined + always-allowed is the riskiest combination and must be called out; warnings were %v",
			cfg.Warnings())
	}
	if !mentions(cfg, "GITHUB_TOKEN") {
		t.Errorf("an inherited environment variable must be reported by name; warnings were %v", cfg.Warnings())
	}
	// "read":"ask" is the default posture and must NOT generate noise, or the
	// warnings that matter get lost among the ones that don't.
	if mentions(cfg, `"read"`) {
		t.Errorf("an ordinary ask-policy tool should not warn; warnings were %v", cfg.Warnings())
	}
}

// A config that predates this feature must load byte-for-byte unchanged in
// behaviour. This is the repo's own models.json, which is the config that
// actually ships.
func TestShippedConfigStillLoadsWithAgentModeOff(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("the shipped models.json must load: %v", err)
	}
	if cfg.MCP.Enabled {
		t.Error("the shipped models.json reported agent mode ENABLED; it has no mcp section and must stay off")
	}
	for _, w := range cfg.Warnings() {
		if strings.Contains(w, "mcp") {
			t.Errorf("adding the mcp section made the shipped config warn: %q", w)
		}
	}
}

// `json:"mcp,omitempty"` does NOT omit an unset MCP section, because omitempty
// has no effect on a struct field -- a struct is never "empty" to encoding/json.
// So a marshalled Config carries `"mcp":{"budget":{}}` exactly as it already
// carries `"retrieval":{}` and `"zdr":{}`.
//
// That is fine, and this test exists to say so deliberately rather than leave
// the next reader to wonder whether the omitempty tag is load-bearing. Config is
// only ever UNMARSHALLED from models.json -- nothing writes it back -- so the
// marshalled form reaches no user and no wire. What actually protects existing
// configs is that the zero value means "off" (TestMCPDefaultsAreRestrictive) and
// that the shipped models.json still loads clean
// (TestShippedConfigStillLoadsWithAgentModeOff).
//
// If Config ever DOES get written back to disk, this test fails and the fix is a
// *MCPConfig pointer, not a tag.
func TestEmptyMCPSectionMarshalsInertlyLikeItsNeighbours(t *testing.T) {
	b, err := json.Marshal(Config{ConfigVersion: 1, DefaultTier: "primary"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	// The section is present-but-inert, in the same shape as the two sections
	// that already behave this way.
	for _, want := range []string{`"retrieval":{}`, `"zdr":{}`, `"mcp":{"builtin":{},"budget":{}}`} {
		if !strings.Contains(got, want) {
			t.Errorf("marshalled config %s does not contain %s; if the shape of these sections "+
				"changed, re-check whether Config is now written back to disk anywhere", got, want)
		}
	}

	// The inert form must round-trip back to agent mode OFF. This is the
	// property that actually matters.
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back.MCP.Enabled {
		t.Error("an inert marshalled mcp section round-tripped to agent mode ENABLED")
	}
}

// mentions is a boolean shorthand over config_warnings_test.go's
// warningsMentioning, which returns the matching warnings themselves.
func mentions(cfg *Config, substr string) bool {
	return len(warningsMentioning(cfg, substr)) > 0
}
