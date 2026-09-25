//go:build linux

package mcp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// THE CROSS-PROCESS PROOF FOR BWRAP. BwrapEgressUsable runs the real helper
// inside real bwrap: the listener fd travels out through bwrap's namespaces, the
// supervisor reads the destination out of a process in a DIFFERENT pid and user
// namespace, and a denied target must come back EPERM while an allowed one
// connects. None of that can be faked in-process, and none of it is implied by
// the Landlock probe passing -- under bwrap the supervisor has a namespace
// boundary to reach across that Landlock never puts in the way.
//
// On a host where bwrap works this must succeed; a failure is a real regression,
// not a skip.
//
// Neuter check: drop the egressDenied branch in egressSupervisor.handle and the
// denied-target connect stops returning EPERM, so the probe reports false.
func TestBwrapEgressUsableEnforcesEndToEnd(t *testing.T) {
	if !BwrapUsable() {
		t.Skip("NOT RUN: bwrap cannot create a user namespace on this host")
	}
	if !BwrapEgressUsable() {
		t.Fatal("BwrapEgressUsable is false on a bwrap host: the egress firewall did not enforce end to end")
	}
}

// The bwrap filter must trap connect and touch NOTHING else. Reusing the
// Landlock program here would newly refuse socket(AF_UNIX), non-stream
// socketpairs, System V IPC and io_uring inside bwrap -- all of which work there
// today -- which would break builds in the name of a restriction the bwrap prompt
// never claims. This test is what stops someone "simplifying" the two into one.
func TestTheEgressOnlyFilterTrapsConnectAndNothingElse(t *testing.T) {
	only, err := egressOnlyFilterProgram()
	if err != nil {
		t.Fatalf("egressOnlyFilterProgram: %v", err)
	}
	full, err := socketFilterProgram(true)
	if err != nil {
		t.Fatalf("socketFilterProgram: %v", err)
	}
	if len(only) >= len(full) {
		t.Errorf("the connect-only program (%d) is not smaller than the full one (%d)", len(only), len(full))
	}

	var trapsConnect, returnsUserNotif bool
	for _, ins := range only {
		if ins.K == uint32(unix.SYS_CONNECT) {
			trapsConnect = true
		}
		if ins.K == seccompRetUserNotif {
			returnsUserNotif = true
		}
	}
	if !trapsConnect || !returnsUserNotif {
		t.Errorf("the connect-only program does not trap connect to a user notification (connect=%v notif=%v)",
			trapsConnect, returnsUserNotif)
	}

	// The restrictions that must NOT come along.
	for _, unwanted := range []struct {
		name string
		k    uint32
	}{
		{"socket", uint32(unix.SYS_SOCKET)},
		{"socketpair", uint32(unix.SYS_SOCKETPAIR)},
		{"io_uring_setup", uint32(unix.SYS_IO_URING_SETUP)},
		{"EACCES", unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)},
		{"ENOSYS", unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
	} {
		for _, ins := range only {
			if ins.K == unwanted.k {
				t.Errorf("the connect-only program carries a rule for %s, which bwrap allows today", unwanted.name)
				break
			}
		}
	}

	// The arch guard is NOT optional: without it a foreign-architecture connect
	// walks past the trap and the block -- and the prompt's claim -- is false.
	var guardsArch bool
	for _, ins := range only {
		if ins.K == seccompAuditArch {
			guardsArch = true
		}
	}
	if !guardsArch {
		t.Error("the connect-only program has no architecture guard, so the trap is bypassable")
	}
}

// A half-applied egress-only invocation must be refused outright. Applying an
// EMPTY landlock policy as if it were real would confine the command to nothing
// at all; carrying a policy and skipping it would claim a confinement that never
// happened. Both are worse than failing to start.
func TestParseHelperArgsRefusesAHalfAppliedEgressOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "egress-only without a listener fd",
			args: []string{egressOnlyFlag, landlockArgsSeparator, "true"},
			want: egressFdFlag,
		},
		{
			name: "egress-only carrying a landlock policy",
			args: []string{egressOnlyFlag, egressFdFlag, "3", "--rx", "/usr", landlockArgsSeparator, "true"},
			want: "landlock policy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, err := parseHelperArgs(tc.args)
			if err == nil {
				t.Fatalf("parseHelperArgs accepted %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	// And the valid shape parses, with only set and no policy.
	policy, _, egress, argv, err := parseHelperArgs(
		[]string{egressOnlyFlag, egressFdFlag, "3", landlockArgsSeparator, "make"})
	if err != nil {
		t.Fatalf("the valid egress-only form was refused: %v", err)
	}
	if !egress.only || !egress.enabled || egress.fd != 3 {
		t.Errorf("parsed egress = %+v, want only+enabled on fd 3", egress)
	}
	if len(policy.Rules) != 0 {
		t.Errorf("egress-only produced %d landlock rules, want none", len(policy.Rules))
	}
	if len(argv) != 1 || argv[0] != "make" {
		t.Errorf("argv = %v, want [make]", argv)
	}
}

// The bwrap argv must run the command THROUGH the helper when filtering, bind the
// helper binary in (it is invisible in bwrap's mount namespace otherwise), and
// must NOT carry --daemon-pid: inside bwrap's pid namespace the daemon's pid does
// not resolve, so PR_SET_PTRACER would fail and take the whole command with it.
func TestWrapCommandRunsTheHelperInsideBwrapOnlyWhenFiltering(t *testing.T) {
	stubBackends(t, true, false)
	stubLimiter(t, false)
	ws := t.TempDir()

	cfg := SandboxConfig{Mode: SandboxBubblewrap, WorkspaceRoot: ws, AllowNetwork: true, EgressFilter: true}
	bin, args, err := WrapCommand("make", []string{"test"}, cfg)
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}
	if bin != "bwrap" {
		t.Fatalf("bin = %q, want bwrap", bin)
	}
	all := strings.Join(args, " ")
	self := selfExecutable()
	if self == "" {
		t.Fatal("this test binary is not registered as a sandbox helper, so it cannot check the filtered argv")
	}
	for _, want := range []string{
		"--ro-bind " + self + " " + self, // the helper binary, bound in
		self + " " + SandboxHelperArg,    // the command IS the helper
		egressOnlyFlag,
		egressFdFlag + " 3",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the filtered bwrap argv is missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, daemonPidFlag) {
		t.Errorf("the bwrap argv carries %s, which cannot resolve inside its pid namespace:\n%s",
			daemonPidFlag, all)
	}
	// The helper binds go LAST, after the /tmp tmpfs -- under `go test` the helper
	// lives in /tmp, and an earlier bind would be hidden by it.
	if strings.Index(all, "--tmpfs /tmp") > strings.Index(all, "--ro-bind "+self) {
		t.Errorf("the helper bind comes before the /tmp tmpfs, which would hide it:\n%s", all)
	}
	// The real command still ends the line, after the helper's own separator.
	if !strings.HasSuffix(all, landlockArgsSeparator+" make test") {
		t.Errorf("the argv does not end in the real command:\n%s", all)
	}

	// Without the flag, nothing changes: bwrap runs the command directly.
	cfg.EgressFilter = false
	_, plain, err := WrapCommand("make", []string{"test"}, cfg)
	if err != nil {
		t.Fatalf("WrapCommand: %v", err)
	}
	unfiltered := strings.Join(plain, " ")
	for _, unwanted := range []string{SandboxHelperArg, egressOnlyFlag, egressFdFlag} {
		if strings.Contains(unfiltered, unwanted) {
			t.Errorf("the unfiltered bwrap argv carries %q:\n%s", unwanted, unfiltered)
		}
	}
	if !strings.HasSuffix(unfiltered, "-- make test") {
		t.Errorf("the unfiltered argv does not run the command directly:\n%s", unfiltered)
	}
}

// BOTH PROBES MUST FAIL CLOSED. Everything downstream -- whether the firewall is
// installed at all, and whether the prompt claims a block -- is decided by these
// two booleans, so a probe that cannot run its check must answer NO, never "no
// news is good news". The exported names are sync.OnceValues and answer once per
// process, which is why the bodies are tested here directly.
func TestTheEgressProbesRefuseWhenTheBackendCannotRun(t *testing.T) {
	t.Run("bwrap unusable", func(t *testing.T) {
		stubBackends(t, false, true)
		if bwrapEgressUsable() {
			t.Error("the bwrap probe said yes on a host where bwrap cannot run")
		}
	})
	t.Run("landlock unusable", func(t *testing.T) {
		stubBackends(t, true, false)
		if landlockEgressUsable() {
			t.Error("the landlock probe said yes on a host where Landlock cannot run")
		}
	})
	t.Run("the helper binary cannot be found", func(t *testing.T) {
		stubBackends(t, true, true)
		origLookPath := lookPath
		t.Cleanup(func() { lookPath = origLookPath })
		lookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
		if bwrapEgressUsable() || landlockEgressUsable() {
			t.Error("a probe said yes with its binary missing from PATH")
		}
	})
}

// And the scaffolding itself refuses when the helper will not even start: a probe
// that cannot be run has not proven anything, and must not be read as success.
func TestEgressProbeRefusesWhenTheHelperCannotStart(t *testing.T) {
	if egressProbe(func(string) (string, []string) {
		return filepath.Join(t.TempDir(), "no-such-binary"), nil
	}) {
		t.Error("egressProbe reported success for a helper that never started")
	}
}

// A binary that cannot re-execute itself as the helper cannot install the
// filter. It must REFUSE, not run the command unfiltered -- by this point the
// handler has a supervisor waiting and the approval prompt has already told the
// user the metadata endpoint is blocked, so running without the filter would
// make that sentence false. Failing is recoverable; a silent lie is not.
func TestWrapCommandRefusesToFilterWithoutAHelperBinary(t *testing.T) {
	stubBackends(t, true, false)
	stubLimiter(t, false)
	orig := selfExecutable
	t.Cleanup(func() { selfExecutable = orig })
	selfExecutable = func() string { return "" }

	cfg := SandboxConfig{Mode: SandboxBubblewrap, WorkspaceRoot: t.TempDir(), AllowNetwork: true, EgressFilter: true}
	_, _, err := WrapCommand("make", nil, cfg)
	if err == nil {
		t.Fatal("WrapCommand built a bwrap command with the egress filter requested but not installable")
	}
	if !strings.Contains(err.Error(), "egress firewall") {
		t.Errorf("the refusal does not name the egress firewall: %v", err)
	}

	// Without the filter requested, the same host still works normally.
	cfg.EgressFilter = false
	if _, _, err := WrapCommand("make", nil, cfg); err != nil {
		t.Errorf("an unfiltered bwrap command was refused too: %v", err)
	}
}
