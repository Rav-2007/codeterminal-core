//go:build linux

package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// LandlockABI is the kernel's Landlock ABI version, or 0 when there is none.
var LandlockABI = sync.OnceValue(func() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
})

// LandlockUsable reports whether the landlock backend will actually ENFORCE its
// policy here, by running the real helper once.
//
// PRESENCE IS NOT CAPABILITY -- the lesson this file's neighbours each learned
// the hard way (BwrapUsable, LimiterUsable). A kernel that answers the version
// query is not yet proof, so the probe runs the helper exactly as a command
// would be run, with --self-check: after restricting itself and before running
// anything, the helper must be REFUSED three things -- reading a file outside
// its policy, creating a Unix socket, and setting up io_uring -- or it exits
// without running the command, and this reports false.
//
// One helper, once per process.
var LandlockUsable = sync.OnceValue(func() bool {
	if LandlockABI() < 1 || !seccompSupported {
		return false
	}
	self := selfExecutable()
	if self == "" {
		return false
	}
	truePath, err := lookPath("true")
	if err != nil {
		return false
	}
	dir, err := os.MkdirTemp("", "mochiii-landlock-probe-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	workspace := filepath.Join(dir, "workspace")
	outside := filepath.Join(dir, "outside")
	if os.Mkdir(workspace, 0o700) != nil || os.WriteFile(outside, []byte("must not be readable"), 0o600) != nil {
		return false
	}

	// The production policy's shape, with a workspace of its own. `true` is in
	// /usr/bin, which the system rules grant.
	policy := landlockPolicyFor("", SandboxConfig{WorkspaceRoot: workspace})
	args := append([]string{SandboxHelperArg}, policy.args()...)
	args = append(args, selfCheckFlag, outside, landlockArgsSeparator, truePath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, self, args...)
	probe.Env = ServerEnv(nil)
	return probe.Run() == nil
})

// sandboxExecMain is the helper: restrict this thread, then become the command.
func sandboxExecMain(args []string) int {
	policy, selfCheck, argv, err := parseHelperArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", helperErrorPrefix, err)
		return exitSandboxSetup
	}
	// Resolved BEFORE restricting: looking the command up needs nothing the
	// policy withholds, but failing here gives the shell's own exit status.
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", helperErrorPrefix, err)
		return exitSandboxNotFound
	}

	// ONE THREAD, START TO FINISH. no_new_privs, the seccomp filter and the
	// Landlock domain are all per-thread; execve from this thread carries all
	// three into the command, and every other runtime thread ends at execve.
	// Locking is what guarantees the exec happens on the thread that was
	// restricted.
	runtime.LockOSThread()

	// bwrap's --new-session, for the same reason: no controlling terminal to
	// push keystrokes into (TIOCSTI). Refused only if already a session
	// leader, which is harmless; /dev/tty is outside the policy regardless.
	_, _ = unix.Setsid()

	if err := applySandbox(policy, LandlockABI()); err != nil {
		fmt.Fprintf(os.Stderr, "%s%v\n", helperErrorPrefix, err)
		return exitSandboxSetup
	}
	if selfCheck != "" {
		if err := checkEnforced(selfCheck); err != nil {
			fmt.Fprintf(os.Stderr, "%sself-check: %v\n", helperErrorPrefix, err)
			return exitSandboxSelfCheck
		}
	}
	err = syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "%sexec %s: %v\n", helperErrorPrefix, argv[0], err)
	return exitSandboxNotExec
}

// applySandbox restricts the CALLING THREAD: no_new_privs, then the seccomp
// filter, then the Landlock domain. It cannot be undone, which is the point;
// callers lock the thread first.
func applySandbox(policy landlockPolicy, abi int) error {
	if abi < 1 {
		return errors.New("this kernel has no Landlock")
	}
	if !seccompSupported {
		return fmt.Errorf("no seccomp filter for %s", runtime.GOARCH)
	}
	// Required for an unprivileged process to install either of the two, and
	// wanted anyway: a setuid binary run inside cannot gain what we took away.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := installSocketFilter(); err != nil {
		return err
	}
	return restrictLandlock(policy, abi)
}

// Landlock rights, by what they apply to.
const (
	landlockFileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	landlockReadRights = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
)

// handledFSRights is every file-system right this ABI knows. Handling all of
// them is what makes the policy an allowlist: a right the ruleset does not
// handle is a right it silently grants everywhere.
func handledFSRights(abi int) uint64 {
	r := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		r |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		r |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		r |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return r
}

func rightsFor(access string) uint64 {
	switch access {
	case accessReadExec:
		return landlockReadRights | unix.LANDLOCK_ACCESS_FS_EXECUTE
	case accessRead:
		return landlockReadRights
	case accessReadWrite:
		return ^uint64(0) // everything handled; masked below
	case accessDevice:
		return unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return 0
}

// restrictLandlock builds the ruleset for policy and applies it to the calling
// thread.
func restrictLandlock(policy landlockPolicy, abi int) error {
	handled := handledFSRights(abi)
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	if abi >= 6 {
		// Abstract Unix sockets and signals, scoped to the domain: nothing
		// inside can reach a process outside by either. The seccomp filter
		// already refuses Unix sockets outright; this is the kernel saying the
		// same thing a second way.
		attr.Scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
	}
	// The full struct on every ABI: an older kernel accepts a larger struct
	// whose unknown trailing fields are zero, and they are zero below ABI 6.
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer func() { _ = unix.Close(ruleset) }()

	for _, rule := range policy.Rules {
		if err := addLandlockRule(ruleset, rule.Path, rightsFor(rule.Access)&handled); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("landlock restrict: %w", errno)
	}
	return nil
}

// addLandlockRule grants rights beneath path. A path that does not exist on
// this host (/lib64 on arm64, /etc/pki on Debian) is skipped, exactly as the
// bwrap backend skips binding it.
func addLandlockRule(ruleset int, path string, rights uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("landlock rule %s: %w", path, err)
	}
	defer func() { _ = unix.Close(fd) }()

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("landlock rule %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		// The kernel refuses directory rights on a file outright.
		rights &= landlockFileRights
	}
	if rights == 0 {
		return nil
	}
	beneath := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset),
		unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&beneath)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock rule %s: %w", path, errno)
	}
	return nil
}

// THE SECCOMP FILTER: what Landlock cannot say.
//
// Landlock on current kernels has no right for CONNECTING to a Unix socket that
// lives at a path. Without this filter a build script could open
// /run/user/<uid>/bus and ask systemd, over the session bus, to run anything it
// liked outside the sandbox -- or use ssh-agent's socket, or this daemon's.
// So:
//
//   - socket(AF_UNIX, ...) is refused with EACCES: pathname and abstract alike.
//   - socketpair is allowed only for SOCK_STREAM. A stream pair is connected
//     to itself and cannot be pointed anywhere else, and it is what child
//     processes talk over (Node, Python). A datagram pair CAN send to any
//     socket by path, so it is refused.
//   - io_uring_setup is refused with ENOSYS: io_uring creates and connects
//     sockets without ever calling socket(2).
//   - A syscall made through a foreign ABI (i386 via int 0x80, whose socketcall
//     multiplexes socket(2); x32 on amd64) is refused with EPERM. Each is a
//     known way around a filter keyed on syscall numbers.
//
// Everything else is allowed: this is a filter for one hole, not a policy.

// Offsets into struct seccomp_data.
const (
	seccompDataNr   = 0
	seccompDataArch = 4
	seccompDataArg0 = 16
	seccompDataArg1 = 24
	sockTypeMask    = 0xf // SOCK_TYPE_MASK: strips SOCK_NONBLOCK and SOCK_CLOEXEC
	x32SyscallBit   = 0x40000000
)

// bpfStep is one filter instruction, with jumps written as labels.
type bpfStep struct {
	label  string
	code   uint16
	k      uint32
	jt, jf string // "" means the next instruction
}

// assembleBPF resolves labels into the relative offsets classic BPF uses.
func assembleBPF(steps []bpfStep) ([]unix.SockFilter, error) {
	at := map[string]int{}
	for i, s := range steps {
		if s.label != "" {
			at[s.label] = i
		}
	}
	offset := func(from int, label string) (uint8, error) {
		if label == "" {
			return 0, nil
		}
		to, ok := at[label]
		if !ok {
			return 0, fmt.Errorf("bpf: no label %q", label)
		}
		d := to - (from + 1)
		if d < 0 || d > 255 {
			return 0, fmt.Errorf("bpf: jump to %q is out of range", label)
		}
		return uint8(d), nil
	}
	out := make([]unix.SockFilter, len(steps))
	for i, s := range steps {
		jt, err := offset(i, s.jt)
		if err != nil {
			return nil, err
		}
		jf, err := offset(i, s.jf)
		if err != nil {
			return nil, err
		}
		out[i] = unix.SockFilter{Code: s.code, Jt: jt, Jf: jf, K: s.k}
	}
	return out, nil
}

func socketFilterProgram() ([]unix.SockFilter, error) {
	const (
		ld  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jge = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
		and = unix.BPF_ALU | unix.BPF_AND | unix.BPF_K
		ret = unix.BPF_RET | unix.BPF_K
	)
	steps := []bpfStep{
		{code: ld, k: seccompDataArch},
		{code: jeq, k: seccompAuditArch, jf: "eperm"},
		{code: ld, k: seccompDataNr},
	}
	if seccompX32Possible {
		steps = append(steps, bpfStep{code: jge, k: x32SyscallBit, jt: "eperm"})
	}
	// System V IPC and POSIX message queues: refused with EPERM. Each only jumps
	// on a match and falls through otherwise, so the io_uring line below stays
	// the single terminal fallthrough to "allow". See seccompBlockedSyscalls.
	for _, nr := range seccompBlockedSyscalls {
		steps = append(steps, bpfStep{code: jeq, k: nr, jt: "eperm"})
	}
	steps = append(steps,
		bpfStep{code: jeq, k: uint32(unix.SYS_SOCKET), jt: "socket"},
		bpfStep{code: jeq, k: uint32(unix.SYS_SOCKETPAIR), jt: "socketpair"},
		bpfStep{code: jeq, k: uint32(unix.SYS_IO_URING_SETUP), jt: "enosys", jf: "allow"},

		bpfStep{label: "socket", code: ld, k: seccompDataArg0},
		bpfStep{code: jeq, k: unix.AF_UNIX, jt: "eacces", jf: "allow"},

		bpfStep{label: "socketpair", code: ld, k: seccompDataArg1},
		bpfStep{code: and, k: sockTypeMask},
		bpfStep{code: jeq, k: unix.SOCK_STREAM, jt: "allow", jf: "eacces"},

		bpfStep{label: "allow", code: ret, k: unix.SECCOMP_RET_ALLOW},
		bpfStep{label: "eperm", code: ret, k: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		bpfStep{label: "eacces", code: ret, k: unix.SECCOMP_RET_ERRNO | uint32(unix.EACCES)},
		bpfStep{label: "enosys", code: ret, k: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
	)
	return assembleBPF(steps)
}

// installSocketFilter installs the filter on the calling thread.
func installSocketFilter() error {
	filter, err := socketFilterProgram()
	if err != nil {
		return err
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return fmt.Errorf("seccomp filter: %w", err)
	}
	runtime.KeepAlive(filter)
	return nil
}

// checkEnforced is the probe's proof, run on the restricted thread: each of
// these must be REFUSED, and refused for the reason this sandbox gives.
func checkEnforced(outside string) error {
	f, err := os.Open(outside)
	if err == nil {
		_ = f.Close()
		return fmt.Errorf("%s, outside the policy, was readable", outside)
	}
	if !errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("reading %s: want a permission error, got %w", outside, err)
	}
	if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); err == nil {
		_ = unix.Close(fd)
		return errors.New("a Unix socket could be created")
	} else if !errors.Is(err, unix.EACCES) {
		return fmt.Errorf("creating a Unix socket: want EACCES, got %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0); errno != unix.ENOSYS {
		return fmt.Errorf("io_uring_setup: want ENOSYS, got %v", errno)
	}
	return nil
}
