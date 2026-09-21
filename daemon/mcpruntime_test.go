package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// THE DENY MATRIX.
//
// configPolicy is the single answer to "may this tool run?", and every path
// through it that is not an explicit, readable "allow" in the user's config
// must resolve to ask or deny. A resolver that returned allow on a path its
// author had not considered is precisely how a tool runs that nobody
// authorised, so this enumerates the paths rather than spot-checking them.
func TestConfigPolicyDefaultsToRestrictive(t *testing.T) {
	enabled := func(m MCPConfig) *Config {
		m.Enabled = true
		return &Config{MCP: m}
	}

	cases := []struct {
		name         string
		cfg          *Config
		server, tool string
		want         mcp.Policy
	}{
		{
			"a nil config denies everything",
			nil, mcp.BuiltinServerName, "read_file", mcp.PolicyDeny,
		},
		{
			// The master switch. Every allow below it is inert until it is on.
			"agent mode off denies even an explicit allow",
			&Config{MCP: MCPConfig{
				Enabled: false,
				Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
			}},
			mcp.BuiltinServerName, "read_file", mcp.PolicyDeny,
		},
		{
			"an unlisted builtin tool is ask",
			enabled(MCPConfig{}), mcp.BuiltinServerName, "read_file", mcp.PolicyAsk,
		},
		{
			"disabling the builtins denies them all, whatever the tool table says",
			enabled(MCPConfig{Builtin: MCPBuiltinConfig{
				Disabled: true,
				Tools:    map[string]string{"read_file": PolicyAllow},
			}}),
			mcp.BuiltinServerName, "read_file", mcp.PolicyDeny,
		},
		{
			"an explicit builtin allow is honoured",
			enabled(MCPConfig{Builtin: MCPBuiltinConfig{
				Tools: map[string]string{"read_file": PolicyAllow},
			}}),
			mcp.BuiltinServerName, "read_file", mcp.PolicyAllow,
		},
		{
			// The model supplies this name back to us. A server that is not
			// configured is not ours, and the shape a prompt-injection attempt
			// would take.
			"an unknown server is denied",
			enabled(MCPConfig{}), "never_configured", "anything", mcp.PolicyDeny,
		},
		{
			"a disabled server is denied even with an allow",
			enabled(MCPConfig{Servers: map[string]MCPServerConfig{
				"s": {Disabled: true, AcknowledgedUnconfined: true,
					Tools: map[string]string{"t": PolicyAllow}},
			}}),
			"s", "t", mcp.PolicyDeny,
		},
		{
			// The second lock. Validate refuses such a config outright, so this
			// is unreachable in practice -- which is exactly why it is checked:
			// "unreachable" is a property that stops holding quietly.
			"a server that never acknowledged is denied even with an allow",
			enabled(MCPConfig{Servers: map[string]MCPServerConfig{
				"s": {AcknowledgedUnconfined: false,
					Tools: map[string]string{"t": PolicyAllow}},
			}}),
			"s", "t", mcp.PolicyDeny,
		},
		{
			"an unlisted tool on a valid server is ask",
			enabled(MCPConfig{Servers: map[string]MCPServerConfig{
				"s": {AcknowledgedUnconfined: true},
			}}),
			"s", "anything", mcp.PolicyAsk,
		},
		{
			"an explicit server allow is honoured",
			enabled(MCPConfig{Servers: map[string]MCPServerConfig{
				"s": {AcknowledgedUnconfined: true, Tools: map[string]string{"t": PolicyAllow}},
			}}),
			"s", "t", mcp.PolicyAllow,
		},
		{
			// The config loader refuses invented policies, so reaching here
			// with one means something made a permission up.
			"an invented policy is denied",
			enabled(MCPConfig{Builtin: MCPBuiltinConfig{
				Tools: map[string]string{"t": "sure_go_ahead"},
			}}),
			mcp.BuiltinServerName, "t", mcp.PolicyDeny,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := configPolicy{cfg: tc.cfg}.PolicyFor(tc.server, tc.tool)
			if got != tc.want {
				t.Errorf("PolicyFor(%q, %q) = %q, want %q", tc.server, tc.tool, got, tc.want)
			}
		})
	}
}

// A Server whose built-in tools are grounded against a temp workspace.
func builtinTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	real, err := editapply.ResolveRealWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("resolving workspace: %v", err)
	}
	return &Server{
		cfg:       &Config{},
		workspace: real,
		logger:    log.New(os.Stderr, "test: ", 0),
	}
}

func call(t *testing.T, h mcp.Handler, args string) mcp.Result {
	t.Helper()
	res, err := h(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler returned a Go error rather than a readable tool error: %v", err)
	}
	return res
}

// The built-in read tool is confined by the SAME resolver that gates
// model-proposed edits. These are the vectors editapply's conformance table
// exists for -- reachable here through a different door, so a read tool cannot
// become the way out of the workspace.
func TestBuiltinReadFileIsConfined(t *testing.T) {
	s := builtinTestServer(t)
	handler := s.builtinReadFile

	if err := os.WriteFile(filepath.Join(s.workspace, "inside.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	// Something worth stealing, outside the workspace.
	outside := filepath.Join(filepath.Dir(s.workspace), "outside.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	if res := call(t, handler, `{"path":"inside.txt"}`); res.IsError || res.Content != "hello" {
		t.Fatalf("reading a file inside the workspace failed: %+v", res)
	}

	refused := []struct{ name, path string }{
		{"absolute path", outside},
		{"bare parent", ".."},
		{"parent traversal", "../outside.txt"},
		{"laundered traversal", "sub/../../outside.txt"},
		{"deep traversal", "a/b/c/../../../../outside.txt"},
		{"protected dir", ".git/config"},
		{"protected dir case-folded", ".GIT/config"},
		{"ssh dir", ".ssh/id_rsa"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, handler, `{"path":`+quote(tc.path)+`}`)
			if !res.IsError {
				t.Errorf("read_file(%q) succeeded, returning %q. A read tool must not become the "+
					"way out of the workspace", tc.path, res.Content)
			}
			if strings.Contains(res.Content, "SECRET") {
				t.Errorf("read_file(%q) leaked the out-of-workspace file's contents", tc.path)
			}
		})
	}

	t.Run("a symlink out of the workspace is refused", func(t *testing.T) {
		link := filepath.Join(s.workspace, "innocent.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		res := call(t, handler, `{"path":"innocent.txt"}`)
		if !res.IsError || strings.Contains(res.Content, "SECRET") {
			t.Errorf("a symlink pointing out of the workspace was followed: %+v", res)
		}
	})

	t.Run("malformed arguments are a readable tool error", func(t *testing.T) {
		if res := call(t, handler, `{"path": unquoted}`); !res.IsError {
			t.Error("malformed arguments were not refused")
		}
		if res := call(t, handler, `{}`); !res.IsError {
			t.Error("a missing path was not refused")
		}
	})
}

// Truncation is announced. A model given a silently clipped file reasons about
// the part it cannot see as though it were absent.
func TestBuiltinReadFileAnnouncesTruncation(t *testing.T) {
	s := builtinTestServer(t)
	big := strings.Repeat("x", maxBuiltinReadBytes+1000)
	if err := os.WriteFile(filepath.Join(s.workspace, "big.txt"), []byte(big), 0600); err != nil {
		t.Fatal(err)
	}

	res := call(t, s.builtinReadFile, `{"path":"big.txt"}`)
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if len(res.Content) > maxBuiltinReadBytes+200 {
		t.Errorf("result is %d bytes; the read cap did not apply", len(res.Content))
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Error("the result was truncated without saying so")
	}
}

// A listing that advertises .git or a secret-shaped filename has told the model
// those exist and invited it to ask for them.
func TestBuiltinListDirectoryHidesProtectedAndSecretNames(t *testing.T) {
	s := builtinTestServer(t)
	for _, dir := range []string{".git", ".ssh", "src"} {
		if err := os.Mkdir(filepath.Join(s.workspace, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.go", "id_rsa", ".env", "README.md"} {
		if err := os.WriteFile(filepath.Join(s.workspace, f), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	res := call(t, s.builtinListDirectory, `{"path":"."}`)
	if res.IsError {
		t.Fatalf("listing the workspace root failed: %+v", res)
	}

	for _, hidden := range []string{".git", ".ssh", "id_rsa", ".env"} {
		if strings.Contains(res.Content, hidden) {
			t.Errorf("the listing advertised %q; naming it tells the model it exists and invites "+
				"a request for it. Got:\n%s", hidden, res.Content)
		}
	}
	for _, shown := range []string{"main.go", "README.md", "src/"} {
		if !strings.Contains(res.Content, shown) {
			t.Errorf("the listing omitted %q; got:\n%s", shown, res.Content)
		}
	}

	t.Run("traversal is refused here too", func(t *testing.T) {
		if res := call(t, s.builtinListDirectory, `{"path":"../.."}`); !res.IsError {
			t.Errorf("listing outside the workspace succeeded: %q", res.Content)
		}
	})
}

// Every built-in must arrive confined and first-party once registered, and none
// of them may write. The second half is the property that makes agent mode safe
// even when consent is misconfigured.
func TestBuiltinsAreConfinedAndReadOnly(t *testing.T) {
	s := builtinTestServer(t)
	registry := mcp.NewRegistry(configPolicy{cfg: &Config{MCP: MCPConfig{Enabled: true}}}, 20)
	for _, b := range s.builtinTools(&proposalSink{}, "") {
		if err := registry.RegisterBuiltin(b); err != nil {
			t.Fatalf("registering %s: %v", b.Tool.Name, err)
		}
	}

	tools, errs := registry.Advertised(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(tools) == 0 {
		t.Fatal("no built-in tools were advertised")
	}

	for _, tool := range tools {
		// CONFINEMENT IS ASSERTED FOR EVERY BUILT-IN EXCEPT THE ONES THAT LEAVE
		// THE MACHINE, and the exception is checked rather than merely skipped.
		//
		// A blanket "everything is confined" here would have quietly passed
		// web_search the moment it was added -- it spawns nothing and writes
		// nothing, so it satisfies every other clause in this loop. That is the
		// same shape of miss RegisterBuiltin made about sandbox_exec: the flags
		// that describe LOCAL effects all say "harmless", and the honest answer
		// to "where does this go" is not among them.
		//
		// LaunchesSubprocess was added to this branch after the query_compiler_*
		// pair was found doing exactly that with neither flag set: they read as
		// harmless by every clause here, and the honest answer to "what does this
		// start" was not among them either.
		if tool.ReachesNetwork || tool.LaunchesSubprocess {
			if tool.Confined {
				t.Errorf("built-in %q reaches the network or starts a subprocess and still reports "+
					"itself confined; the approval prompt would tell the user this is governed by the "+
					"edit-review pipeline", tool.Name)
			}
		} else if !tool.Confined && !hostCannotConfine(t, s, tool.Name) {
			t.Errorf("built-in %q is not confined", tool.Name)
		}
		if tool.Server != mcp.BuiltinServerName {
			t.Errorf("built-in %q has server %q", tool.Name, tool.Server)
		}
		if !json.Valid(tool.Schema) {
			t.Errorf("built-in %q has an invalid schema: %s", tool.Name, tool.Schema)
		}
		// v1's built-ins are all reads, with the exception of sandbox_exec which
		// is allowed to run tests and compilations that may write build artifacts.
		if !tool.ReadOnlyHint && tool.Name != "sandbox_exec" {
			t.Errorf("built-in %q is not read-only. Lane A tools must not mutate the filesystem: "+
				"a write tool belongs in the existing edit-proposal flow, where the user sees a diff", tool.Name)
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// buildRegistry is where config becomes a running tool surface. Its failure
// modes matter more than its success: a server that will not start must cost
// the user that server's tools and nothing else.
func TestBuildRegistry(t *testing.T) {
	quiet := log.New(io.Discard, "", 0)

	t.Run("builtins disabled leaves no tools", func(t *testing.T) {
		s := builtinTestServer(t)
		s.cfg = &Config{MCP: MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Disabled: true}}}

		registry, errs := s.buildRegistry(context.Background(), quiet, &proposalSink{}, "")
		t.Cleanup(func() { _ = registry.Close() })
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if tools, _ := registry.Advertised(context.Background()); len(tools) != 0 {
			t.Errorf("advertised %d tools with the builtins disabled", len(tools))
		}
	})

	t.Run("an unstartable server degrades rather than fails", func(t *testing.T) {
		s := builtinTestServer(t)
		s.cfg = &Config{MCP: MCPConfig{
			Enabled: true,
			Servers: map[string]MCPServerConfig{
				"broken": {Command: "/definitely/not/a/binary", AcknowledgedUnconfined: true},
			},
		}}

		registry, errs := s.buildRegistry(context.Background(), quiet, &proposalSink{}, "")
		t.Cleanup(func() { _ = registry.Close() })

		if len(errs) != 1 {
			t.Fatalf("errs = %v, want one so the caller can report a DegradedMCPServer", errs)
		}
		if !errors.Is(errs[0], mcp.ErrServerUnavailable) {
			t.Errorf("error %v is not recognisable as ErrServerUnavailable", errs[0])
		}
		// The built-ins are still there. A broken server must not cost the
		// user the tools that did work.
		tools, _ := registry.Advertised(context.Background())
		if len(tools) == 0 {
			t.Error("a failed server took the built-in tools down with it")
		}
	})

	t.Run("an unacknowledged server is never spawned", func(t *testing.T) {
		s := builtinTestServer(t)
		s.cfg = &Config{MCP: MCPConfig{
			Enabled: true,
			Builtin: MCPBuiltinConfig{Disabled: true},
			Servers: map[string]MCPServerConfig{
				// Would succeed if it were ever launched. It must not be.
				"unacked": {Command: "/bin/cat", AcknowledgedUnconfined: false},
			},
		}}

		registry, errs := s.buildRegistry(context.Background(), quiet, &proposalSink{}, "")
		t.Cleanup(func() { _ = registry.Close() })
		if len(errs) != 0 {
			t.Errorf("an unacknowledged server should be skipped silently, not attempted: %v", errs)
		}
		if tools, _ := registry.Advertised(context.Background()); len(tools) != 0 {
			t.Errorf("an unacknowledged server contributed %d tool(s)", len(tools))
		}
	})
}

// Three servers' diagnostics interleaved into one unattributable stream is the
// thing this prevents.
func TestPrefixWriterAttributesEachLine(t *testing.T) {
	var buf strings.Builder
	w := &prefixWriter{prefix: "mcp/srv: ", logger: log.New(&buf, "", 0)}

	// Written in fragments that do not align with line boundaries, which is how
	// a subprocess's stderr actually arrives.
	for _, chunk := range []string{"first line\nsec", "ond line\n", "third\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}

	got := buf.String()
	for _, want := range []string{"mcp/srv: first line", "mcp/srv: second line", "mcp/srv: third"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// A partial line is held until its newline arrives, not emitted as a
	// fragment.
	if _, err := w.Write([]byte("incomplete")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "incomplete") {
		t.Error("a line without a newline was emitted early, splitting one server line across two log lines")
	}
}

// hostCannotConfine reports whether tool's unconfined flag is the HOST's answer
// rather than a defect.
//
// sandbox_exec is the one built-in whose confinement is a property of the
// machine: sandboxExecConfined is mcp.Confines(cfg), which is false when neither
// bwrap nor docker is installed -- every Windows runner, and any minimal Linux
// image. The product is already honest about it at runtime; the approval prompt
// says "NOT confined on this host: ... it runs with your full privileges."
//
// So the assertion is narrowed rather than skipped: this returns true ONLY for
// that tool and ONLY when the host genuinely cannot confine, so a sandbox_exec
// that reports itself unconfined on a machine that CAN confine still fails, and
// no other built-in is excused at all.
func hostCannotConfine(t *testing.T, s *Server, name string) bool {
	t.Helper()
	if name != "sandbox_exec" {
		return false
	}
	if s.sandboxExecConfined() {
		return false
	}
	t.Logf("sandbox_exec reports unconfined because this host has no usable bwrap or docker; " +
		"that is the answer the approval prompt gives the user too")
	return true
}

// THE EXEMPTION HAS TO BE GUARDED, OR IT BECOMES THE HOLE.
//
// hostCannotConfine narrows one assertion, and a narrowing nothing checks is
// just a slower way of deleting it. Measured: widening it to return true
// unconditionally let a sandbox_exec hard-coded to Confined:false pass
// TestBuiltinsAreConfinedAndReadOnly, and no other test noticed.
//
// So both halves of its narrowness are pinned here: the tool it names, and the
// host condition it depends on.
func TestTheConfinementExemptionIsNarrow(t *testing.T) {
	s := builtinTestServer(t)

	// A server that CANNOT confine, so the host clause does not short-circuit
	// and the NAME clause is the only thing standing between this loop and a
	// blanket exemption. Auto with no workspace root cannot confine -- see
	// TestConfinesAndLimitsApplyAreIndependent, which pins that.
	//
	// Without this, dropping the name check is invisible on any machine where
	// confinement works: the host clause returns false first and every tool
	// looks correctly un-excused. Measured exactly that way.
	unconfinable := &Server{logger: discardLogger(), workspace: ""}
	if unconfinable.sandboxExecConfined() {
		t.Fatal("the fixture server can confine after all, so the loop below proves nothing")
	}
	for _, name := range []string{"read_file", "search_code", "propose_edit", "repo_map", ""} {
		if hostCannotConfine(t, unconfinable, name) {
			t.Errorf("the confinement exemption excused %q; it exists for sandbox_exec alone, "+
				"whose confinement is a fact about the HOST rather than about the tool", name)
		}
	}

	// And on a host that CAN confine, it must not excuse sandbox_exec either --
	// otherwise a genuinely unconfined sandbox_exec passes unnoticed.
	if s.sandboxExecConfined() && hostCannotConfine(t, s, "sandbox_exec") {
		t.Error("the exemption fired for sandbox_exec on a host that DOES confine, so a real " +
			"regression in its confinement would no longer fail any test")
	}
}
