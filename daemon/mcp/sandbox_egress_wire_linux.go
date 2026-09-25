//go:build linux

package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The wire between the sandboxed helper and the daemon's supervisor: the helper
// hands the listener fd back over an inherited socketpair (SCM_RIGHTS), because
// the fd is born inside the sandbox but must be owned OUTSIDE it. The command the
// helper becomes must never hold it -- a seccomp listener in the target's hands
// would let it answer its own connects -- so the helper marks it close-on-exec
// after sending the copy, and the daemon's copy is the only one that survives.

// sendListenerFD passes fd to the daemon over the inherited socketpair. One dummy
// byte carries the ancillary rights; the fd number is meaningless across the
// boundary, only the open file it names travels.
func sendListenerFD(sock, fd int) error {
	return unix.Sendmsg(sock, []byte{0}, unix.UnixRights(fd), nil, 0)
}

// RecvListenerFD receives the seccomp listener the helper sent. It blocks until
// the helper sends (early, before it execs the command) or the socket closes.
func RecvListenerFD(sock int) (int, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4)) // one fd
	_, oobn, _, _, err := unix.Recvmsg(sock, buf, oob, 0)
	if err != nil {
		return -1, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return -1, fmt.Errorf("egress: no control message with the listener fd: %v", err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) == 0 {
		return -1, fmt.Errorf("egress: no listener fd in the control message: %v", err)
	}
	return fds[0], nil
}

// egressDenySet is the deny list a supervisor enforces: the metadata/link-local
// ranges always, and loopback only when asked.
func egressDenySet(denyLoopback bool) []netip.Prefix {
	nets := append([]netip.Prefix{}, egressDenyNets...)
	if denyLoopback {
		nets = append(nets, loopbackDenyNets...)
	}
	return nets
}

// RunEgressSupervisor owns listener until ctx ends or the fd closes: it receives
// every trapped connect and answers it (deny the metadata ranges, connect the
// rest on the target's own socket). It returns when the last target has exited
// and the listener is closed, or the context is cancelled.
func RunEgressSupervisor(ctx context.Context, listener int, denyLoopback bool) {
	sup := &egressSupervisor{listener: listener, deny: egressDenySet(denyLoopback)}
	done := make(chan struct{})
	go func() {
		sup.run()
		close(done)
	}()
	select {
	case <-ctx.Done():
	case <-done:
	}
}

// egressSelfCheck is the helper's proof, run on the restricted thread once the
// listener and supervisor are wired: a connect to the denied target must be
// refused with EPERM, and one to the allowed target must succeed. The spec is
// "<denied host:port>,<allowed host:port>".
func egressSelfCheck(spec string) error {
	pair := strings.SplitN(spec, ",", 2)
	if len(pair) != 2 {
		return fmt.Errorf("bad self-check spec %q", spec)
	}
	if err := connectExpect(pair[0], true); err != nil {
		return fmt.Errorf("denied target %s: %w", pair[0], err)
	}
	if err := connectExpect(pair[1], false); err != nil {
		return fmt.Errorf("allowed target %s: %w", pair[1], err)
	}
	return nil
}

// connectExpect opens a TCP socket and connects to hostport, asserting the
// outcome the firewall should produce: EPERM when the destination is denied,
// success otherwise. The connect is what the seccomp filter traps.
func connectExpect(hostport string, wantDenied bool) error {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Is4() {
		return fmt.Errorf("self-check needs an IPv4 literal, got %q", host)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	cerr := unix.Connect(fd, &unix.SockaddrInet4{Port: port, Addr: ip.As4()})
	if wantDenied {
		if !errors.Is(cerr, unix.EPERM) {
			return fmt.Errorf("want EPERM, got %v", cerr)
		}
		return nil
	}
	if cerr != nil {
		return fmt.Errorf("want success, got %v", cerr)
	}
	return nil
}

// EgressFilterUsable reports whether the egress firewall will actually ENFORCE
// here, by running the real helper end to end once: the helper installs the
// listener, hands it back, grants ptrace, restricts itself, and then -- proving
// the whole chain including the cross-process reads the supervisor depends on --
// finds a connect to the metadata endpoint refused and a connect to a loopback
// listener allowed. Presence is not capability, the lesson this file's neighbours
// each learned; only a real trapped-and-answered connect is proof.
//
// It rides on LandlockUsable: the egress helper IS the landlock helper with one
// more filter, so a host that cannot enforce Landlock cannot enforce this either.
var EgressFilterUsable = sync.OnceValue(landlockEgressUsable)

// landlockEgressUsable is EgressFilterUsable's body, named so its refusals can be
// tested: a OnceValue answers once per process, so a test can never watch it say
// no to a host that cannot enforce.
func landlockEgressUsable() bool {
	if !LandlockUsable() {
		return false
	}
	self := selfExecutable()
	if self == "" {
		return false
	}
	if _, err := lookPath("true"); err != nil {
		return false
	}
	dir, err := os.MkdirTemp("", "mochiii-egress-probe-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	workspace := filepath.Join(dir, "workspace")
	if os.Mkdir(workspace, 0o700) != nil {
		return false
	}
	return egressProbe(func(spec string) (string, []string) {
		policy := landlockPolicyFor("", SandboxConfig{WorkspaceRoot: workspace})
		args := append([]string{SandboxHelperArg}, policy.args()...)
		args = append(args, egressFdFlag, "3", daemonPidFlag, strconv.Itoa(os.Getpid()),
			egressSelfCheckFlag, spec, landlockArgsSeparator, "true")
		return self, args
	})
}

// BwrapEgressUsable is the same proof for the bubblewrap backend, and it is a
// separate probe rather than a flag on the other one because the two rest on
// different mechanisms. Under bwrap the trapped process sits inside a PID and
// user namespace, and the supervisor has to reach across both: the kernel must
// report the notification's pid in the SUPERVISOR's namespace, and
// process_vm_readv and pidfd_getfd must still be permitted -- by the daemon being
// an ancestor of the tree, since PR_SET_PTRACER cannot name a pid that does not
// exist inside the namespace. Running the real helper inside real bwrap is the
// only thing that proves all of that, so that is what this does.
//
// It deliberately does NOT call LandlockUsable: a bwrap host need not have
// Landlock, and requiring it is exactly what kept this block Landlock-only.
var BwrapEgressUsable = sync.OnceValue(bwrapEgressUsable)

// bwrapEgressUsable is BwrapEgressUsable's body; see landlockEgressUsable.
func bwrapEgressUsable() bool {
	if !BwrapUsable() {
		return false
	}
	self := selfExecutable()
	if self == "" {
		return false
	}
	if _, err := lookPath("bwrap"); err != nil {
		return false
	}
	return egressProbe(func(spec string) (string, []string) {
		args := bwrapBaseArgs()
		for _, m := range sandboxSystemPaths {
			if _, err := os.Stat(m); err == nil {
				args = append(args, "--ro-bind", m, m)
			}
		}
		// Last, so nothing shadows it -- the same ordering the real invocation uses.
		args = append(args, "--ro-bind", self, self, "--chdir", "/", "--")
		args = append(args, self, SandboxHelperArg, egressOnlyFlag, egressFdFlag, "3",
			egressSelfCheckFlag, spec, landlockArgsSeparator, "true")
		return "bwrap", args
	})
}

// egressProbe is the scaffolding both probes share: a real loopback listener as
// the ALLOWED destination, a socketpair for the listener handoff, a real
// supervisor, and one real helper invocation that must find the metadata endpoint
// refused and the loopback listener reachable. build returns the binary and argv
// to run, given the self-check spec.
//
// Presence is not capability, the lesson every neighbour of this file learned the
// hard way; only a connect that was really trapped and really answered is proof.
func egressProbe(build func(spec string) (string, []string)) bool {
	// An allowed destination: a real loopback listener (loopback is not denied).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	daemonEnd := os.NewFile(uintptr(pair[0]), "egress-daemon")
	helperEnd := os.NewFile(uintptr(pair[1]), "egress-helper")
	defer func() { _ = daemonEnd.Close() }()

	bin, args := build(fmt.Sprintf("169.254.169.254:80,127.0.0.1:%d", port))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, bin, args...)
	probe.Env = ServerEnv(nil)
	probe.ExtraFiles = []*os.File{helperEnd} // becomes fd 3 in the helper

	if err := probe.Start(); err != nil {
		_ = helperEnd.Close()
		return false
	}
	_ = helperEnd.Close() // the helper has its own copy

	supCtx, supCancel := context.WithCancel(context.Background())
	defer supCancel()
	dfd := int(daemonEnd.Fd())
	go func() {
		lfd, err := RecvListenerFD(dfd)
		if err != nil {
			return
		}
		defer func() { _ = unix.Close(lfd) }()
		RunEgressSupervisor(supCtx, lfd, false)
	}()

	return probe.Wait() == nil
}
