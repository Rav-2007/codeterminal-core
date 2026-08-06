//go:build unix

package protocol

import (
	"fmt"
	"net"
	"os"
	"time"
)

// The Unix-domain-socket backend. This is the original behaviour, moved behind
// the seam unchanged, and it is the reference the named-pipe backend had to
// reproduce.

// defaultAddress is ALWAYS the local transport. See transport.go's note on why
// $HOST no longer selects TCP.
func defaultAddress() Address {
	return Address{Transport: TransportUnix, Address: SocketPath()}
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
	ln, err := net.Listen("unix", a.Address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(a.Address, 0600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restricting socket permissions: %w", err)
	}
	return ln, nil
}

func dial(a Address, timeout time.Duration) (net.Conn, error) {
	if a.Transport != "" && a.Transport != TransportUnix && a.Transport != TransportTCP {
		return nil, fmt.Errorf("transport %q is not supported on this platform", a.Transport)
	}
	network := "unix"
	if a.Transport == TransportTCP {
		network = "tcp"
	}
	if timeout > 0 {
		return net.DialTimeout(network, a.Address, timeout)
	}
	return net.Dial(network, a.Address)
}
