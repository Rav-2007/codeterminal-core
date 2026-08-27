package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func stubLimiter(t *testing.T, usable bool) {
	t.Helper()
	prev := LimiterUsable
	LimiterUsable = func() bool { return usable }
	t.Cleanup(func() { LimiterUsable = prev })
}

func joined(args []string) string { return strings.Join(args, " ") }

// A config that asks for no bounds must come back byte-identical. Lane B passes
// a zero-value SandboxConfig, so this is what keeps third-party servers running
// exactly as they did before limits existed.
func TestACommandThatAsksForNoLimitsIsNotWrapped(t *testing.T) {
	stubLimiter(t, true)
	bin, args, err := WrapCommand("node", []string{"server.js"}, SandboxConfig{Mode: SandboxNone})
	if err != nil {
		t.Fatal(err)
	}
	if bin != "node" || joined(args) != "server.js" {
		t.Errorf("an unlimited command was rewritten to %s %v", bin, args)
	}
	if wantsLimits(SandboxConfig{}) {
		t.Error("a zero config must not be read as asking for limits")
	}
}

// DEGRADES IN ONE DIRECTION. A host with no user systemd is not a host where a
// build must stop working; it is one where the command is less contained, and
// the honest response is to run it and say so.
func TestAnUnavailableLimiterRunsTheCommandUnbounded(t *testing.T) {
	cfg := SandboxConfig{Mode: SandboxNone, MemoryLimitMB: 512, PidsLimit: 64}
	stubLimiter(t, false)
	bin, _, err := WrapCommand("make", []string{"check"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "make" {
		t.Errorf("bin = %q, want the command run directly when no limiter exists", bin)
	}
	if LimitsApply(cfg) {
		t.Error("LimitsApply must be false when the limiter cannot run, or the prompt promises a cap there is none of")
	}

	stubLimiter(t, true)
	if !LimitsApply(cfg) {
		t.Error("LimitsApply must be true when the limiter works and bounds were asked for")
	}
}

// THE SCOPE MUST CONTAIN THE SANDBOX, not sit inside it: a fork bomb inside the
// namespace is outside the accounting otherwise.
func TestTheScopeWrapsTheSandboxAndCarriesEveryBound(t *testing.T) {
	stubLimiter(t, true)
	ws := t.TempDir()
	cfg := SandboxConfig{
		Mode: SandboxBubblewrap, WorkspaceRoot: ws,
		MemoryLimitMB: 2048, PidsLimit: 512, CPULimit: 1.5,
	}
	if !BwrapUsable() {
		t.Skip("no usable bwrap on this host")
	}
	bin, args, err := WrapCommand("go", []string{"build", "./..."}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "systemd-run" {
		t.Fatalf("bin = %q, want the scope on the outside", bin)
	}
	all := joined(args)
	for _, want := range []string{"MemoryMax=2048M", "TasksMax=512", "CPUQuota=150%"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing bound %q in: %s", want, all)
		}
	}
	if strings.Index(all, "bwrap") < strings.Index(all, "MemoryMax") {
		t.Error("bwrap appears before the limits; the scope must wrap the sandbox, not the reverse")
	}
	for _, name := range limiterRuntimeEnvNames {
		if !strings.Contains(all, "-u "+name) {
			t.Errorf("%s is not stripped before the payload: %s", name, all)
		}
	}
}

// An omitted bound must not be sent as a zero, which systemd would read as
// "allow nothing" rather than "no limit".
func TestAnOmittedBoundIsAbsentRatherThanZero(t *testing.T) {
	all := joined(limiterPrefix(SandboxConfig{MemoryLimitMB: 256}))
	if strings.Contains(all, "TasksMax") || strings.Contains(all, "CPUQuota") {
		t.Errorf("an unset bound was still sent: %s", all)
	}
	if !strings.Contains(all, "MemoryMax=256M") {
		t.Errorf("the bound that WAS set is missing: %s", all)
	}
}

// LimiterEnv adds exactly what systemd-run needs and ServerEnv keeps not
// carrying it -- the split is what stops the bus reaching a command that has no
// limiter prefix to strip it again.
func TestOnlyLimiterEnvCarriesTheBus(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/test")
	has := func(env []string, name string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				return true
			}
		}
		return false
	}
	if has(ServerEnv(nil), "XDG_RUNTIME_DIR") {
		t.Error("ServerEnv leaks the session bus to every subprocess")
	}
	if !has(LimiterEnv(nil), "XDG_RUNTIME_DIR") {
		t.Error("LimiterEnv omits what systemd-run needs, so the limiter would silently never run")
	}
	// A credential must not become grantable just because it went through the
	// limiter's allow-list.
	for _, name := range ForbiddenEnvNames {
		if has(LimiterEnv([]string{name}), name) {
			t.Errorf("LimiterEnv passed the forbidden %q", name)
		}
	}
}

// HOME is bound at the same path inside as out, exactly as the workspace is, so
// one HOME assignment is correct in every backend.
func TestTheSandboxHomeIsBoundAtTheSamePathInsideAsOut(t *testing.T) {
	if !BwrapUsable() {
		t.Skip("no usable bwrap on this host")
	}
	stubLimiter(t, false)
	ws, home := t.TempDir(), t.TempDir()
	_, args, err := WrapCommand("go", []string{"test"}, SandboxConfig{
		Mode: SandboxBubblewrap, WorkspaceRoot: ws, HomeDir: home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined(args), "--bind "+filepath.Clean(home)+" "+filepath.Clean(home)) {
		t.Errorf("HOME is not bound identity-wise: %s", joined(args))
	}

	// And absent when unset, so the pre-existing tmpfs behaviour is unchanged.
	_, args, err = WrapCommand("go", []string{"test"}, SandboxConfig{
		Mode: SandboxBubblewrap, WorkspaceRoot: ws,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined(args), home) {
		t.Error("a config with no HomeDir still bound one")
	}
}

// The docker backend has its own flags and needs no scope, so it reports bounded
// without a limiter -- and it must carry the pids limit it was always missing.
func TestDockerIsBoundedByItsOwnFlags(t *testing.T) {
	stubLimiter(t, false)
	cfg := SandboxConfig{
		Mode: SandboxDocker, WorkspaceRoot: t.TempDir(), Image: "golang:1.23",
		MemoryLimitMB: 1024, PidsLimit: 256, CPULimit: 2,
	}
	if !LimitsApply(cfg) {
		t.Error("docker sets its own bounds; LimitsApply must say so even with no systemd scope")
	}
	if _, err := os.Stat("/usr/bin/docker"); err != nil {
		t.Skip("no docker binary to build a command with")
	}
	bin, args, err := WrapCommand("go", []string{"build"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "docker" {
		t.Fatalf("bin = %q", bin)
	}
	for _, want := range []string{"--memory=1024m", "--pids-limit=256", "--cpus=2.00"} {
		if !strings.Contains(joined(args), want) {
			t.Errorf("missing %q in: %s", want, joined(args))
		}
	}
}

// CONFINEMENT AND BOUNDEDNESS ARE SEPARATE PROPERTIES, and a prompt that
// conflates them lies in one direction or the other. A command can be inside a
// namespace and still able to exhaust the host; it can be bounded by a scope
// and completely unconfined.
func TestConfinesAndLimitsApplyAreIndependent(t *testing.T) {
	ws := t.TempDir()
	stubLimiter(t, true)

	bounded := SandboxConfig{Mode: SandboxNone, WorkspaceRoot: ws, MemoryLimitMB: 512}
	if Confines(bounded) {
		t.Error("SandboxNone is host execution; calling it confined obtains consent for the wrong thing")
	}
	if !LimitsApply(bounded) {
		t.Error("unconfined but bounded is a real state, and strictly better than unconfined and unbounded")
	}

	if BwrapUsable() {
		stubLimiter(t, false)
		confined := SandboxConfig{Mode: SandboxBubblewrap, WorkspaceRoot: ws, MemoryLimitMB: 512}
		if !Confines(confined) {
			t.Error("an explicit bubblewrap mode confines")
		}
		if LimitsApply(confined) {
			t.Error("bwrap has no memory, cpu or pids flag; confined must not imply bounded")
		}
	}

	// An empty workspace has nothing to confine TO, so auto resolves to none.
	if Confines(SandboxConfig{Mode: SandboxAuto}) {
		t.Error("auto with no workspace root cannot confine")
	}
}

// Every refusal names what to do about it. These are the paths a user reaches
// by misconfiguring, and "unknown sandbox mode" with no further detail is how a
// support question becomes an afternoon.
func TestEveryUnrunnableSandboxRefusesWithAReason(t *testing.T) {
	stubLimiter(t, false)
	_, dockerErr := lookPath("docker")
	for _, tc := range []struct {
		name        string
		cfg         SandboxConfig
		want        string
		needsDocker bool
	}{
		// The binary check runs first by design -- "docker is not installed" is
		// a more useful answer than "no image" on a host with no docker -- so
		// these two only reach the branch they are about where docker exists.
		{"docker with no image", SandboxConfig{Mode: SandboxDocker, WorkspaceRoot: t.TempDir()}, "requires an Image", true},
		{"docker with no workspace", SandboxConfig{Mode: SandboxDocker, Image: "x"}, "non-empty WorkspaceRoot", true},
		{"docker absent entirely", SandboxConfig{Mode: SandboxDocker}, "not installed on PATH", false},
		{"bwrap with no workspace", SandboxConfig{Mode: SandboxBubblewrap}, "WorkspaceRoot", false},
		{"a mode that does not exist", SandboxConfig{Mode: SandboxMode("chroot")}, "unknown sandbox mode", false},
	} {
		if tc.needsDocker && dockerErr != nil {
			continue
		}
		if tc.name == "docker absent entirely" && dockerErr == nil {
			continue
		}
		_, _, err := WrapCommand("go", []string{"build"}, tc.cfg)
		if err == nil {
			t.Errorf("%s: ran anyway", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q does not say %q", tc.name, err, tc.want)
		}
	}
}

// THE PROBE ITSELF MUST RUN AT LEAST ONCE IN THE SUITE.
//
// Every other test here stubs LimiterUsable, which is right for testing the
// decisions built on it and leaves the probe -- the part that actually talks to
// systemd, under the scrubbed environment, and is the whole reason this is not
// a lookPath call -- executed by nothing. A capability probe that is never
// exercised is how "presence is not capability" gets made a fourth time.
//
// It asserts agreement with an independently-built invocation rather than a
// fixed value, because the right answer genuinely differs by host: a CI
// container with no user systemd must report false and still pass.
func TestTheLimiterProbeAgreesWithAnActualScope(t *testing.T) {
	got := LimiterUsable()

	independent := false
	if _, err := lookPath("systemd-run"); err == nil {
		if truePath, err := lookPath("true"); err == nil {
			cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
				"-p", "MemoryMax=64M", "-p", "TasksMax=16", "-p", "CPUQuota=100%", "--", truePath)
			cmd.Env = LimiterEnv(nil)
			independent = cmd.Run() == nil
		}
	}

	if got != independent {
		t.Errorf("LimiterUsable()=%v but a real scope %s here -- the probe and the thing it "+
			"predicts disagree, which is the failure mode it exists to prevent",
			got, map[bool]string{true: "succeeded", false: "failed"}[independent])
	}
	t.Logf("limiter usable on this host: %v", got)
}

// LimitsApply must short-circuit before consulting the host when nothing was
// asked for: a Lane B server with a zero config must not pay for a probe, and
// must never be reported as bounded.
func TestLimitsApplyIsFalseWhenNothingWasAskedFor(t *testing.T) {
	stubLimiter(t, true)
	if LimitsApply(SandboxConfig{Mode: SandboxNone}) {
		t.Error("a config asking for no bounds was reported as bounded")
	}
	if LimitsApply(SandboxConfig{Mode: SandboxDocker, Image: "x", WorkspaceRoot: t.TempDir()}) {
		t.Error("docker with no bounds requested is not bounded either")
	}
}

// THE TOOLCHAIN MUST BE REACHABLE FROM INSIDE THE NAMESPACE.
//
// Every command sandbox_exec accepts except make is normally installed
// per-user -- go from a tarball into ~/.local/go, npm under ~/.nvm, cargo under
// ~/.cargo -- and none of those paths was bound. The sandbox could run exactly
// the one of the four that needed it least. Measured before the fix, on a host
// with go at ~/.local/go/bin/go: "bwrap: execvp go: No such file or directory".
func TestAToolchainOutsideTheSystemDirectoriesIsBoundIn(t *testing.T) {
	if !BwrapUsable() {
		t.Skip("no usable bwrap on this host")
	}
	stubLimiter(t, false)

	// A binary in a <root>/bin layout, which is what all three installers use.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(bin, "faketool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, args, err := WrapCommand("faketool", nil, SandboxConfig{
		Mode: SandboxBubblewrap, WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The ROOT, not the bin directory: GOROOT's lib and pkg sit beside bin, and
	// binding only bin gives a toolchain that starts and then cannot find its
	// own standard library.
	if !strings.Contains(joined(args), "--ro-bind "+root+" "+root) {
		t.Errorf("the toolchain root is not bound, so the command cannot be executed at all:\n%s", joined(args))
	}
	if strings.Contains(joined(args), "--bind "+root) {
		t.Error("the toolchain is bound writable; it only needs to be read")
	}
}

// A toolchain already inside a system mount must not be bound twice.
func TestASystemToolchainIsNotBoundRedundantly(t *testing.T) {
	if _, err := os.Stat("/usr/bin/make"); err != nil {
		t.Skip("no /usr/bin/make on this host")
	}
	if root := toolchainRoot("make"); !coveredBy(root, []string{"/usr"}) {
		t.Errorf("toolchainRoot(make) = %q, which is not covered by /usr", root)
	}
	if coveredBy("/home/user/.local/go", []string{"/usr", "/bin"}) {
		t.Error("a per-user toolchain must not be treated as already covered")
	}
}
