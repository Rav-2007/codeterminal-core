package mcp

import (
	"os/exec"
	"reflect"
	"slices"
	"testing"
)

// stubBackends pins what the three probes answer for the length of a test.
func stubBackends(t *testing.T, bwrap, landlock bool) {
	t.Helper()
	origBwrap, origLandlock := BwrapUsable, LandlockUsable
	t.Cleanup(func() { BwrapUsable, LandlockUsable = origBwrap, origLandlock })
	BwrapUsable = func() bool { return bwrap }
	LandlockUsable = func() bool { return landlock }
}

// THE ORDER IS THE SCOPE. Landlock is last, so every host that gets bwrap or
// docker today still does; it is opt-in, so third-party MCP servers never get
// it by default; and it is never chosen for a config that forbids the network,
// a promise Landlock cannot keep (it can refuse TCP, not UDP).
//
// Neuter check: move the landlock case above BwrapUsable() in ResolveMode, and
// the first row fails.
func TestAutoPrefersBwrapThenDockerThenLandlock(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "/usr/bin/docker", nil }

	base := SandboxConfig{Mode: SandboxAuto, WorkspaceRoot: "/work", AllowNetwork: true, LandlockFallback: true}
	withImage := base
	withImage.Image = "golang:1.25"
	noOptIn := base
	noOptIn.LandlockFallback = false
	offline := base
	offline.AllowNetwork = false

	cases := []struct {
		name            string
		bwrap, landlock bool
		cfg             SandboxConfig
		want            SandboxMode
	}{
		{"bwrap wins over everything", true, true, withImage, SandboxBubblewrap},
		{"docker wins over landlock", false, true, withImage, SandboxDocker},
		{"landlock when neither can run", false, true, base, SandboxLandlock},
		{"never without the opt-in (Lane B)", false, true, noOptIn, SandboxNone},
		{"never when the network is forbidden", false, true, offline, SandboxNone},
		{"none when landlock cannot run either", false, false, base, SandboxNone},
	}
	for _, c := range cases {
		stubBackends(t, c.bwrap, c.landlock)
		if got := ResolveMode(c.cfg); got != c.want {
			t.Errorf("%s: ResolveMode = %q, want %q", c.name, got, c.want)
		}
	}
}

// The explanation walks the same ladder the selection did, and says nothing
// about backends ranked below the one chosen.
func TestPassedOverExplainsTheLadderInOrder(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(file string) (string, error) {
		if file == "bwrap" {
			return "/usr/bin/bwrap", nil
		}
		return "", exec.ErrNotFound
	}
	cfg := SandboxConfig{Mode: SandboxAuto, WorkspaceRoot: "/work", AllowNetwork: true, LandlockFallback: true}

	stubBackends(t, false, true)
	if got, want := PassedOver(cfg), []string{"bwrap is installed but cannot start a sandbox here", "Docker is not installed"}; !reflect.DeepEqual(got, want) {
		t.Errorf("landlock chosen: PassedOver = %q, want %q", got, want)
	}

	stubBackends(t, false, false)
	if got := PassedOver(cfg); len(got) != 3 || got[2] != "Landlock is unavailable" {
		t.Errorf("nothing chosen: PassedOver = %q, want bwrap, docker and landlock, in that order", got)
	}
	offline := cfg
	offline.AllowNetwork = false
	if got := PassedOver(offline); got[len(got)-1] != "Landlock cannot keep this command off the network" {
		t.Errorf("offline: PassedOver = %q", got)
	}

	stubBackends(t, true, true)
	if got := PassedOver(cfg); got != nil {
		t.Errorf("bwrap chosen: nothing was passed over, got %q", got)
	}
	explicit := cfg
	explicit.Mode = SandboxNone
	if got := PassedOver(explicit); got != nil {
		t.Errorf("an explicit mode was not chosen by the ladder, got %q", got)
	}
}

// THE ALLOWLIST IS THE ONE INTENDED: the workspace and the command's own home
// writable, the system readable, and never the user's real home, /tmp or /run.
//
// Neuter check: add `p.add(accessRead, "/")` to landlockPolicyFor, and the
// "never granted" half fails.
func TestTheLandlockPolicyGrantsTheWorkspaceAndNotTheUsersHome(t *testing.T) {
	cfg := SandboxConfig{WorkspaceRoot: "/home/u/project", HomeDir: "/home/u/.cache/mochiii/sandbox-home/abc"}
	p := landlockPolicyFor("", cfg)

	grants := func(access, path string) bool {
		return slices.Contains(p.Rules, landlockRule{Access: access, Path: path})
	}
	for _, want := range []landlockRule{
		{accessReadWrite, "/home/u/project"},
		{accessReadWrite, "/home/u/.cache/mochiii/sandbox-home/abc"},
		{accessReadExec, "/usr"},
		{accessRead, "/etc/ssl"},
		{accessRead, "/proc"},
		{accessDevice, "/dev/null"},
		{accessRead, "/dev/urandom"},
	} {
		if !grants(want.Access, want.Path) {
			t.Errorf("the policy does not grant %s %s", want.Access, want.Path)
		}
	}
	for _, r := range p.Rules {
		for _, never := range []string{"/", "/home", "/home/u", "/tmp", "/run", "/etc", "/sys", "/dev", "/dev/tty"} {
			if r.Path == never {
				t.Errorf("the policy grants %s %s, which it must never grant", r.Access, r.Path)
			}
		}
	}

	ro := cfg
	ro.ReadOnlyWorkspace = true
	if p := landlockPolicyFor("", ro); slices.Contains(p.Rules, landlockRule{Access: accessReadWrite, Path: "/home/u/project"}) {
		t.Error("a read-only workspace was granted write access")
	}
}

// A TOOLCHAIN ROOT THAT CONTAINS THE WORKSPACE IS A HOME DIRECTORY. `go` in
// ~/bin has root ~, and granting that would grant ~/.ssh with it; bwrap refuses
// the same bind for the same reason.
func TestAToolchainRootAboveTheWorkspaceIsNotGranted(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })

	lookPath = func(string) (string, error) { return "/home/u/bin/go", nil }
	p := landlockPolicyFor("go", SandboxConfig{WorkspaceRoot: "/home/u/project"})
	if slices.Contains(p.Rules, landlockRule{Access: accessReadExec, Path: "/home/u"}) {
		t.Error("the toolchain root /home/u contains the workspace and was granted anyway")
	}

	lookPath = func(string) (string, error) { return "/opt/go/bin/go", nil }
	p = landlockPolicyFor("go", SandboxConfig{WorkspaceRoot: "/home/u/project"})
	if !slices.Contains(p.Rules, landlockRule{Access: accessReadExec, Path: "/opt/go"}) {
		t.Errorf("a toolchain outside the system paths was not granted: %v", p.Rules)
	}
}

// ONE LIST. The bwrap binds and the landlock policy read the same system
// paths, so the two backends cannot drift apart on what "the system" is.
//
// Neuter check: give landlockPolicyFor its own copy of the list with one path
// changed, and this fails.
func TestBwrapAndLandlockShareTheSystemPaths(t *testing.T) {
	p := landlockPolicyFor("", SandboxConfig{WorkspaceRoot: "/work"})
	for _, path := range sandboxSystemPaths {
		if !slices.ContainsFunc(p.Rules, func(r landlockRule) bool { return r.Path == path }) {
			t.Errorf("the landlock policy is missing system path %s", path)
		}
	}
}

// The policy survives the trip through argv exactly, and the parser refuses
// anything it does not understand rather than guessing.
func TestHelperArgsRoundTripAndRefuseNonsense(t *testing.T) {
	p := landlockPolicyFor("", SandboxConfig{WorkspaceRoot: "/work", HomeDir: "/home/cache"})
	args := append(p.args(), selfCheckFlag, "/tmp/outside", "--", "go", "test", "./...")
	got, check, argv, err := parseHelperArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) || check != "/tmp/outside" || !reflect.DeepEqual(argv, []string{"go", "test", "./..."}) {
		t.Errorf("round trip changed the policy or command:\n got %v %q %q\nwant %v", got, check, argv, p)
	}

	for _, bad := range [][]string{
		{"--rx", "/usr"},                // no separator
		{"--rx", "/usr", "--"},          // no command
		{"--rx", "relative", "--", "x"}, // not absolute
		{"--wx", "/usr", "--", "x"},     // unknown access
		{"--rx"},                        // flag with no value
	} {
		if _, _, _, err := parseHelperArgs(bad); err == nil {
			t.Errorf("parseHelperArgs(%q) accepted it", bad)
		}
	}
}

// Nothing re-executes a binary as the helper unless that binary dispatches the
// helper: one that did not would run its own test suite instead -- which may
// probe again, and again.
//
// Neuter check: drop the helperRegistered test from resolveSelfExecutable, and
// the unregistered half fails.
func TestAnUnregisteredBinaryIsNeverUsedAsTheHelper(t *testing.T) {
	if !helperRegistered.Load() {
		t.Fatal("TestMain did not register this test binary as the helper")
	}
	if exe := resolveSelfExecutable(); exe == "" {
		t.Fatal("a registered test binary resolved to no helper at all")
	}
	helperRegistered.Store(false)
	defer helperRegistered.Store(true)
	if exe := resolveSelfExecutable(); exe != "" {
		t.Errorf("an unregistered binary would be re-executed as the helper: %s", exe)
	}
}
