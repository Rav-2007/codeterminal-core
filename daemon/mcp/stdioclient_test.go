package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// These tests spawn a REAL MCP server subprocess and speak the real protocol
// over it. The fakes in registry_test.go prove the registry's logic; only this
// proves the SDK adapter, and in particular that the environment discipline
// survives contact with an actual process rather than merely producing the
// right []string.
//
// Built fresh from source (the eval_test.go convention) so the test exercises
// current code rather than a stale binary.
func buildEchoServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), exeName("echoserver"))
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/echoserver")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the test MCP server: %v\n%s", err, out)
	}
	return bin
}

func connectEcho(t *testing.T, envAllow []string) *StdioClient {
	t.Helper()
	client, err := Connect(context.Background(), LaunchConfig{
		Name:     "echo",
		Command:  buildEchoServer(t),
		EnvAllow: envAllow,
		Stderr:   os.Stderr,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// THE TEST THIS PACKAGE EXISTS FOR. A real subprocess, asked what it can see.
//
// Must fail if ServerEnv is neutered, if Connect stops setting cmd.Env, or if
// anyone "helpfully" makes the allow-list additive over os.Environ().
func TestRealServerNeverSeesCredentials(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-must-not-reach-the-child")
	t.Setenv("CODETERMINAL_MOCHIII_KEY", "mochi_must-not-reach-the-child")
	t.Setenv("CODETERMINAL_API_KEY", "must-not-reach-the-child")
	t.Setenv("A_SERVERS_OWN_TOKEN", "this one is legitimately its own")

	client := connectEcho(t, []string{"A_SERVERS_OWN_TOKEN"})

	res, err := client.CallTool(context.Background(), "env_names", nil)
	if err != nil {
		t.Fatalf("CallTool(env_names): %v", err)
	}
	seen := strings.Split(res.Content, ",")

	for _, forbidden := range ForbiddenEnvNames {
		if slices.Contains(seen, forbidden) {
			t.Errorf("a real MCP server subprocess could see %s. That key can spend the user's "+
				"money or quota, and no MCP server needs it", forbidden)
		}
	}
	if !slices.Contains(seen, "A_SERVERS_OWN_TOKEN") {
		t.Errorf("the allow-listed variable did not reach the child; it saw %v", seen)
	}
	// The baseline comes free; nothing else may be there.
	//
	// Against BaselineEnvNames rather than a hardcoded {PATH, HOME}, because the
	// baseline is platform-shaped and a POSIX literal here was wrong twice over
	// on Windows: HOME does not exist, and SYSTEMROOT arrives whether or not
	// ServerEnv passes it -- os/exec's addCriticalEnv appends it to every Cmd
	// below this scrubber. The first Windows CI run failed on exactly that.
	//
	// This still fails loudly on a leak: BaselineEnvNames is a fixed list of
	// non-secret variables, ForbiddenEnvNames is checked separately above, and
	// anything outside both is an error. Widening the baseline to hide a leak
	// would mean editing the named list in mcp.go, which is a visible diff.
	allowed := append(append([]string{}, BaselineEnvNames...), "A_SERVERS_OWN_TOKEN")
	for _, name := range seen {
		if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, name) }) {
			t.Errorf("the child saw %q, which was neither allow-listed nor a baseline variable (baseline: %v)",
				name, BaselineEnvNames)
		}
	}
}

// The baseline must never be a route to a credential, however the platform
// spells its variables. Cheap, and it is the one property that a
// platform-varying list could quietly lose.
func TestBaselineEnvNamesAreNeverCredentials(t *testing.T) {
	for _, name := range BaselineEnvNames {
		if IsForbiddenEnvName(name) {
			t.Errorf("%q is in the unconditional baseline AND in ForbiddenEnvNames; the baseline "+
				"would hand every MCP server a credential this daemon refuses to grant on request", name)
		}
	}
}

// A config that explicitly demands the inference keys is refused BEFORE
// anything is spawned, rather than spawned-then-filtered.
//
// Two independent mechanisms stop this, and that redundancy is deliberate:
// ValidateEnvAllowList refuses the config here, and ServerEnv would drop the
// variable even if it did not (asserted in mcp_test.go). Either alone is
// sufficient; both means removing one does not silently open the hole.
func TestAllowListCannotGrantCredentialsToARealServer(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-asked-for-loudly")

	_, err := Connect(context.Background(), LaunchConfig{
		Name:     "echo",
		Command:  buildEchoServer(t),
		EnvAllow: []string{"OPENROUTER_API_KEY"},
	})
	if err == nil {
		t.Fatal("a config demanding OPENROUTER_API_KEY started a server; asking loudly must not work")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the refusal %q should explain what was refused and why", err)
	}
}

func TestListToolsOverRealTransport(t *testing.T) {
	client := connectEcho(t, nil)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)

		// Every Lane B tool, unconditionally.
		if tool.Confined {
			t.Errorf("tool %q from a subprocess reported CONFINED; that value reaches the user "+
				"on the approval prompt", tool.Name)
		}
		if tool.Lane != protocol.LaneThirdParty {
			t.Errorf("tool %q has lane %q, want %q", tool.Name, tool.Lane, protocol.LaneThirdParty)
		}
		if tool.Server != "echo" {
			t.Errorf("tool %q has server %q, want the CONFIGURED name", tool.Name, tool.Server)
		}
		// The schema has to be usable JSON or the model gets an unusable entry.
		if !json.Valid(tool.Schema) {
			t.Errorf("tool %q has an invalid schema: %s", tool.Name, tool.Schema)
		}
	}

	slices.Sort(names)
	// claims_to_be_safe exists so a client that started BELIEVING a server's
	// self-description has somewhere to show up: it declares itself read-only
	// and non-destructive, and neither claim may move Confined above. See
	// TestLaneBToolsAreNeverConfined.
	if !slices.Equal(names, []string{"always_fails", "claims_to_be_safe", "echo", "env_names"}) {
		t.Errorf("ListTools returned %v", names)
	}
}

func TestCallToolOverRealTransport(t *testing.T) {
	client := connectEcho(t, nil)

	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError || res.Content != "echo: hello" {
		t.Errorf("result = %+v, want the echoed text", res)
	}
}

// A tool that fails is not a transport failure. The model must see the failure
// text so it can recover; swallowing it leaves the model to repeat the call.
func TestToolLevelErrorIsNotATransportError(t *testing.T) {
	client := connectEcho(t, nil)

	res, err := client.CallTool(context.Background(), "always_fails", nil)
	if err != nil {
		t.Fatalf("a tool-level failure surfaced as a Go error: %v", err)
	}
	if !res.IsError {
		t.Error("result did not carry IsError")
	}
	if !strings.Contains(res.Content, "always fails") {
		t.Errorf("the tool's own error text was lost: %q", res.Content)
	}
}

// Malformed arguments come back as a tool error the model can read and correct,
// never as a guess at what it meant.
func TestMalformedArgumentsAreRefusedNotGuessed(t *testing.T) {
	client := connectEcho(t, nil)

	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text": unquoted}`))
	if err != nil {
		t.Fatalf("malformed arguments should be a tool error, not a Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "valid JSON") {
		t.Errorf("result = %+v, want a readable complaint about the arguments", res)
	}
}

// Close must actually reap the process, and be safe to call twice.
func TestCloseReapsTheSubprocess(t *testing.T) {
	client, err := Connect(context.Background(), LaunchConfig{
		Name:    "echo",
		Command: buildEchoServer(t),
		Stderr:  os.Stderr,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	client.mu.Lock()
	pid := client.cmd.Process.Pid
	client.mu.Unlock()
	if pid <= 0 {
		t.Fatal("no process handle after Connect -- we cannot enforce our own teardown")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Signal 0 probes liveness without delivering anything.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("the server subprocess (pid %d) is still alive after Close", pid)
	}

	if err := client.Close(); err != nil {
		t.Errorf("Close must be idempotent -- the registry may close a server that already died: %v", err)
	}

	// And a closed client refuses work rather than panicking on a nil session.
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Error("ListTools succeeded on a closed client")
	}
	if _, err := client.CallTool(context.Background(), "echo", nil); err == nil {
		t.Error("CallTool succeeded on a closed client")
	}
}

// A server that cannot start is a degradation, not a crash, and it must be
// recognisable as one.
func TestConnectFailuresAreRecognisable(t *testing.T) {
	_, err := Connect(context.Background(), LaunchConfig{
		Name:    "missing",
		Command: "/definitely/not/a/real/binary",
	})
	if err == nil {
		t.Fatal("connecting to a nonexistent binary succeeded")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("error %q should be recognisable as ErrServerUnavailable so the caller can "+
			"report a DegradedMCPServer rather than failing the turn", err)
	}
}

// Validation happens before anything is spawned.
func TestConnectValidatesBeforeSpawning(t *testing.T) {
	cases := []struct {
		name string
		cfg  LaunchConfig
		want string
	}{
		{"reserved name", LaunchConfig{Name: BuiltinServerName, Command: "/bin/true"}, "reserved"},
		{"separator in name", LaunchConfig{Name: "a__b", Command: "/bin/true"}, "impossible to tell apart"},
		{"no command", LaunchConfig{Name: "s"}, "no command"},
		{"forbidden env", LaunchConfig{Name: "s", Command: "/bin/true",
			EnvAllow: []string{"OPENROUTER_API_KEY"}}, "credentials"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Connect(context.Background(), tc.cfg)
			if err == nil {
				t.Fatal("expected a refusal before anything was spawned")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}
