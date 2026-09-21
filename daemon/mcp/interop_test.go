package mcp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// INTEROP WITH A SERVER NOBODY HERE WROTE.
//
// echoserver and badserver are both built on the same Go SDK this client uses,
// which is a real limit on what they prove: a bug in the SDK cancels itself out
// when it sits on both ends of the wire. The entire point of MCP support is
// talking to servers written by other people, in other languages, so at least
// once that has to actually be done.
//
// NOT PART OF `make check`, and it never will be. It shells out to npx, which
// wants a network on a cold cache, and CI has neither the network nor any
// business depending on npm's availability. It is committed so the run is
// repeatable and so its result can be cited rather than remembered:
//
//	MCP_INTEROP=1 go test ./mcp/ -run TestInterop -v
//
// The default target is the MCP project's own reference server, which exists
// precisely to be tested against.
const (
	interopEnv     = "MCP_INTEROP"
	interopCmdEnv  = "MCP_INTEROP_COMMAND"
	interopArgsEnv = "MCP_INTEROP_ARGS"
)

func interopLaunch(t *testing.T) LaunchConfig {
	t.Helper()
	if os.Getenv(interopEnv) == "" {
		t.Skipf("set %s=1 to run interop against a third-party MCP server", interopEnv)
	}

	command := os.Getenv(interopCmdEnv)
	args := strings.Fields(os.Getenv(interopArgsEnv))
	if command == "" {
		command = "npx"
		args = []string{"-y", "@modelcontextprotocol/server-everything", "stdio"}
	}
	if _, err := exec.LookPath(command); err != nil {
		t.Skipf("%s is not on PATH: %v", command, err)
	}

	return LaunchConfig{
		Name:    "interop",
		Command: command,
		Args:    args,
		Stderr:  os.Stderr,
		Logf:    t.Logf,
		// A cold npm cache downloads the package before the server says a word,
		// which is exactly the "slowness, not malice" case DefaultConnectTimeout
		// is sized for -- and more than it allows on a first run.
		ConnectTimeout: 120 * time.Second,
	}
}

// The whole client surface against a real third-party server: connect, list,
// call, close.
func TestInteropWithAThirdPartyServer(t *testing.T) {
	cfg := interopLaunch(t)

	start := time.Now()
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Connect(%s %s): %v", cfg.Command, strings.Join(cfg.Args, " "), err)
	}
	t.Cleanup(func() { _ = client.Close() })
	t.Logf("interop: handshake with %s in %s", cfg.Command, time.Since(start).Round(time.Millisecond))

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("a server that advertises no tools proves nothing about interop")
	}

	// The invariants that must hold for ANY server, stated as such: these are
	// facts about what a subprocess IS, and no server's self-description moves
	// them.
	for _, tool := range tools {
		if tool.Confined {
			t.Errorf("tool %q reported itself confined", tool.QualifiedName())
		}
		if tool.Lane != "third_party" {
			t.Errorf("tool %q reported lane %q", tool.QualifiedName(), tool.Lane)
		}
		if tool.Server != cfg.Name {
			t.Errorf("tool %q claims server %q rather than the user's configured name %q",
				tool.Name, tool.Server, cfg.Name)
		}
		if err := ValidateToolName(tool.Name); err != nil {
			t.Errorf("an advertised name failed the validator that gates advertising: %v", err)
		}
		if len(tool.Schema) == 0 {
			t.Errorf("tool %q arrived with no schema, so the model cannot call it", tool.QualifiedName())
		}
	}

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	t.Logf("interop: %d tool(s): %s", len(tools), strings.Join(names, ", "))

	// The reference server's echo takes {"message": ...}. Skipped rather than
	// failed against some other server, since this half is about the CALL path
	// working at all and a different server has different tools.
	var echo *Tool
	for i := range tools {
		if tools[i].Name == "echo" {
			echo = &tools[i]
		}
	}
	if echo == nil {
		t.Log("interop: no `echo` tool; skipping the call half")
		return
	}

	res, err := client.CallTool(context.Background(), echo.Name,
		[]byte(`{"message":"interop probe"}`))
	if err != nil {
		t.Fatalf("CallTool(echo): %v", err)
	}
	if res.IsError {
		t.Errorf("echo came back as a tool error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "interop probe") {
		t.Errorf("echo did not round-trip; got %q", res.Content)
	}
	t.Logf("interop: echo returned %q", res.Content)
}

// The environment discipline against a server we did not write.
//
// mcp_test.go asserts ServerEnv's output and stdioclient_test.go proves it
// reaches a real subprocess -- but that subprocess is ours, and ours is not the
// one that matters. This is the same proof against a Node process launched
// through npx, which is the shape a user's actual config has.
//
// It cannot ask the server what it sees (a third-party server has no
// env_names tool), so it asserts the thing that is actually under this
// daemon's control: what was put in the child's environment.
func TestInteropServerEnvironmentCarriesNoCredentials(t *testing.T) {
	cfg := interopLaunch(t)

	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-must-not-reach-the-child")
	t.Setenv("MOCHIII_PROXY_KEY", "mochi_must-not-reach-the-child")

	env := ServerEnv(cfg.EnvAllow)
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if IsForbiddenEnvName(name) {
			t.Errorf("%s was about to be handed to a third-party server", name)
		}
		if strings.HasPrefix(value, "sk-or-v1-") || strings.HasPrefix(value, "mochi_") {
			t.Errorf("%s carries a value shaped like one of this product's credentials", name)
		}
	}

	// And the launch really does use it, rather than this testing a function
	// the launch path has quietly stopped calling.
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	t.Logf("interop: child environment was %d variable(s): %s", len(env), envNames(env))

	// BETTER THAN ASSERTING WHAT WE BUILT: asking the child what it got.
	//
	// The reference server ships a get-env tool, so this stops being a test of
	// ServerEnv's return value and becomes a test of what a Node process
	// launched through npx can actually read -- which is the question. Skipped
	// against a server that has no such tool, because there is then no way to
	// ask and inventing one would be worse than saying so.
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var getEnv string
	for _, tool := range tools {
		if tool.Name == "get-env" || tool.Name == "env_names" {
			getEnv = tool.Name
		}
	}
	if getEnv == "" {
		t.Log("interop: no env-reporting tool; the child's own view is unverified here")
		return
	}

	res, err := client.CallTool(context.Background(), getEnv, []byte(`{}`))
	if err != nil {
		t.Fatalf("CallTool(%s): %v", getEnv, err)
	}
	for _, forbidden := range ForbiddenEnvNames {
		if strings.Contains(res.Content, forbidden) {
			t.Errorf("a third-party Node server could see %s. That key can spend the user's money "+
				"or quota, and no MCP server needs it", forbidden)
		}
	}
	for _, shape := range []string{"sk-or-v1-", "mochi_"} {
		if strings.Contains(res.Content, shape) {
			t.Errorf("the child's environment carries a value beginning %q", shape)
		}
	}

	// WHAT THE CHILD SEES IS NOT WHAT WE HANDED IT, and the interop run is what
	// made that visible. ServerEnv passed 2 variables (PATH, HOME); the server
	// reports 25, because npx injects its own -- NODE, PWD, INIT_CWD, EDITOR,
	// COLOR and eighteen npm_config_*.
	//
	// Not a hole: none of them is one of this product's credentials, which is
	// the property under test and it holds. But Connect's doc comment says "the
	// child saw only HOME, PATH and the one allow-listed variable", and that is
	// true only of a server executed DIRECTLY. Put a launcher in between -- npx,
	// uvx, a shell wrapper, which is what a real user's config contains -- and
	// the launcher contributes its own environment on top of ours.
	//
	// The allow-list bounds what THIS DAEMON passes. It cannot bound what a
	// launcher adds, and no version of it could.
	t.Logf("interop: handed the child %d variable(s); the child reports %d bytes of environment, "+
		"carrying none of this product's credentials. The excess is the launcher's own.",
		len(env), len(res.Content))
}

func envNames(env []string) string {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
