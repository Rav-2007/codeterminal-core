package protocol

import (
	"net"
	"time"
)

// The transport seam.
//
// This package already owns the DISCOVERY convention — where the socket and
// lockfile live — precisely so the daemon and every client derive identical
// paths and cannot drift. It owns the transport for the same reason: what
// "connect to the daemon" means differs by platform, and that difference must
// be stated once.
//
// WHY NAMED PIPES ON WINDOWS, AND WHY THAT IS NOT A PREFERENCE. Go supports
// AF_UNIX on Windows, so a Go-only port could have kept Unix sockets
// everywhere. Node does not: libuv's net.connect interprets its path option on
// Windows as a named pipe and nothing else. The VS Code extension is a Node
// client, so the transport is forced by the TypeScript half rather than chosen
// by the Go half.
//
// THE SEAM IS THE LOCKFILE, NOT THE DIAL CALL. Every client already reads the
// lockfile to find the daemon. Putting the address there — transport and all —
// means a client dials whatever it was told rather than deriving it, so
// clients/vscode/src/daemonClient.ts needs no platform branch at all: it passes
// one string to net.createConnection either way. That is the payoff, and it is
// why Address travels in LockFile rather than being recomputed per client.

// Transport names how to reach the daemon.
const (
	// TransportUnix is a Unix domain socket. Address is a filesystem path.
	TransportUnix = "unix"
	// TransportTCP is a TCP socket. Address is a host:port.
	//
	// NOT REACHABLE IN PRODUCTION, AND DELIBERATELY SO. Nothing returns it from
	// DefaultAddress; it survives as a type for tests and for any future,
	// explicitly-designed remote mode.
	//
	// It used to be selected whenever $HOST was set, which was wrong twice
	// over. First, $HOST is set for unrelated reasons all over the place —
	// containers, CI runners, PaaS platforms, tcsh — so a daemon documented as
	// "never listens on a network port" (daemon/main.go) would silently bind
	// one. Second, it could not work anyway: authorizePeer runs on every
	// accepted connection and fails closed, and SO_PEERCRED on a TCP socket
	// reports uid 4294967295 (UID_INVALID), which never matches a real uid.
	// Measured on Linux — every TCP client was refused before its bearer token
	// was even read.
	//
	// So the observable effect of setting $HOST was a daemon that bound a port,
	// wrote a token file, and then rejected every client with a peer-uid
	// mismatch. An availability failure wearing a feature's clothes.
	//
	// Reviving it means designing a remote story on purpose: a bind address
	// that defaults to loopback, a constant-time token comparison, token
	// rotation, and a transport-level answer to everything peer auth was doing.
	// That is a project, not a flag.
	TransportTCP = "tcp"
	// TransportNamedPipe is a Windows named pipe. Address is a \\.\pipe\ name,
	// which is NOT a filesystem path: it has no directory, no permissions bits,
	// and leaves no residue when the owner dies.
	TransportNamedPipe = "npipe"
)

// Address is everything needed to reach a daemon.
//
// Kept as a struct rather than a bare string because the two fields answer
// different questions and a reader that guesses gets it wrong: "unix" implies a
// file that can be stat'd, chmod'd and unlinked, and "npipe" implies none of
// those. Code that special-cases stale-socket cleanup depends on knowing which.
type Address struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
}

// String renders an address for logs and error messages.
func (a Address) String() string {
	if a.Transport == "" || a.Transport == TransportUnix {
		return a.Address
	}
	return a.Transport + ":" + a.Address
}

// IsZero reports whether the address is unset, which is how a lockfile written
// by an older daemon reads.
func (a Address) IsZero() bool { return a.Address == "" }

// Listen binds a listener at addr. Callers should use DefaultAddress rather
// than constructing one, so that both sides of the connection agree.
func Listen(a Address) (net.Listener, error) { return listen(a) }

// Dial connects to a daemon at addr.
func Dial(a Address) (net.Conn, error) { return dial(a, 0) }

// DialTimeout connects to a daemon at addr, giving up after d.
func DialTimeout(a Address, d time.Duration) (net.Conn, error) { return dial(a, d) }

// DefaultAddress is the address this build's daemon listens on and every client
// of it dials. It is derived, not configured, so all four callers agree without
// coordinating.
//
// PER USER. Prefer DefaultAddressFor: this one cannot distinguish two
// workspaces, which is the defect daemon/twoworkspaces_test.go reproduces.
func DefaultAddress() Address { return defaultAddress() }

// DefaultAddressFor is DefaultAddress scoped to one workspace, so two
// workspaces get two daemons instead of the second failing to start.
//
// realRoot must be the CANONICAL root — the same one the daemon grounds
// against, resolved through symlinks. An empty string reproduces
// DefaultAddress exactly, which is what a client with no workspace to offer
// gets. See WorkspaceTag for why canonicalisation is the caller's job.
func DefaultAddressFor(realRoot string) Address { return defaultAddressFor(realRoot) }

// AddressFromLock resolves the address a client should dial from a lockfile.
//
// It prefers the explicit Address, and falls back to the legacy SocketPath so a
// lockfile written by an older daemon on the same machine still works. The
// fallback is Unix-only by construction: SocketPath never carried a pipe name.
func AddressFromLock(l LockFile) Address {
	if !l.Address.IsZero() {
		return l.Address
	}
	return Address{Transport: TransportUnix, Address: l.SocketPath}
}

// NewLockFile builds the lockfile document for a daemon listening at addr.
//
// SocketPath is populated only for a Unix socket, where it is the same string
// Address carries: it exists so a client built before Address did keeps working
// against a newer daemon on the same machine. A named pipe has no path, so the
// field stays empty and an old client fails to find the daemon — which is the
// honest outcome, since an old client could not have dialled a pipe anyway.
func NewLockFile(addr Address, pid int) LockFile {
	l := LockFile{PID: pid, Address: addr}
	if addr.Transport == TransportUnix {
		l.SocketPath = addr.Address
	}
	return l
}

// socketBufferBytes is how much unread data this transport must be able to hold
// for a peer that has stopped reading, on EVERY platform.
//
// THE PROPERTY, and why it is a number rather than a default. A client that
// stops reading -- crashed, suspended, or merely slow -- leaves the daemon's
// handler goroutine parked in a write it can never finish. Serve's concurrency
// semaphore is finite, so enough such clients stop the daemon accepting at all,
// and WaitForDrain can never complete because inFlight never falls to zero.
// That is an availability failure, and it is decided entirely by a buffer size.
//
// It was declared on exactly one platform. The Windows named pipe sets it
// explicitly because a pipe created with a zero quota has NO buffer and every
// write blocks -- that default cost three unrelated-looking symptoms on the
// first Windows CI run. Unix inherited whatever the kernel happened to pick,
// and the test pinning this property said "a Unix socket carries ~200 KiB",
// which is a LINUX figure written down as a Unix one.
//
// macOS is not Linux here. Its default for a unix stream socket is
// net.local.stream.sendspace = 8 KiB, so a 32 KiB response blocked, and the
// first macOS run this repository has ever had reported exactly that. Same
// shape as sun_path being 108 bytes on Linux and 104 on macOS: a per-kernel
// constant treated as portable because only one kernel had ever run it.
//
// 64 KiB is the number the Windows backend already had to name, so it is the
// contract, and both other platforms are now held to it instead of to their
// defaults.
const socketBufferBytes = 64 * 1024
