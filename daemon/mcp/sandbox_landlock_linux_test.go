//go:build linux

package mcp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// requireLandlock skips, saying why, where the kernel cannot run these tests.
func requireLandlock(t *testing.T) int {
	t.Helper()
	abi := LandlockABI()
	if abi < 1 {
		t.Skip("NOT RUN: this kernel has no Landlock (ABI 0)")
	}
	if !seccompSupported {
		t.Skipf("NOT RUN: no seccomp filter written for %s", runtime.GOARCH)
	}
	return abi
}

// inSandbox runs fn on ONE OS thread of this test process after applying the
// sandbox to that thread alone, and returns what fn returned.
//
// This is how the restrictions are tested in-process, with coverage: the
// goroutine locks its thread and never unlocks it, so when it returns the Go
// runtime destroys the thread -- restrictions and all -- and every other
// thread in the test binary is untouched throughout. Everything fn does runs
// on the restricted thread, because a locked goroutine never migrates.
func inSandbox(t *testing.T, policy landlockPolicy, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked, deliberately: see above
		if err := applySandbox(policy, LandlockABI()); err != nil {
			done <- fmt.Errorf("applying the sandbox: %w", err)
			return
		}
		done <- fn()
	}()
	return <-done
}

// fixture is a workspace and home the policy grants, and a directory beside
// them it does not.
type fixture struct {
	workspace, home, outside string
	policy                   landlockPolicy
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		workspace: filepath.Join(root, "workspace"),
		home:      filepath.Join(root, "home"),
		outside:   filepath.Join(root, "outside"),
	}
	for _, dir := range []string{f.workspace, f.home, f.outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(f.workspace, "main.go"), filepath.Join(f.outside, "secret")} {
		if err := os.WriteFile(file, []byte("contents of "+filepath.Base(file)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.policy = landlockPolicyFor("", SandboxConfig{WorkspaceRoot: f.workspace, HomeDir: f.home})
	return f
}

// FILES OUTSIDE THE POLICY ARE UNREADABLE AND UNWRITABLE, and the workspace is
// both -- the property the whole backend exists for.
//
// Neuter check: make restrictLandlock return nil before landlock_restrict_self,
// and the secret is read.
func TestLandlockConfinesFilesToThePolicy(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)

	err := inSandbox(t, f.policy, func() error {
		if data, err := os.ReadFile(filepath.Join(f.outside, "secret")); err == nil {
			return fmt.Errorf("read a file outside the policy: %q", data)
		} else if !errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("reading outside: want a permission error, got %w", err)
		}
		if err := os.WriteFile(filepath.Join(f.outside, "planted"), []byte("x"), 0o600); err == nil {
			return errors.New("wrote a file outside the policy")
		}
		if _, err := os.ReadFile(filepath.Join(f.workspace, "main.go")); err != nil {
			return fmt.Errorf("the workspace is not readable: %w", err)
		}
		if err := os.WriteFile(filepath.Join(f.workspace, "built"), []byte("x"), 0o600); err != nil {
			return fmt.Errorf("the workspace is not writable: %w", err)
		}
		if err := os.MkdirAll(filepath.Join(f.home, ".cache", "go-build"), 0o700); err != nil {
			return fmt.Errorf("HOME is not writable: %w", err)
		}
		if _, err := os.ReadDir("/usr/bin"); err != nil {
			return fmt.Errorf("the system is not readable: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.outside, "planted")); err == nil {
		t.Error("a file was planted outside the policy")
	}
}

// NO WAY OUT THROUGH A UNIX SOCKET. The test listens on a socket outside the
// policy -- standing in for the session bus, ssh-agent and this daemon -- and
// the sandboxed thread cannot reach it. Stream socketpairs (what child
// processes talk over) and TCP still work.
//
// Neuter check: point the AF_UNIX comparison in socketFilterProgram at
// "allow", and the dial succeeds.
func TestUnixSocketsAreRefusedButPipesAndTCPAreNot(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	sock := filepath.Join(f.outside, "bus")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()

	err = inSandbox(t, f.policy, func() error {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return errors.New("connected to a Unix socket outside the sandbox")
		} else if !errors.Is(err, unix.EACCES) {
			return fmt.Errorf("dialling the socket: want EACCES, got %w", err)
		}
		if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM, 0); err == nil {
			_ = unix.Close(fd)
			return errors.New("created an AF_UNIX datagram socket")
		}
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("a stream socketpair, which child processes need, was refused: %w", err)
		}
		_, _ = unix.Close(fds[0]), unix.Close(fds[1])
		if fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0); err == nil {
			_, _ = unix.Close(fds[0]), unix.Close(fds[1])
			return errors.New("a datagram socketpair, which can send to any socket by path, was allowed")
		}
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		if err != nil {
			return fmt.Errorf("TCP, which builds need for downloads, was refused: %w", err)
		}
		_ = unix.Close(fd)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// io_uring can create and connect sockets without calling socket(2), so it is
// refused outright. The unsandboxed baseline proves the refusal is the filter's.
//
// Neuter check: drop the io_uring_setup comparison, and it returns EFAULT.
func TestIOURingIsRefused(t *testing.T) {
	requireLandlock(t)
	if _, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0); errno == unix.ENOSYS {
		t.Skip("NOT RUN: this kernel has no io_uring, so there is nothing to refuse")
	}
	f := newFixture(t)
	err := inSandbox(t, f.policy, func() error {
		if _, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0); errno != unix.ENOSYS {
			return fmt.Errorf("io_uring_setup: want ENOSYS, got %v", errno)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// SYSTEM V IPC AND POSIX MESSAGE QUEUES ARE REFUSED, so a command cannot attach
// to another of the user's processes' shared memory, semaphores or queues the
// way it could without bwrap's --unshare-ipc. None of these is path-based or
// covered by a landlock scope, so the seccomp filter is the only thing that can
// close them. The probes use a fixed key with no IPC_CREAT, so nothing is ever
// created: unsandboxed they return ENOENT, and the filter turns that into EPERM.
//
// Neuter check: drop the seccompBlockedSyscalls loop in socketFilterProgram,
// and shmget returns ENOENT instead of EPERM.
func TestSysVIPCAndMessageQueuesAreRefused(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	const probeKey = 0x6d6f6368 // "moch"; not IPC_PRIVATE, so no segment is made
	err := inSandbox(t, f.policy, func() error {
		if _, _, errno := unix.Syscall(unix.SYS_SHMGET, probeKey, 4096, 0); errno != unix.EPERM {
			return fmt.Errorf("shmget: want EPERM, got %v", errno)
		}
		if _, _, errno := unix.Syscall(unix.SYS_MSGGET, probeKey, 0, 0); errno != unix.EPERM {
			return fmt.Errorf("msgget: want EPERM, got %v", errno)
		}
		if _, _, errno := unix.Syscall(unix.SYS_SEMGET, probeKey, 0, 0); errno != unix.EPERM {
			return fmt.Errorf("semget: want EPERM, got %v", errno)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// OTHER PROCESSES ARE OUT OF REACH: their environment (where secrets live) is
// unreadable, and -- from ABI 6 -- they cannot be signalled.
//
// Neuter check: drop attr.Scoped in restrictLandlock, and the signal is
// delivered.
func TestOtherProcessesAreOutOfReach(t *testing.T) {
	abi := requireLandlock(t)
	f := newFixture(t)
	other := exec.Command("sleep", "30")
	other.Env = []string{"MOCHIII_TEST_SECRET=must-not-leak"}
	if err := other.Start(); err != nil {
		t.Skipf("NOT RUN: cannot start a process to reach for: %v", err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	pid := other.Process.Pid

	err := inSandbox(t, f.policy, func() error {
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
			return fmt.Errorf("read another process's environment: %q", data)
		}
		// process_vm_readv is the other way into a process's memory. Landlock's
		// ptrace hook denies it across the domain, the same as the environ read
		// above; the permission check fires before the address is touched, so a
		// fixed address is fine.
		localBuf := []byte{0}
		local := []unix.Iovec{{Base: &localBuf[0], Len: 1}}
		remote := []unix.RemoteIovec{{Base: 0x1000, Len: 1}}
		if n, err := unix.ProcessVMReadv(pid, local, remote, 0); err == nil {
			return fmt.Errorf("read another process's memory with process_vm_readv (%d bytes)", n)
		} else if !errors.Is(err, unix.EPERM) {
			return fmt.Errorf("process_vm_readv on an outside process: want EPERM, got %v", err)
		}
		if _, err := os.ReadFile("/proc/self/status"); err != nil {
			return fmt.Errorf("its own /proc entry, which runtimes need, is unreadable: %w", err)
		}
		if abi >= 6 {
			if err := unix.Kill(pid, 0); !errors.Is(err, unix.EPERM) {
				return fmt.Errorf("signalling a process outside the sandbox: want EPERM, got %v", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if abi < 6 {
		t.Logf("signal scoping NOT checked: ABI %d predates it (the prompt says so on such kernels)", abi)
	}
}

// THE SELF-CHECK CAN SAY NO. On an unrestricted thread every one of its three
// refusals is missing, and it must report that -- otherwise the probe would
// pass on a kernel that enforces nothing.
//
// Neuter check: make checkEnforced return nil, and this fails.
func TestTheSelfCheckFailsWhenNothingIsEnforced(t *testing.T) {
	f := newFixture(t)
	if err := checkEnforced(filepath.Join(f.outside, "secret")); err == nil {
		t.Fatal("checkEnforced passed on an unrestricted thread")
	}
	if err := checkEnforced(filepath.Join(f.outside, "no-such-file")); err == nil {
		t.Fatal("checkEnforced passed for a sentinel that does not exist, which proves nothing")
	}
	requireLandlock(t)
	err := inSandbox(t, f.policy, func() error { return checkEnforced(filepath.Join(f.outside, "secret")) })
	if err != nil {
		t.Fatalf("the self-check failed inside a real sandbox: %v", err)
	}
}

// THE PROBE, END TO END: this test binary really is re-executed as the helper,
// really restricts itself, and really passes its self-check.
func TestLandlockUsableRunsTheRealHelper(t *testing.T) {
	requireLandlock(t)
	if !LandlockUsable() {
		t.Fatal("Landlock is available on this kernel but the helper probe failed")
	}
}

// runHelper runs the real helper -- this test binary re-executed -- on argv
// under the fixture's policy, and returns its combined output.
func runHelper(t *testing.T, f fixture, env []string, argv ...string) (string, error) {
	t.Helper()
	bin, args, err := WrapCommand(argv[0], argv[1:], SandboxConfig{
		Mode: SandboxLandlock, WorkspaceRoot: f.workspace, HomeDir: f.home, AllowNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = f.workspace
	cmd.Env = append(ServerEnv(nil), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// THE RESTRICTIONS SURVIVE execve. The helper restricts its own thread and then
// becomes the command; this proves the command inherits all of it, which is
// the one thing the in-process tests above cannot show.
//
// Neuter check: skip applySandbox in sandboxExecMain, and the secret is printed.
func TestTheCommandTheHelperBecomesIsConfined(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	secret := filepath.Join(f.outside, "secret")
	script := fmt.Sprintf(`cat %q; echo "outside=$?"; cat main.go; echo "workspace=$?"; `+
		`echo x > %q; echo "plant=$?"; echo y > "$HOME/ok" && echo "home=0"`,
		secret, filepath.Join(f.outside, "planted"))

	out, err := runHelper(t, f, []string{"HOME=" + f.home}, "sh", "-c", script)
	if err != nil {
		t.Fatalf("the helper failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "contents of secret") {
		t.Fatalf("the command read a file outside the sandbox:\n%s", out)
	}
	for _, want := range []string{"outside=1", "contents of main.go", "workspace=0", "home=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in the command's output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "plant=0") {
		t.Errorf("the command planted a file outside the sandbox:\n%s", out)
	}
}

// USABLE, NOT MERELY STRICT: the Go toolchain builds and tests a module under
// the helper, with its temporary files in TMPDIR under HOME the way the
// handler arranges them.
func TestGoBuildsAndTestsUnderTheHelper(t *testing.T) {
	requireLandlock(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("NOT RUN: no go on PATH")
	}
	f := newFixture(t)
	for name, body := range map[string]string{
		"go.mod":       "module sandboxed\n\ngo 1.21\n",
		"main.go":      "package main\n\nfunc main() {}\n\nfunc double(n int) int { return 2 * n }\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n\tif double(2) != 4 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(f.workspace, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(f.home, ".tmp", "call-test")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + f.home, "TMPDIR=" + tmp, "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod",
		"PATH=" + filepath.Dir(goBin) + ":/usr/bin:/bin"}
	for _, argv := range [][]string{{"go", "build", "./..."}, {"go", "test", "./..."}} {
		out, err := runHelper(t, f, env, argv...)
		if err != nil {
			t.Fatalf("%s failed under the helper: %v\n%s", strings.Join(argv, " "), err, out)
		}
		t.Logf("%s: %s", strings.Join(argv, " "), strings.TrimSpace(out))
	}
}

// A HELPER THAT CANNOT BUILD ITS SANDBOX RUNS NOTHING. It must fail closed --
// never fall back to running the command unconfined.
func TestAHelperThatCannotSandboxRunsNothing(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"--rx", "relative", "--", "true"}, exitSandboxSetup},
		{[]string{"--rx", "/usr", "--", "no-such-command-anywhere"}, exitSandboxNotFound},
	} {
		if got := sandboxExecMain(c.args); got != c.want {
			t.Errorf("sandboxExecMain(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

// THE FILTER, INSTRUCTION BY INSTRUCTION, including the bypasses no Go test
// can make for real: an i386 syscall through int 0x80 (whose socketcall
// multiplexes socket(2)) and an x32 syscall on amd64. The program is run by a
// small interpreter against each seccomp_data the kernel would hand it.
//
// Neuter check: delete the arch comparison from socketFilterProgram, and the
// foreign-ABI rows are allowed.
func TestTheSocketFilterDecidesEveryCaseCorrectly(t *testing.T) {
	if !seccompSupported {
		t.Skipf("NOT RUN: no seccomp filter for %s", runtime.GOARCH)
	}
	prog, err := socketFilterProgram()
	if err != nil {
		t.Fatal(err)
	}
	const foreignArch = unix.AUDIT_ARCH_I386
	errno := func(e unix.Errno) uint32 { return unix.SECCOMP_RET_ERRNO | uint32(e) }
	cases := []struct {
		name       string
		arch, nr   uint32
		arg0, arg1 uint64
		want       uint32
	}{
		{"socket(AF_UNIX)", seccompAuditArch, uint32(unix.SYS_SOCKET), unix.AF_UNIX, unix.SOCK_STREAM, errno(unix.EACCES)},
		{"socket(AF_INET)", seccompAuditArch, uint32(unix.SYS_SOCKET), unix.AF_INET, unix.SOCK_STREAM, unix.SECCOMP_RET_ALLOW},
		{"socket(AF_UNIX) with high garbage", seccompAuditArch, uint32(unix.SYS_SOCKET), 0xffffffff00000000 | unix.AF_UNIX, 0, errno(unix.EACCES)},
		{"socketpair(stream|nonblock|cloexec)", seccompAuditArch, uint32(unix.SYS_SOCKETPAIR), unix.AF_UNIX, unix.SOCK_STREAM | unix.SOCK_NONBLOCK | unix.SOCK_CLOEXEC, unix.SECCOMP_RET_ALLOW},
		{"socketpair(dgram)", seccompAuditArch, uint32(unix.SYS_SOCKETPAIR), unix.AF_UNIX, unix.SOCK_DGRAM, errno(unix.EACCES)},
		{"socketpair(seqpacket)", seccompAuditArch, uint32(unix.SYS_SOCKETPAIR), unix.AF_UNIX, unix.SOCK_SEQPACKET, errno(unix.EACCES)},
		{"io_uring_setup", seccompAuditArch, uint32(unix.SYS_IO_URING_SETUP), 1, 0, errno(unix.ENOSYS)},
		{"shmget (SysV shared memory)", seccompAuditArch, uint32(unix.SYS_SHMGET), 0, 0, errno(unix.EPERM)},
		{"shmat (SysV shared memory)", seccompAuditArch, uint32(unix.SYS_SHMAT), 0, 0, errno(unix.EPERM)},
		{"semget (SysV semaphores)", seccompAuditArch, uint32(unix.SYS_SEMGET), 0, 0, errno(unix.EPERM)},
		{"msgget (SysV message queue)", seccompAuditArch, uint32(unix.SYS_MSGGET), 0, 0, errno(unix.EPERM)},
		{"mq_open (POSIX message queue)", seccompAuditArch, uint32(unix.SYS_MQ_OPEN), 0, 0, errno(unix.EPERM)},
		{"read", seccompAuditArch, uint32(unix.SYS_READ), 0, 0, unix.SECCOMP_RET_ALLOW},
		{"a foreign ABI (i386 socketcall)", foreignArch, 102, 1, 0, errno(unix.EPERM)},
		{"a foreign ABI, any syscall", foreignArch, 3, 0, 0, errno(unix.EPERM)},
	}
	if seccompX32Possible {
		cases = append(cases, struct {
			name       string
			arch, nr   uint32
			arg0, arg1 uint64
			want       uint32
		}{"x32 socket", seccompAuditArch, x32SyscallBit | uint32(unix.SYS_SOCKET), unix.AF_UNIX, 0, errno(unix.EPERM)})
	}
	for _, c := range cases {
		got, err := runBPF(prog, c.arch, c.nr, c.arg0, c.arg1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: filter returned %#x, want %#x", c.name, got, c.want)
		}
	}
}

// runBPF interprets the subset of classic BPF the filter uses, over a
// seccomp_data built from the arguments.
func runBPF(prog []unix.SockFilter, arch, nr uint32, arg0, arg1 uint64) (uint32, error) {
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data[seccompDataNr:], nr)
	binary.LittleEndian.PutUint32(data[seccompDataArch:], arch)
	binary.LittleEndian.PutUint64(data[seccompDataArg0:], arg0)
	binary.LittleEndian.PutUint64(data[seccompDataArg1:], arg1)
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			acc = binary.LittleEndian.Uint32(data[in.K:])
		case unix.BPF_ALU | unix.BPF_AND | unix.BPF_K:
			acc &= in.K
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if acc == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K:
			if acc >= in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return in.K, nil
		default:
			return 0, fmt.Errorf("instruction %d: opcode %#x not interpreted", pc, in.Code)
		}
	}
	return 0, errors.New("the program ran off its end without returning")
}

// The assembler refuses what classic BPF cannot express, rather than emitting
// a jump that lands somewhere else.
func TestAssembleBPFRefusesBadJumps(t *testing.T) {
	if _, err := assembleBPF([]bpfStep{{code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, jt: "nowhere"}}); err == nil {
		t.Error("a jump to a missing label assembled")
	}
	backward := []bpfStep{
		{label: "top", code: unix.BPF_RET | unix.BPF_K},
		{code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, jt: "top"},
	}
	if _, err := assembleBPF(backward); err == nil {
		t.Error("a backward jump assembled; classic BPF only jumps forward")
	}
}
