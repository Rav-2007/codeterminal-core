package mcp

import (
	"slices"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// THE ENVIRONMENT DISCIPLINE.
//
// This is the most consequential thing in the package. exec.Cmd with a nil Env
// inherits the daemon's ENTIRE environment, which is where the inference
// credentials live -- so the failure mode is not "a server misbehaves", it is
// "every MCP server the user ever runs is handed a key that spends their
// money", silently and permanently.
//
// These tests must fail if ServerEnv is neutered to `return os.Environ()`.
func TestServerEnvNeverLeaksCredentials(t *testing.T) {
	// The daemon's own environment, as it really is at the moment a server is
	// spawned: full of things a server must not see.
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-must-not-leak")
	t.Setenv("CODETERMINAL_API_KEY", "must-not-leak")
	t.Setenv("CODETERMINAL_MOCHIII_KEY", "mochi_must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated-but-also-not-asked-for")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/someone")

	t.Run("nothing is inherited by default", func(t *testing.T) {
		env := ServerEnv(nil)

		if got := names(env); !slices.Equal(got, []string{"PATH", "HOME"}) {
			t.Errorf("ServerEnv(nil) passed %v, want exactly [PATH HOME]. Anything else means the "+
				"daemon's environment is being inherited", got)
		}
		assertNoSecrets(t, env)
	})

	t.Run("an allow-list cannot grant this product's own credentials", func(t *testing.T) {
		// A config that explicitly asks for the inference keys. The allow-list
		// exists so a server can have a credential OF ITS OWN; it is not a
		// mechanism for handing over ours, and asking loudly does not change
		// that.
		env := ServerEnv([]string{"OPENROUTER_API_KEY", "CODETERMINAL_MOCHIII_KEY", "CODETERMINAL_API_KEY"})

		if got := names(env); !slices.Equal(got, []string{"PATH", "HOME"}) {
			t.Errorf("an allow-list naming the inference keys produced %v; those names must be "+
				"ungrantable, not merely absent by default", got)
		}
		assertNoSecrets(t, env)
	})

	t.Run("a case-variant spelling is refused too", func(t *testing.T) {
		env := ServerEnv([]string{"openrouter_api_key", "OpenRouter_Api_Key"})
		assertNoSecrets(t, env)
	})

	t.Run("an unrelated variable is passed only when asked for", func(t *testing.T) {
		if got := names(ServerEnv(nil)); slices.Contains(got, "AWS_SECRET_ACCESS_KEY") {
			t.Error("an unrequested variable reached the child")
		}
		got := names(ServerEnv([]string{"AWS_SECRET_ACCESS_KEY"}))
		if !slices.Contains(got, "AWS_SECRET_ACCESS_KEY") {
			t.Errorf("an explicitly allow-listed variable did not reach the child; got %v", got)
		}
	})

	t.Run("a variable the daemon does not have is simply absent", func(t *testing.T) {
		got := names(ServerEnv([]string{"DEFINITELY_NOT_SET_ANYWHERE"}))
		if slices.Contains(got, "DEFINITELY_NOT_SET_ANYWHERE") {
			t.Error("an unset variable was passed through, presumably as an empty string")
		}
	})

	t.Run("duplicates do not duplicate", func(t *testing.T) {
		env := ServerEnv([]string{"PATH", "PATH", "HOME"})
		if got := names(env); !slices.Equal(got, []string{"PATH", "HOME"}) {
			t.Errorf("ServerEnv produced %v; a repeated name must not produce a repeated entry", got)
		}
	})
}

// The allow-list is validated at config load so a refused variable is a startup
// error naming the offending line, rather than a silent omission the user
// discovers when their server mysteriously cannot authenticate.
func TestValidateEnvAllowList(t *testing.T) {
	if err := ValidateEnvAllowList([]string{"GITHUB_TOKEN", "HOME"}); err != nil {
		t.Errorf("an ordinary allow-list should validate: %v", err)
	}

	for _, forbidden := range ForbiddenEnvNames {
		err := ValidateEnvAllowList([]string{"GITHUB_TOKEN", forbidden})
		if err == nil {
			t.Errorf("an allow-list naming %q validated; it must be refused at load time", forbidden)
			continue
		}
		if !strings.Contains(err.Error(), forbidden) {
			t.Errorf("the refusal for %q does not name it: %v", forbidden, err)
		}
		// The message has to explain the stake, or the user just deletes the
		// line without understanding why it was there.
		if !strings.Contains(err.Error(), "quota") && !strings.Contains(err.Error(), "money") {
			t.Errorf("the refusal for %q does not say what is at stake: %v", forbidden, err)
		}
	}
}

// A qualified name is the key the loop dispatches on, and the model is the one
// handing it back to us. Two servers may legitimately both offer "read_file",
// and a collision resolved by guessing would route a user's approval of one to
// the other.
func TestQualifiedNames(t *testing.T) {
	tool := Tool{Server: "github", Name: "create_issue"}
	if got := tool.QualifiedName(); got != "github__create_issue" {
		t.Errorf("QualifiedName() = %q", got)
	}

	server, name, err := SplitQualifiedName("github__create_issue")
	if err != nil || server != "github" || name != "create_issue" {
		t.Errorf("SplitQualifiedName = (%q, %q, %v)", server, name, err)
	}

	// Anything the daemon did not generate is refused, not guessed at.
	for _, bad := range []string{"", "no_separator", "__leading", "trailing__", "__"} {
		if _, _, err := SplitQualifiedName(bad); err == nil {
			t.Errorf("SplitQualifiedName(%q) succeeded; an unrecognised name must be refused, "+
				"because it is the key we dispatch a tool call on", bad)
		}
	}
}

func TestValidateServerName(t *testing.T) {
	if err := ValidateServerName("github"); err != nil {
		t.Errorf("an ordinary name should validate: %v", err)
	}

	// "builtin" is reserved. An external server allowed to claim it could
	// present unconfined tools under the name the confined ones use.
	err := ValidateServerName(BuiltinServerName)
	if err == nil {
		t.Fatal("a server named \"builtin\" validated; it could shadow the confined tools")
	}
	if !strings.Contains(err.Error(), "shadow") {
		t.Errorf("the refusal should say what the risk is: %v", err)
	}

	if err := ValidateServerName("has__separator"); err == nil {
		t.Error("a server name containing the qualified-name separator validated; " +
			"server and tool would be impossible to tell apart")
	}
	if err := ValidateServerName("  "); err == nil {
		t.Error("a blank server name validated")
	}
}

// Confinement is a property of the lane, not a value anyone computes per call.
// This pins that a Lane B tool can never report itself confined, since that
// value reaches the user on the approval prompt.
func TestLaneBToolsAreNeverConfined(t *testing.T) {
	tool := Tool{
		Server: "anything", Name: "t",
		Lane: protocol.LaneThirdParty, Confined: false,
		// A server claiming to be harmless changes nothing.
		ReadOnlyHint: true, Destructive: false,
	}
	if tool.Confined {
		t.Error("a third-party tool reported confined")
	}
	if tool.Lane != protocol.LaneThirdParty {
		t.Errorf("lane = %q", tool.Lane)
	}
}

// Advertised order must be deterministic: Go map iteration is randomised, and a
// tool list that reshuffles between turns both defeats prompt caching and makes
// noise indistinguishable from a real change.
func TestSortToolsIsDeterministic(t *testing.T) {
	tools := []Tool{
		{Server: "zeta", Name: "b"},
		{Server: "alpha", Name: "z"},
		{Server: "zeta", Name: "a"},
		{Server: "alpha", Name: "a"},
	}
	SortTools(tools)

	var got []string
	for _, tool := range tools {
		got = append(got, tool.QualifiedName())
	}
	want := []string{"alpha__a", "alpha__z", "zeta__a", "zeta__b"}
	if !slices.Equal(got, want) {
		t.Errorf("SortTools produced %v, want %v", got, want)
	}
}

func names(env []string) []string {
	var out []string
	for _, kv := range env {
		out = append(out, strings.SplitN(kv, "=", 2)[0])
	}
	return out
}

// assertNoSecrets checks the VALUES too, not just the names: a bug that renamed
// a variable while still copying its value would pass a names-only check.
func assertNoSecrets(t *testing.T, env []string) {
	t.Helper()
	for _, kv := range env {
		for _, secret := range []string{"sk-or-v1-must-not-leak", "mochi_must-not-leak", "must-not-leak"} {
			if strings.Contains(kv, secret) {
				name := strings.SplitN(kv, "=", 2)[0]
				t.Errorf("a credential value reached the child environment under the name %q", name)
			}
		}
	}
}
