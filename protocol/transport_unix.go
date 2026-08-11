//go:build unix

package protocol

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// The Unix-domain-socket backend. This is the original behaviour, moved behind
// the seam unchanged, and it is the reference the named-pipe backend had to
// reproduce.

// defaultAddress is ALWAYS the local transport. See transport.go's note on why
// $HOST no longer selects TCP.
func defaultAddress() Address { return defaultAddressFor("") }

func defaultAddressFor(realRoot string) Address {
	return Address{Transport: TransportUnix, Address: SocketPathFor(realRoot)}
}

// listen binds the socket and immediately restricts it to the owner.
//
// The chmod lives HERE rather than at the call site because it is not optional
// and not portable: a Unix socket is created under the ambient umask, so a
// permissive one would leave a window in which the socket is world-connectable.
// (That window is the known L7 TOCTOU: it is narrowed by the 0700 runtime
// directory established by SocketDir and closed in practice by SO_PEERCRED,
// which refuses any peer that is not this uid regardless of the socket's mode.)
//
// The named-pipe backend has no equivalent step, which is exactly why this
// belongs behind the seam rather than in daemon/main.go where both platforms
// would have to reason about it.
func listen(a Address) (net.Listener, error) {
	if a.Transport != "" && a.Transport != TransportUnix && a.Transport != TransportTCP {
		return nil, fmt.Errorf("transport %q is not supported on this platform", a.Transport)
	}
	if a.Transport == TransportTCP {
		return net.Listen("tcp", a.Address)
	}
	if err := checkSocketPathLength(a.Address); err != nil {
		return nil, err
	}
	oldMask := syscall.Umask(0177)
	ln, err := net.Listen("unix", a.Address)
	syscall.Umask(oldMask)

	if err != nil {
		return nil, err
	}
	if err := os.Chmod(a.Address, 0600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restricting socket permissions: %w", err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		return bufferedListener{ul}, nil
	}
	return ln, nil
}

// bufferedListener applies socketBufferBytes to every accepted connection.
//
// PER CONNECTION, not once on the listening socket, because whether an accepted
// socket inherits its listener's buffer sizes is exactly the kind of
// per-kernel detail this constant exists to stop depending on.
//
// Accept still hands back the real *net.UnixConn: daemon/peercred_*.go reaches
// through it for SO_PEERCRED and LOCAL_PEERCRED, and wrapping the CONNECTION
// would break peer authentication to fix a buffer size.
type bufferedListener struct{ *net.UnixListener }

func (l bufferedListener) Accept() (net.Conn, error) {
	c, err := l.UnixListener.Accept()
	if err != nil {
		return nil, err
	}
	if uc, ok := c.(*net.UnixConn); ok {
		setSocketBuffers(uc)
	}
	return c, nil
}

// setSocketBuffers raises a Unix socket's send and receive buffers to
// socketBufferBytes.
//
// BOTH directions, on BOTH ends, because which one governs a blocking write is
// per-kernel: Linux bounds unix_stream_sendmsg by the SENDER's sk_sndbuf, while
// the BSDs append to the RECEIVER's socket buffer and bound it by that. Setting
// one pair would fix one platform and leave the other exactly as it was.
//
// BEST EFFORT, and deliberately so: a kernel that refuses the size, or a
// hardened environment that caps it, must not turn a working connection into a
// failed dial over a performance property. That leaves no error to report --
// which is precisely why TestTransport_BuffersAWriteThePeerHasNotRead exists and
// runs on all three platforms in CI. The test is the tripwire, not a log line.
func setSocketBuffers(c syscall.Conn) {
	raw, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, socketBufferBytes)
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, socketBufferBytes)
	})
}

func dial(a Address, timeout time.Duration) (net.Conn, error) {
	if a.Transport != "" && a.Transport != TransportUnix && a.Transport != TransportTCP {
		return nil, fmt.Errorf("transport %q is not supported on this platform", a.Transport)
	}
	network := "unix"
	if a.Transport == TransportTCP {
		network = "tcp"
	}
	var c net.Conn
	var err error
	if timeout > 0 {
		c, err = net.DialTimeout(network, a.Address, timeout)
	} else {
		c, err = net.Dial(network, a.Address)
	}
	if err != nil {
		return nil, err
	}
	// The client end of the same contract. A daemon writing a long answer is
	// bounded by whichever end's buffer the kernel consults, so both are set.
	if uc, ok := c.(*net.UnixConn); ok {
		setSocketBuffers(uc)
	}
	return c, nil
}

// maxUnixSocketPath is the shortest sun_path any supported platform offers.
//
// sockaddr_un.sun_path is a FIXED-SIZE array, not a pointer: 108 bytes on Linux
// and 104 on macOS/BSD, each including the terminating NUL. 103 is therefore the
// portable budget, and using the smaller number on Linux too means a path that
// works here works on a Mac rather than failing only there.
const maxUnixSocketPath = 103

// checkSocketPathLength turns the kernel's worst error message into an
// actionable one.
//
// FOUND BY A TEST OF MY OWN CHANGE. Per-workspace socket names added 17 bytes
// (`daemon-` + 16 hex + the same `.sock`), and a runtime directory deep enough
// to have fitted the old name no longer fitted the new one. bind(2) answers
// EINVAL for this -- surfacing as:
//
//	listen unix /very/long/.../daemon-ed76c217f533a101.sock: bind: invalid argument
//
// "invalid argument" about a path that plainly exists is close to unactionable,
// and the length is nowhere in it. Real deployments are nowhere near the limit
// ($XDG_RUNTIME_DIR is typically /run/user/1000, giving ~55 bytes), but the
// fallback when it is unset is os.TempDir(), and a sandbox or CI runner can put
// that somewhere long.
//
// Checked BEFORE net.Listen so the message is ours, and stated in bytes rather
// than characters because that is what the kernel counts.
func checkSocketPathLength(path string) error {
	if len(path) <= maxUnixSocketPath {
		return nil
	}
	return fmt.Errorf(
		"socket path %s is %d bytes, over the %d-byte limit a unix socket address allows "+
			"(sockaddr_un.sun_path is a fixed array); set XDG_RUNTIME_DIR to a shorter directory",
		path, len(path), maxUnixSocketPath)
}
