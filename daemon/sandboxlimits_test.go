package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// THE CLAIM AND THE BEHAVIOUR MUST BE ONE COMPUTATION -- F-01's lesson, applied
// to resource limits before they became the same finding a second time.
func TestTheDescriptionDoesNotPromiseLimitsTheHostCannotApply(t *testing.T) {
	s := builtinTestServer(t)

	// FORCED, NOT OBSERVED. The first version of this test read the host's own
	// answer and asserted the description agreed with it -- which on a machine
	// with a working systemd user scope means both the real code and a version
	// that hardcodes "Capped at ..." pass, because both say the same thing.
	// A test that cannot fail on the developer's machine is not a test. Driving
	// LimiterUsable to each value in turn is what makes the claim-follows-reality
	// property actually checkable here.
	restore := mcp.LimiterUsable
	t.Cleanup(func() { mcp.LimiterUsable = restore })

	for _, usable := range []bool{true, false} {
		mcp.LimiterUsable = func() bool { return usable }
		desc := s.sandboxExecDescription()
		cfg := s.sandboxExecConfig()

		promisesLimits := strings.Contains(desc, "Capped at")
		if promisesLimits != mcp.LimitsApply(cfg) {
			t.Errorf("limiter usable=%v: the description %s a cap but LimitsApply says %v\n%s",
				usable, map[bool]string{true: "promises", false: "does not promise"}[promisesLimits],
				mcp.LimitsApply(cfg), desc)
		}
		if !mcp.LimitsApply(cfg) && !strings.Contains(desc, "NO memory or process limit") {
			t.Errorf("limiter usable=%v: a host with no limiter must SAY it has none, not stay silent:\n%s",
				usable, desc)
		}
		promisesConfinement := strings.Contains(desc, "Confined to this workspace")
		if promisesConfinement != mcp.Confines(cfg) {
			t.Errorf("limiter usable=%v: confinement claim %v vs Confines %v:\n%s",
				usable, promisesConfinement, mcp.Confines(cfg), desc)
		}
		t.Logf("limiter usable=%v ->\n  %s", usable, desc)
	}
}

// THE SESSION BUS MUST NOT SURVIVE INTO THE COMMAND. A payload that can reach
// the user manager can ask it to start a unit outside the sandbox, unbounded,
// as the user -- which would make the limiter a way out rather than a way in.
func TestTheLimiterStripsTheBusItNeededToGetIn(t *testing.T) {
	cfg := mcp.SandboxConfig{Mode: mcp.SandboxNone, MemoryLimitMB: 512, PidsLimit: 64}
	if !mcp.LimiterUsable() {
		t.Skip("no usable systemd user scope on this host")
	}
	bin, args, err := mcp.WrapCommand("make", []string{"check"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "systemd-run" {
		t.Fatalf("a config asking for limits was not wrapped: %s", bin)
	}
	joined := strings.Join(args, " ")
	for _, name := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if !strings.Contains(joined, "-u "+name) {
			t.Errorf("%s is not stripped before the payload: %s", name, joined)
		}
	}
	// The limiter must contain the SANDBOX, not sit inside it: a fork bomb
	// inside the namespace is outside the accounting otherwise.
	if idx := strings.Index(joined, "make"); idx < strings.Index(joined, "MemoryMax") {
		t.Error("the command appears before the limits; the scope must wrap the whole thing")
	}
	t.Logf("wrapped: %s %s", bin, joined)
}

// THE ENVIRONMENT THE PROBE RUNS UNDER MUST BE THE ONE THE COMMAND GETS.
// Measured: systemd-run under ServerEnv fails with "Failed to connect to bus:
// No medium found", so a probe run with the ambient environment would report a
// limiter that never works.
func TestLimiterEnvCarriesTheBusAndServerEnvDoesNot(t *testing.T) {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Skip("no XDG_RUNTIME_DIR in this environment to distinguish the two")
	}
	has := func(env []string, name string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				return true
			}
		}
		return false
	}
	if has(mcp.ServerEnv(nil), "XDG_RUNTIME_DIR") {
		t.Error("ServerEnv leaks the session bus to every subprocess")
	}
	if !has(mcp.LimiterEnv(nil), "XDG_RUNTIME_DIR") {
		t.Error("LimiterEnv omits what systemd-run needs; the limiter would silently never run")
	}
}

// END TO END, through the real handler: a command that tries to allocate far
// past the cap must die rather than succeed.
func TestARunawayCommandIsKilledByTheCap(t *testing.T) {
	if !mcp.LimiterUsable() {
		t.Skip("no usable systemd user scope on this host")
	}
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("no python3 to allocate with")
	}
	s := builtinTestServer(t)
	// A make recipe is the realistic shape: sandbox_exec only runs go/npm/make/cargo,
	// and a Makefile recipe is exactly the project-supplied script the tool's
	// own description warns approving a call approves.
	hogPy := "b = bytearray()\nfor _ in range(4096):\n    b.extend(bytearray(1024 * 1024))\nprint('ALLOCATED 4G')\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "hog.py"), []byte(hogPy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"),
		[]byte("hog:\n\t@python3 hog.py\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make hog"}`))
	if err != nil {
		t.Fatalf("handler returned a Go error: %v", err)
	}
	if strings.Contains(res.Content, "ALLOCATED 4G") {
		t.Errorf("a 4 GiB allocation completed under a %d MiB cap:\n%s", execMemoryLimitMB, res.Content)
	}
	t.Logf("result: %s", strings.TrimSpace(res.Content))
}

// makeRecipe writes a Makefile whose default target runs sh -c body, and runs
// it through the real handler. This is the only shape sandbox_exec accepts, and
// it is also the realistic threat: a project-supplied recipe.
func makeRecipe(t *testing.T, s *Server, body string) string {
	t.Helper()
	// make(1) expands $ in a recipe before the shell ever sees it, so "$HOME"
	// arrives as "OME". Doubling it is how a Makefile passes a dollar through.
	if err := os.WriteFile(filepath.Join(s.workspace, "Makefile"),
		[]byte("probe:\n\t@"+strings.ReplaceAll(body, "$", "$$")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"make probe"}`))
	if err != nil {
		t.Fatalf("handler returned a Go error: %v", err)
	}
	return res.Content
}

// THE ARGV TEST IS A PROXY; THIS IS THE PROPERTY. It asks the command itself
// what it can see, so a strip that is present in the arguments and ineffective
// in practice still fails here.
func TestTheCommandCannotSeeTheSessionBus(t *testing.T) {
	if !mcp.LimiterUsable() {
		t.Skip("no usable systemd user scope on this host")
	}
	s := builtinTestServer(t)
	out := makeRecipe(t, s, `echo "BUS=[${XDG_RUNTIME_DIR:-STRIPPED}] DBUS=[${DBUS_SESSION_BUS_ADDRESS:-STRIPPED}]"`)
	if !strings.Contains(out, "BUS=[STRIPPED]") || !strings.Contains(out, "DBUS=[STRIPPED]") {
		t.Errorf("the command can reach the session bus, so it can ask systemd to run "+
			"something outside its own sandbox:\n%s", out)
	}
	t.Logf("%s", strings.TrimSpace(out))
}

// A PERSISTENT HOME IS WHAT KEEPS A COLD CACHE OFF THE MEMORY BUDGET. Without
// it HOME is bwrap's internal tmpfs: writable, in RAM, and gone at exit -- so
// every run re-downloads its module cache into the pages the memory cap is
// enforcing.
func TestTheSandboxHomeSurvivesBetweenRuns(t *testing.T) {
	s := builtinTestServer(t)
	home := s.sandboxExecHome()
	if home == "" {
		t.Skip("no user cache directory on this host")
	}
	// HomeDir IS ONLY APPLIED BY A SANDBOX. WrapCommand honours cfg.HomeDir in
	// its bwrap and docker branches; SandboxNone is host execution, where the
	// command keeps the user's real HOME -- which is correct, because the whole
	// reason to redirect it is that bwrap's HOME is a tmpfs that would
	// re-download the module cache into RAM on every run. There is no tmpfs to
	// avoid in host mode, and the user's own cache is warm.
	//
	// So on a host with neither bwrap nor docker this test is asserting a
	// property nothing claims. Skipped by the product's own predicate rather
	// than by GOOS, so it also skips on a minimal Linux image.
	if !s.sandboxExecConfined() {
		t.Skip("nothing confines on this host, so HomeDir is not applied and there is no sandbox home to test")
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	first := makeRecipe(t, s, `echo "HOME=[$HOME]"; echo persisted > "$HOME/cache-probe"; echo WROTE=$?`)
	if !strings.Contains(first, "WROTE=0") {
		t.Fatalf("could not write to HOME inside the sandbox:\n%s", first)
	}
	if !strings.Contains(first, "HOME=["+home+"]") {
		t.Errorf("HOME is not the sandbox home; got:\n%s", first)
	}

	second := makeRecipe(t, s, `cat "$HOME/cache-probe" 2>&1 || echo CACHE_LOST`)
	if !strings.Contains(second, "persisted") {
		t.Errorf("the cache did not survive to the next run, so every build re-downloads "+
			"into the memory budget:\n%s", second)
	}
	if _, err := os.Stat(filepath.Join(home, "cache-probe")); err != nil {
		t.Errorf("the sandbox home is not a real host directory: %v", err)
	}
}

// THE PROPERTY, THROUGH THE REAL HANDLER: the toolchain actually runs, and its
// cache actually lands in the persistent sandbox home rather than in RAM.
//
// Both halves matter together. A toolchain that cannot start was the bug found
// by running the tool; a toolchain that starts and re-downloads its module
// cache into bwrap's tmpfs on every run is the bug the memory cap would have
// turned into mysterious OOM kills.
func TestTheGoToolchainRunsInTheSandboxAndCachesOnDisk(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	s := builtinTestServer(t)
	home := s.sandboxExecHome()
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	res, err := s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"go version"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "go version go") {
		t.Errorf("the go toolchain did not run inside the sandbox: %q", res.Content)
	}

	res, err = s.builtinSandboxExec(context.Background(), json.RawMessage(`{"command":"go env GOMODCACHE"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Same reasoning as TestTheSandboxHomeSurvivesBetweenRuns: the cache is only
	// redirected when a sandbox applies HomeDir. In host mode the toolchain uses
	// the user's own module cache, which is the right answer -- there is no
	// tmpfs HOME to re-download into.
	if home != "" && s.sandboxExecConfined() && !strings.HasPrefix(strings.TrimSpace(res.Content), home) {
		t.Errorf("GOMODCACHE is %q, not under the persistent sandbox home %q -- every build would "+
			"re-download into the memory budget", strings.TrimSpace(res.Content), home)
	}
}
