package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
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
	t.Setenv("MOCHIII_API_KEY", "must-not-leak")
	t.Setenv("MOCHIII_PROXY_KEY", "mochi_must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "unrelated-but-also-not-asked-for")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/someone")

	t.Run("nothing is inherited by default", func(t *testing.T) {
		env := ServerEnv(nil)
		got := names(env)

		// EVERY name must be one this package chose. Asserted as a subset of the
		// baseline rather than as an exact list, because the baseline is
		// PLATFORM-SHAPED: POSIX gets [PATH HOME], Windows gets nine more that a
		// child there genuinely cannot run without (see baselineEnvNames). The
		// old assertion hardcoded [PATH HOME] and so encoded the POSIX shape as
		// if it were the contract -- it failed all three of these subtests on the
		// Windows runner in CI run #42, against correct code.
		//
		// This is not weaker. It is the property the subtest is named for, and it
		// still fails hard against `return os.Environ()`: the daemon's real
		// environment contains AWS_SECRET_ACCESS_KEY and the three inference keys
		// set above, none of which is in any baseline.
		var uninvited []string
		for _, name := range got {
			if !slices.Contains(BaselineEnvNames, name) {
				uninvited = append(uninvited, name)
			}
		}
		// Collected and reported ONCE. Reported per-name, the neuter check dumped
		// the whole inherited environment about 130 times -- roughly half a
		// megabyte of CI log in which the actual finding was unfindable. A test
		// whose failure output cannot be read is most of the way to a test nobody
		// reads.
		if len(uninvited) > 0 {
			t.Errorf("ServerEnv(nil) passed %d variable(s) nobody asked for: %v.\n"+
				"The baseline for this platform is %v; anything outside it means the daemon's "+
				"environment is being inherited.", len(uninvited), truncate(uninvited, 10), BaselineEnvNames)
		}
		// A child with no PATH cannot exec anything, on any platform.
		if !slices.Contains(got, "PATH") {
			t.Errorf("ServerEnv(nil) = %v, with no PATH; a server subprocess cannot run without it", got)
		}
		assertNoSecrets(t, env)
	})

	t.Run("an allow-list cannot grant this product's own credentials", func(t *testing.T) {
		// A config that explicitly asks for the inference keys. The allow-list
		// exists so a server can have a credential OF ITS OWN; it is not a
		// mechanism for handing over ours, and asking loudly does not change
		// that.
		asked := []string{"OPENROUTER_API_KEY", "MOCHIII_PROXY_KEY", "MOCHIII_API_KEY"}
		env := ServerEnv(asked)

		// The property is that ASKING DOES NOT GRANT -- stated directly, rather
		// than inferred from the whole list matching a POSIX-shaped snapshot.
		got := names(env)
		for _, name := range asked {
			if slices.Contains(got, name) {
				t.Errorf("an allow-list naming %q was honoured; this product's own inference "+
					"credentials must be ungrantable, not merely absent by default. Got %v", name, got)
			}
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
		// Stated as "no name appears twice", which is the actual rule. The old
		// form compared against [PATH HOME] and so also asserted the platform's
		// whole baseline as a side effect -- which is why it went red on Windows
		// for a reason that had nothing to do with duplicates.
		seen := map[string]bool{}
		got := names(env)
		for _, name := range got {
			if seen[name] {
				t.Errorf("ServerEnv produced %v; %q appears more than once", got, name)
			}
			seen[name] = true
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
// A Lane B tool can never report itself confined, because that value reaches
// the user on the approval prompt and is the whole basis on which they decide.
//
// THIS TEST USED TO ASSERT A LITERAL. It built Tool{Confined: false} by hand
// and checked that false was false -- no code under test, no input that could
// have moved it, and a doc comment claiming it pinned the property. That is the
// shape this project keeps finding and keeps writing down: a test that cannot
// fail is not a check, it is a place nobody looks.
//
// The guarantee actually lives in StdioClient.ListTools, which builds every
// Tool with Confined:false and Lane:third_party without consulting anything the
// server said. So the assertion is made where it is kept: a real subprocess,
// over a real stdio pipe, asked what it advertises.
func TestLaneBToolsAreNeverConfined(t *testing.T) {
	client := connectEcho(t, nil)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	// ANTI-VACUITY, the failure this test is being rescued from: an empty list
	// would satisfy every loop below.
	if len(tools) == 0 {
		t.Fatal("the server advertised nothing, so this test compares over an empty list")
	}

	for _, tool := range tools {
		if tool.Confined {
			t.Errorf("tool %q came back confined, so a client would print "+
				"\"anything it changes goes through the same review you use for edits\" over a "+
				"separate program running with the user's full privileges", tool.QualifiedName())
		}
		if tool.Lane != protocol.LaneThirdParty {
			t.Errorf("tool %q reported lane %q, so a client cannot tell it is third-party",
				tool.QualifiedName(), tool.Lane)
		}
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

// truncate keeps a failure message readable when the "wrong" answer is the
// entire environment: the first n names are enough to identify what happened,
// and the count above says how bad it is.
func truncate(names []string, n int) string {
	if len(names) <= n {
		return fmt.Sprint(names)
	}
	return fmt.Sprintf("%v ...and %d more", names[:n], len(names)-n)
}
