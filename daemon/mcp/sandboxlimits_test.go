package mcp

import (
	"fmt"
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

// A MEMORY BOUND WITHOUT A SWAP BOUND IS NOT A MEMORY BOUND.
//
// MemoryMax is cgroup v2 memory.max, which caps RESIDENT memory and lets the
// cgroup push everything else to swap. So on any host with swap, a "2048 MiB
// cap" is really 2048 MiB plus the whole swap device, and the number quoted in
// the approval prompt is not the number enforced.
//
// MEASURED: a 4 GiB allocation ran to completion under this exact cap on a CI
// runner with ~4 GiB of swap, and was killed on a workstation only because that
// machine has SwapTotal=0 -- which is what made memory.max look like the hard
// bound it is not. The end-to-end test could therefore never catch this on a
// swapless box, which is why the pairing is asserted here instead.
func TestAMemoryBoundAlwaysBindsSwapToo(t *testing.T) {
	stubLimiter(t, true)
	_, args, err := WrapCommand("go", []string{"build"}, SandboxConfig{
		Mode: SandboxNone, MemoryLimitMB: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	all := joined(args)
	if !strings.Contains(all, "MemoryMax=512M") {
		t.Fatalf("the memory bound is missing entirely: %s", all)
	}
	if !strings.Contains(all, "MemorySwapMax=0") {
		t.Errorf("MemoryMax was set without MemorySwapMax=0, so the cap leaks through swap "+
			"on every host that has any: %s", all)
	}

	// The converse: asking for no memory bound must not silently bind swap,
	// which would be a limit nobody requested.
	_, none, err := WrapCommand("go", []string{"build"}, SandboxConfig{
		Mode: SandboxNone, PidsLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined(none), "MemorySwapMax") {
		t.Errorf("a config asking only for a pids bound came back with a swap bound: %s", joined(none))
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
	for _, want := range []string{"MemoryMax=2048M", "MemorySwapMax=0", "TasksMax=512", "CPUQuota=150%"} {
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
	_, bwrapErr := lookPath("bwrap")
	for _, tc := range []struct {
		name        string
		cfg         SandboxConfig
		want        string
		needsDocker bool
		needsBwrap  bool
	}{
		// The binary check runs first by design -- "docker is not installed" is
		// a more useful answer than "no image" on a host with no docker -- so
		// these two only reach the branch they are about where docker exists.
		{"docker with no image", SandboxConfig{Mode: SandboxDocker, WorkspaceRoot: t.TempDir()}, "requires an Image", true, false},
		{"docker with no workspace", SandboxConfig{Mode: SandboxDocker, Image: "x"}, "non-empty WorkspaceRoot", true, false},
		{"docker absent entirely", SandboxConfig{Mode: SandboxDocker}, "not installed on PATH", false, false},
		// needsBwrap for the same reason the docker rows carry needsDocker, and
		// its absence is why this test could never pass on Windows: the binary
		// check runs first by design, so with no bwrap installed the error says
		// "not installed on PATH" and never reaches the WorkspaceRoot branch
		// this row is about.
		{"bwrap with no workspace", SandboxConfig{Mode: SandboxBubblewrap}, "WorkspaceRoot", false, true},
		{"a mode that does not exist", SandboxConfig{Mode: SandboxMode("chroot")}, "unknown sandbox mode", false, false},
	} {
		if tc.needsDocker && dockerErr != nil {
			continue
		}
		if tc.needsBwrap && bwrapErr != nil {
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
// delegatedControllers reads which cgroup v2 controllers this user's systemd
// manager may actually apply. It is the INDEPENDENT oracle for the test below:
// systemd-run will happily accept -p MemoryMax= without this list containing
// "memory", and then enforce nothing.
func delegatedControllers() ([]string, bool) {
	uid := os.Getuid()
	path := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/cgroup.controllers", uid, uid)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return strings.Fields(string(b)), true
}

// THE PROBE MUST AGREE WITH THE KERNEL, NOT WITH systemd-run's EXIT CODE.
//
// This test used to compare LimiterUsable() against whether a scope could be
// CREATED, and passed on every host anyone had run it on. That equivalence was
// the bug: on a runner whose user manager has no delegated memory controller,
// the scope is created, systemd-run exits zero, MemoryMax is silently discarded,
// and a 4 GiB allocation runs to completion under a "2048 MiB cap" -- while the
// approval prompt tells the user the command is bounded.
//
// So the oracle is now what the kernel will actually enforce, and the assertion
// is two-sided, because the two directions catch opposite defects:
//
//	memory delegated, probe false  -> the probe is broken, and its breakage is
//	                                  SILENT: every limits test skips, CI goes
//	                                  green, and nothing is ever bounded again.
//	memory undelegated, probe true -> the original bug: a promise we cannot keep.
func TestTheLimiterProbeAgreesWithWhatTheKernelEnforces(t *testing.T) {
	controllers, ok := delegatedControllers()
	if !ok {
		t.Skip("no systemd user manager cgroup on this host; nothing to compare the probe against")
	}
	memoryDelegated := false
	for _, c := range controllers {
		if c == "memory" {
			memoryDelegated = true
			break
		}
	}
	// A delegated memory controller is not enough: the bound this product
	// promises includes swap, so a kernel with no swap accounting cannot honour
	// it and the probe is right to say so. See memoryLimitsAreEnforced.
	uid := os.Getuid()
	swapFile := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/memory.swap.max", uid, uid)
	if _, err := os.Stat(swapFile); err != nil {
		memoryDelegated = false
	}

	got := LimiterUsable()

	// What the OLD probe measured, kept only as evidence in the failure message.
	scopeStarts := false
	if _, err := lookPath("systemd-run"); err == nil {
		if truePath, err := lookPath("true"); err == nil {
			cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
				"-p", "MemoryMax=64M", "-p", "TasksMax=16", "-p", "CPUQuota=100%", "--", truePath)
			cmd.Env = LimiterEnv(nil)
			scopeStarts = cmd.Run() == nil
		}
	}

	switch {
	case memoryDelegated && !got:
		t.Errorf("this host delegates %v and has swap accounting, so the bound binds here, but LimiterUsable() is false.\n\n"+
			"That is the SILENT direction: every test gated on LimiterUsable now skips, the suite "+
			"goes green, and sandbox_exec stops bounding anything. Check the read-back probe -- a "+
			"missing sh or cat, or a changed /proc/self/cgroup format, looks exactly like this.",
			controllers)
	case !memoryDelegated && got:
		t.Errorf("this host does NOT delegate a memory controller (%v), so MemoryMax cannot bind, "+
			"but LimiterUsable() is true.\n\n"+
			"A scope still starts here (%v) -- that is precisely what the old probe measured and "+
			"why it was wrong. LimitsApply will now tell the user their command is capped while it "+
			"is not.", controllers, scopeStarts)
	}
	t.Logf("delegated controllers=%v limiterUsable=%v scopeStarts=%v", controllers, got, scopeStarts)
}

// The read-back decision, in isolation. "max" is the exact string an
// undelegated memory controller returns for memory.max, and a non-zero
// memory.swap.max is how a cap that looks right lets a process exceed it by
// the size of swap -- the defect that survived the first version of this probe.
func TestMemoryLimitsAreEnforced(t *testing.T) {
	const want = 64 * 1024 * 1024
	for _, tc := range []struct {
		readBack string
		enforced bool
		why      string
	}{
		{"67108864\n0\n", true, "exactly what was asked for, swap bound to zero"},
		{"67108864 0", true, "whitespace-separated rather than newline"},
		{"33554432\n0\n", true, "a tighter bound than asked for is still a bound"},
		{"67108864\nmax\n", false, "THE CI READING: capped memory, UNBOUNDED SWAP"},
		{"67108864\n1048576\n", false, "a little swap is still more than the number we quoted"},
		{"max\n0\n", false, "unbounded memory, whatever swap says"},
		{"max\nmax\n", false, "unbounded both ways"},
		{"67108864\n", false, "only one file read back -- swap could not be checked"},
		{"", false, "no output at all: cat failed, or a file was missing"},
		{"134217728\n0\n", false, "looser than asked for: something else set this, so we did not"},
		{"0\n0\n", false, "zero is not a bound this code ever asks for"},
		{"-1\n0\n", false, "negative"},
		{"6710886four\n0\n", false, "not a number"},
		{"67108864\n0\n0\n", false, "three values, so the read-back is not what we think it is"},
	} {
		if got := memoryLimitsAreEnforced(tc.readBack, want); got != tc.enforced {
			t.Errorf("memoryLimitsAreEnforced(%q) = %v, want %v -- %s", tc.readBack, got, tc.enforced, tc.why)
		}
	}
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
