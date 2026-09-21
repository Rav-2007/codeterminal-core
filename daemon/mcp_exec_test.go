package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// P0, found 2026-08-05 and reproduced before it was fixed.
//
// sandbox_exec was named for a sandbox it never used. It called
// exec.CommandContext directly, with cmd.Env left nil — which inherits the
// daemon's ENTIRE environment, where OPENROUTER_API_KEY and
// MOCHIII_PROXY_KEY live. Its "strict whitelist" of go/npm/make/cargo
// was described in its own source as preventing "RCE sandbox escape", but every
// entry on it runs project-supplied shell by design: a Makefile recipe IS
// shell.
//
// So one approved call against a repo with a hostile Makefile sent the user's
// inference credentials wherever that Makefile chose. mcp.ForbiddenEnvNames
// exists precisely to stop this for Lane B subprocesses; the daemon's own tool
// bypassed it.
//
// These tests drive the real handler, not a copy of its logic, because the
// defect was in how the command was CONSTRUCTED — a test that built its own
// exec.Cmd would have passed against the broken code.

// execToolFixture stands up a workspace whose Makefile prints whatever
// credentials it can see, which is exactly what an exfiltrating target would do
// before sending them somewhere.
func execToolFixture(t *testing.T) *Server {
	t.Helper()

	ws := t.TempDir()
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	makefile := "leak:\n\t@echo \"OR=$$OPENROUTER_API_KEY MOCHI=$$MOCHIII_PROXY_KEY CT=$$MOCHIII_API_KEY\"\n"
	if err := os.WriteFile(filepath.Join(real, "Makefile"), []byte(makefile), 0600); err != nil {
		t.Fatal(err)
	}

	// The daemon holds these for the inference path. t.Setenv restores them.
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-CANARY-must-not-escape")
	t.Setenv("MOCHIII_PROXY_KEY", "mochi_CANARY-must-not-escape")
	t.Setenv("MOCHIII_API_KEY", "sk-CANARY-must-not-escape")

	return &Server{logger: discardLogger(), workspace: real}
}

func runExecTool(t *testing.T, s *Server, command string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.builtinSandboxExec(context.Background(), raw)
	if err != nil {
		t.Fatalf("builtinSandboxExec returned a transport error: %v", err)
	}
	return res.Content
}

// THE ONE THAT MATTERS. A whitelisted binary running a project-supplied recipe
// must not be able to see this daemon's credentials.
// requireWorkingSandbox skips when no sandbox backend on this host can actually
// execute a command.
//
// Ubuntu 24.04+ ships kernel.apparmor_restrict_unprivileged_userns=1, under
// which bwrap is installed and every invocation dies with "setting up uid map:
// Permission denied". sandbox_exec correctly FAILS CLOSED there -- it does not
// silently run the command unconfined -- so the tests below that need the
// command to actually run have an unmet premise, not a defect to report.
//
// NOT RUN, loudly, rather than a green tick: the confinement these tests cover
// is unverified on such a host, and pretending otherwise is what this whole
// campaign exists to stop.
func requireWorkingSandbox(t *testing.T) {
	t.Helper()
	if mcp.BwrapUsable() {
		return
	}
	// NOT exec.LookPath("docker"), which is what this said first and is the
	// same presence-is-not-capability mistake a third time. A docker with no
	// image configured cannot run anything, and sandbox_exec configures none --
	// so on a host without a usable bwrap there is no working backend, whatever
	// docker binaries are lying around. GitHub's ubuntu-latest runners are
	// exactly that host: bubblewrap installs and then fails with "setting up
	// uid map: Permission denied", and docker is present but imageless.
	if mcp.DockerUsable(mcp.SandboxConfig{Image: sandboxExecImage()}) {
		return
	}
	t.Skip("NOT RUN: no sandbox backend on this host can execute (bwrap cannot create a user " +
		"namespace and no docker image is configured); sandbox_exec confinement is UNVERIFIED here")
}

func TestSandboxExec_DoesNotLeakInferenceCredentials(t *testing.T) {
	requireWorkingSandbox(t)
	if _, err := os.Stat("/usr/bin/make"); err != nil {
		if _, err2 := os.Stat("/bin/make"); err2 != nil {
			t.Skip("make is not installed; this test needs a real whitelisted binary")
		}
	}
	s := execToolFixture(t)

	out := runExecTool(t, s, "make leak")

	if strings.Contains(out, "CANARY") {
		t.Errorf("sandbox_exec handed this daemon's credentials to a Makefile recipe.\n"+
			"A single approved call against a repo with a hostile Makefile exfiltrates the "+
			"user's OpenRouter key.\noutput was: %s", out)
	}
	// The command must still WORK — a scrub that broke the tool would be
	// reverted by whoever hit it next.
	if !strings.Contains(out, "OR=") {
		t.Errorf("the recipe did not run at all; the environment scrub must not break the tool.\noutput was: %s", out)
	}
}

// PATH and HOME must survive, or every one of go/npm/make/cargo fails to
// resolve its own toolchain and the scrub gets reverted as "broken".
func TestSandboxExec_KeepsTheEnvironmentACompilerNeeds(t *testing.T) {
	requireWorkingSandbox(t)
	s := execToolFixture(t)

	raw, _ := json.Marshal(map[string]string{"command": "go env GOPATH"})
	res, err := s.builtinSandboxExec(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if strings.Contains(res.Content, "executable file not found") {
		t.Errorf("the scrub removed PATH, so the toolchain is unreachable: %s", res.Content)
	}
}

// The allow-list is a first filter, and it must still refuse the obvious things
// even though it is explicitly NOT the security boundary.
func TestSandboxExec_RefusesBinariesOffTheList(t *testing.T) {
	s := execToolFixture(t)

	for _, cmd := range []string{
		"sh -c 'echo hi'",
		"bash -c id",
		"curl https://example.com",
		"/bin/sh",
		"python3 -c 'print(1)'",
	} {
		out := runExecTool(t, s, cmd)
		if !strings.Contains(out, "not one of go, npm, make or cargo") {
			t.Errorf("sandbox_exec(%q) was not refused; got %q", cmd, out)
		}
	}
}

func TestSandboxExec_RejectsEmptyAndMalformedInput(t *testing.T) {
	s := execToolFixture(t)

	if out := runExecTool(t, s, "   "); !strings.Contains(out, "cannot be empty") {
		t.Errorf("an all-whitespace command was not refused: %q", out)
	}
	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":`))
	if err != nil {
		t.Fatalf("malformed JSON should be a tool error, not a transport error: %v", err)
	}
	if !strings.Contains(res.Content, "not a valid JSON object") {
		t.Errorf("malformed JSON was not reported: %q", res.Content)
	}
}

// The command runs in the workspace, not in whatever directory the daemon
// happens to have been started from.
func TestSandboxExec_RunsInTheWorkspace(t *testing.T) {
	requireWorkingSandbox(t)
	s := execToolFixture(t)
	out := runExecTool(t, s, "make leak")
	if strings.Contains(out, "No rule to make target") || strings.Contains(out, "No targets") {
		t.Errorf("the Makefile in the workspace was not found; cmd.Dir is wrong: %s", out)
	}
}
