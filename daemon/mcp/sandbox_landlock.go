package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// THE LANDLOCK BACKEND, the portable half: the policy, how it travels, and the
// helper entry point. The Linux half -- the syscalls -- is
// sandbox_landlock_linux.go.
//
// WHY IT EXISTS. On Ubuntu 24.04 and later, kernel.apparmor_restrict_unprivileged_userns=1
// is the default, and under it bwrap is installed and cannot run (BwrapUsable).
// With no Docker either, SandboxAuto fell through to SandboxNone and
// sandbox_exec ran every approved build with the user's full privileges --
// measured on 2026-09-21 on exactly such a machine, where the same daemon was
// confined when VS Code started it (its AppArmor profile permits user
// namespaces) and unconfined when a terminal did.
//
// Landlock and seccomp are both unprivileged and neither needs a namespace, so
// that restriction does not reach them. What they give up relative to bwrap is
// stated in the approval prompt rather than discovered: no private process
// table and no private /tmp.
//
// HOW IT RUNS. Nothing in os/exec runs code between fork and exec, so the
// daemon re-executes ITSELF as a small helper: `<daemon> __sandbox-exec <policy>
// -- <command> <args>`. The helper restricts its own thread and then execs the
// command from that thread, which carries the restrictions into it.

// SandboxHelperArg is the argv[1] that turns this binary into the helper.
const SandboxHelperArg = "__sandbox-exec"

// Exit statuses the helper uses for its own failures, so they cannot be
// mistaken for the command's. 125 and 126/127 follow docker and the shell.
const (
	exitSandboxSetup      = 125 // the sandbox could not be built; nothing ran
	exitSandboxNotExec    = 126 // the command was found and could not be executed
	exitSandboxNotFound   = 127 // the command was not found
	exitSandboxSelfCheck  = 97  // the probe's self-check found a restriction not enforced
	helperErrorPrefix     = "mochiii sandbox: "
	selfCheckFlag         = "--self-check"
	landlockArgsSeparator = "--"

	// Egress firewall (sandbox_egress_linux.go): the helper installs a
	// connect(2)-trapping listener, hands its fd to the daemon over the socketpair
	// at egressFdFlag, and grants the daemon (daemonPidFlag) the ptrace access the
	// supervisor needs. egressSelfCheckFlag carries "<denied>,<allowed>" host:port
	// pairs for the EgressFilterUsable probe.
	egressFdFlag        = "--egress-fd"
	daemonPidFlag       = "--daemon-pid"
	egressSelfCheckFlag = "--self-check-egress"

	// egressOnlyFlag is the bwrap backend's mode: install the egress firewall and
	// NOTHING else -- no Landlock domain, no socket filter. bwrap has already
	// confined the filesystem by the time this helper runs, and a bwrap host need
	// not have Landlock at all. Takes no value.
	egressOnlyFlag = "--egress-only"
)

// helperEgress is the egress-firewall half of a helper invocation: whether it is
// on, the socketpair fd to hand the listener back on, the daemon pid to grant
// ptrace access, the probe's self-check addresses (empty for a real run), and
// whether the firewall is the ONLY thing this helper applies (the bwrap mode).
type helperEgress struct {
	enabled   bool
	fd        int
	daemonPid int
	selfCheck string
	only      bool
}

// validate refuses a half-applied egress-only invocation. The bwrap mode applies
// the firewall and nothing else, so it MUST carry a listener fd, and it must NOT
// carry a Landlock policy: an empty policy applied as if it were real would
// confine the command to nothing at all, and a policy carried but skipped would
// be a confinement claimed and never enforced. Either is worse than refusing.
func (e helperEgress) validate(policy landlockPolicy) error {
	if !e.only {
		return nil
	}
	if !e.enabled {
		return fmt.Errorf("%s needs %s", egressOnlyFlag, egressFdFlag)
	}
	if len(policy.Rules) > 0 {
		return fmt.Errorf("%s cannot be combined with a landlock policy (%d rules given)",
			egressOnlyFlag, len(policy.Rules))
	}
	return nil
}

// helperRegistered is set by MaybeRunSandboxHelper. Nothing re-executes this
// binary as a helper unless it has been, because a binary that does not
// dispatch SandboxHelperArg -- any test binary that forgot to -- would run its
// whole test suite instead, which may probe again, and again.
var helperRegistered atomic.Bool

// MaybeRunSandboxHelper runs the helper and exits if this process was started
// as one, and otherwise records that this binary can serve as one.
//
// It must be the first thing main (and any TestMain whose tests reach the
// landlock backend) does: the helper's job is done before anything else in the
// program has started.
func MaybeRunSandboxHelper() {
	if len(os.Args) > 1 && os.Args[1] == SandboxHelperArg {
		os.Exit(sandboxExecMain(os.Args[2:]))
	}
	helperRegistered.Store(true)
}

// selfExecutable is the path the helper is re-executed from, or "" when it
// cannot be. Resolved once, at first use.
//
// NOT /proc/self/exe. Under the limiter the helper is started by systemd, and
// /proc/self/exe resolved there names systemd.
var selfExecutable = sync.OnceValue(resolveSelfExecutable)

func resolveSelfExecutable() string {
	if !helperRegistered.Load() {
		return ""
	}
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// Access classes a landlock rule can grant.
const (
	accessReadExec  = "rx"  // read files and directories, and execute
	accessRead      = "ro"  // read files and directories
	accessReadWrite = "rw"  // everything, INCLUDING execute: a build runs what it just compiled
	accessDevice    = "dev" // a character device: read, write and ioctl
)

type landlockRule struct {
	Access string
	Path   string
}

// landlockPolicy is the complete list of what a confined command may touch.
// Anything not beneath one of these paths is refused.
type landlockPolicy struct {
	Rules []landlockRule
}

func (p *landlockPolicy) add(access, path string) {
	p.Rules = append(p.Rules, landlockRule{Access: access, Path: filepath.Clean(path)})
}

// landlockPolicyFor is the policy sandbox_exec's commands run under. It
// mirrors the bwrap backend's binds rule for rule, and the table below is the
// whole of it:
//
//	read + execute   the system binaries and libraries, and the command's toolchain
//	read             TLS and DNS configuration (/etc/ssl, ...), and /proc
//	read + write     the workspace, and HomeDir (the command's HOME and TMPDIR)
//	devices          /dev/null, /dev/zero, /dev/full; /dev/urandom and /dev/random read-only
//
// NOT GRANTED, AND THAT IS THE POINT: the user's real home (~/.ssh, ~/.aws,
// browser profiles), /tmp (other programs' files and sockets live there), /run
// (the session bus, keyrings, agents and this daemon's own socket), the rest of
// /etc, /sys, and /dev/tty.
//
// /proc is readable because language runtimes read /proc/self. Other processes'
// environ, mem and fd entries stay closed regardless: Landlock refuses
// ptrace-mode access from inside a domain to any process outside it.
func landlockPolicyFor(command string, cfg SandboxConfig) landlockPolicy {
	var p landlockPolicy
	for _, path := range sandboxSystemPaths {
		if strings.HasPrefix(path, "/etc/") {
			p.add(accessRead, path)
		} else {
			p.add(accessReadExec, path)
		}
	}
	ws := filepath.Clean(cfg.WorkspaceRoot)
	// The same condition the bwrap backend binds the toolchain under, for the
	// same reason: a toolchain root that CONTAINS the workspace is a home
	// directory or wider, and granting it would grant everything beside the
	// workspace too.
	if root := toolchainRoot(command); root != "" && !coveredBy(root, sandboxSystemPaths) && !coveredBy(ws, []string{root}) {
		p.add(accessReadExec, root)
	}
	p.add(accessRead, "/proc")
	for _, dev := range []string{"/dev/null", "/dev/zero", "/dev/full"} {
		p.add(accessDevice, dev)
	}
	for _, dev := range []string{"/dev/urandom", "/dev/random"} {
		p.add(accessRead, dev)
	}
	if cfg.ReadOnlyWorkspace {
		p.add(accessReadExec, ws)
	} else {
		p.add(accessReadWrite, ws)
	}
	if cfg.HomeDir != "" {
		p.add(accessReadWrite, cfg.HomeDir)
	}
	return p
}

// args encodes the policy as helper flags: `--rx /usr --rw /work ...`.
func (p landlockPolicy) args() []string {
	out := make([]string, 0, 2*len(p.Rules))
	for _, r := range p.Rules {
		out = append(out, "--"+r.Access, r.Path)
	}
	return out
}

// parseHelperArgs is the helper's side of args: rules, then an optional
// --self-check path, then "--" and the command.
func parseHelperArgs(args []string) (policy landlockPolicy, selfCheck string, egress helperEgress, argv []string, err error) {
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == landlockArgsSeparator {
			argv = args[i+1:]
			if len(argv) == 0 {
				return policy, "", egress, nil, fmt.Errorf("no command after %q", landlockArgsSeparator)
			}
			if vErr := egress.validate(policy); vErr != nil {
				return policy, "", egress, nil, vErr
			}
			return policy, selfCheck, egress, argv, nil
		}
		// The one flag that takes no value, checked before the value lookup below.
		if flag == egressOnlyFlag {
			egress.only = true
			continue
		}
		if i+1 >= len(args) {
			return policy, "", egress, nil, fmt.Errorf("%s has no value", flag)
		}
		value := args[i+1]
		i++
		switch flag {
		case "--" + accessReadExec, "--" + accessRead, "--" + accessReadWrite, "--" + accessDevice:
			if !filepath.IsAbs(value) {
				return policy, "", egress, nil, fmt.Errorf("%s %q is not an absolute path", flag, value)
			}
			policy.add(strings.TrimPrefix(flag, "--"), value)
		case selfCheckFlag:
			selfCheck = value
		case egressFdFlag:
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n < 0 {
				return policy, "", egress, nil, fmt.Errorf("%s %q is not a valid fd", flag, value)
			}
			egress.enabled, egress.fd = true, n
		case daemonPidFlag:
			n, convErr := strconv.Atoi(value)
			if convErr != nil || n <= 0 {
				return policy, "", egress, nil, fmt.Errorf("%s %q is not a valid pid", flag, value)
			}
			egress.daemonPid = n
		case egressSelfCheckFlag:
			egress.selfCheck = value
		default:
			return policy, "", egress, nil, fmt.Errorf("unknown flag %q", flag)
		}
	}
	return policy, "", egress, nil, fmt.Errorf("no %q before the command", landlockArgsSeparator)
}
