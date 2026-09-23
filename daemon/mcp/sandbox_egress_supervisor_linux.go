//go:build linux

package mcp

import (
	"errors"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The seccomp-notify SUPERVISOR: the daemon side of the egress firewall. It owns
// the listener fd the helper handed back, receives every trapped connect(2), and
// decides. It NEVER answers CONTINUE -- a denied destination is refused outright,
// and an allowed one is connected on the target's OWN socket by this supervisor,
// so the address the kernel acts on is the one this code validated, not one a
// racing thread could swap in afterwards. That is what makes the block
// TOCTOU-safe rather than best-effort.

// installEgressListener installs the combined socket+connect filter on the
// calling (already no_new_privs, locked) thread via seccomp(2) with a new
// listener, and returns the listener fd. Unlike installSocketFilter's
// PR_SET_SECCOMP, this form can carry a SECCOMP_RET_USER_NOTIF verdict and hands
// back the fd the supervisor listens on.
func installEgressListener() (int, error) {
	filter, err := socketFilterProgram(true)
	if err != nil {
		return -1, err
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP, seccompSetModeFilter,
		seccompFilterFlagNewListener, uintptr(unsafe.Pointer(&prog)))
	runtime.KeepAlive(filter)
	if errno != 0 {
		return -1, errno
	}
	if int(fd) < 0 {
		return -1, errors.New("seccomp new listener returned a bad fd")
	}
	return int(fd), nil
}

// notifRecv blocks for the next trapped syscall on the listener.
func notifRecv(listener int) (seccompNotif, error) {
	var n seccompNotif
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(listener),
			seccompIoctlNotifRecv, uintptr(unsafe.Pointer(&n)))
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return n, errno
		}
		return n, nil
	}
}

// notifSend answers one trapped syscall: error<0 makes the target's syscall
// return that errno, error==0 makes it return val. flags is always 0 -- there is
// no CONTINUE on this path.
func notifSend(listener int, id uint64, val int64, errno int32) error {
	resp := seccompNotifResp{id: id, val: val, errno: errno}
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(listener),
		seccompIoctlNotifSend, uintptr(unsafe.Pointer(&resp)))
	if e != 0 {
		return e
	}
	return nil
}

// notifIDValid reports whether the notification is still live -- the target has
// not died or been replaced by a reused pid -- so reading its memory and acting
// on its behalf is safe.
func notifIDValid(listener int, id uint64) bool {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(listener),
		seccompIoctlNotifIDValid, uintptr(unsafe.Pointer(&id)))
	return errno == 0
}

// egressSupervisor holds a listener and the deny set it enforces.
type egressSupervisor struct {
	listener int
	deny     []netip.Prefix
}

// run receives and answers trapped connects until the listener closes (the last
// target exited and the fd was closed) or an unrecoverable recv error. It hands
// each notification to a goroutine so a blocking connect-on-behalf never stalls
// another target.
func (s *egressSupervisor) run() {
	for {
		n, err := notifRecv(s.listener)
		if err != nil {
			return // ENOTCONN/EBADF on close, or the target set is gone
		}
		go s.handle(n)
	}
}

// egressRefused is the WHOLE security decision for one trapped connect, kept as a
// pure function so every branch of it is tested exhaustively and none of it
// depends on a live kernel. It fails CLOSED: anything this cannot judge is
// refused. The one deliberate exception is a family it does not decode (not
// AF_INET/AF_INET6): Unix sockets are already governed by the seccomp and
// Landlock layers, which never let the command create one, so there is nothing
// here to add and refusing would break callers this filter has no opinion about.
func egressRefused(nr int32, addrLen uint64, raw []byte, readOK bool, deny []netip.Prefix) bool {
	if nr != int32(unix.SYS_CONNECT) {
		return true // the filter should never route anything else here
	}
	if addrLen == 0 || addrLen > 128 {
		return true // not a sockaddr this can read
	}
	if !readOK {
		return true // could not read the destination out of the target
	}
	addr, ok := destFromSockaddr(raw)
	return ok && egressDenied(addr, deny)
}

// handle answers one trapped connect: read the destination out of the target,
// put it to egressRefused, and either deny or perform the connect on the
// target's own socket.
func (s *egressSupervisor) handle(n seccompNotif) {
	addrLen := n.data.args[2]
	var raw []byte
	readOK := false
	if n.data.nr == int32(unix.SYS_CONNECT) && addrLen > 0 && addrLen <= 128 {
		// Checked before reading the target's memory: it guards against acting on
		// a notification whose process has died and whose pid may be reused.
		if !notifIDValid(s.listener, n.id) {
			return // target gone; nothing to answer
		}
		raw = make([]byte, addrLen)
		readOK = readTargetMem(int(n.pid), n.data.args[1], raw) == nil
	}
	if egressRefused(n.data.nr, addrLen, raw, readOK, s.deny) {
		_ = notifSend(s.listener, n.id, 0, -int32(unix.EPERM))
		return
	}
	// Allowed: perform the connect on the TARGET'S OWN socket, so the kernel acts
	// on the validated address, and hand back the exact result.
	val, errno := connectOnBehalf(int(n.pid), int(n.data.args[0]), raw)
	_ = notifSend(s.listener, n.id, val, errno)
}

// connectOnBehalf grabs the target's socket (pidfd_getfd, which needs the ptrace
// access the helper granted with PR_SET_PTRACER) and connects it to addr from
// here. The target's fd shares the open file, so its own connect state is what we
// leave: a non-blocking socket returns EINPROGRESS immediately, a blocking one
// blocks in this goroutine until it resolves -- the semantics the target expects,
// since it never sees that we, not it, called connect.
func connectOnBehalf(pid, sockFd int, raw []byte) (val int64, errno int32) {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return 0, -int32(unix.EPERM) // fail closed
	}
	defer func() { _ = unix.Close(pidfd) }()
	local, err := unix.PidfdGetfd(pidfd, sockFd, 0)
	if err != nil {
		return 0, -int32(unix.EPERM)
	}
	defer func() { _ = unix.Close(local) }()

	_, _, e := unix.Syscall(unix.SYS_CONNECT, uintptr(local),
		uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)))
	if e == 0 {
		return 0, 0
	}
	return 0, -int32(e)
}

// readTargetMem reads len(buf) bytes from the target process's address space.
// process_vm_readv needs PTRACE_MODE_ATTACH access, which the helper granted the
// daemon via PR_SET_PTRACER before exec.
func readTargetMem(pid int, addr uint64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	local := []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}}
	remote := []unix.RemoteIovec{{Base: uintptr(addr), Len: len(buf)}}
	n, err := unix.ProcessVMReadv(pid, local, remote, 0)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return errors.New("short read of target sockaddr")
	}
	return nil
}
