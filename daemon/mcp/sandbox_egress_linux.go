//go:build linux

package mcp

import (
	"encoding/binary"
	"net/netip"

	"golang.org/x/sys/unix"
)

// THE EGRESS FIREWALL: block a network-allowed build from reaching the cloud
// metadata endpoint (169.254.169.254 and its kin), which the disclosed-but-
// unenforced residual F5 left open.
//
// Landlock cannot (its network control is by port, not address), systemd cannot
// (IPAddressDeny needs CAP_NET_ADMIN the user manager lacks), and a netns needs
// the user namespaces this host blocks. What CAN, unprivileged and namespace-
// free: seccomp user-notification. A filter traps connect(2) to a listener the
// daemon holds; the daemon reads the destination out of the target and either
// DENIES (metadata) or performs the connect ON THE TARGET'S OWN SOCKET and hands
// the result back. It never uses SECCOMP_USER_NOTIF_FLAG_CONTINUE, so a
// multithreaded build cannot swap the address after the check -- there is no
// TOCTOU window. The seccomp-notify half is proven in the Phase-B spike.
//
// This file is the pure, testable core: the constants, the notification structs,
// and the address decision. The syscalls and the supervisor loop are in
// sandbox_egress_supervisor_linux.go.

// seccomp(2) and the notification ioctls, computed by hand because
// golang.org/x/sys/unix v0.10.0 predates them. The ioctl numbers are the
// asm-generic encoding _IOC(dir,type,nr,size) = dir<<30 | size<<16 | type<<8 | nr,
// with type '!' (0x21): _IOWR('!',0,seccomp_notif[80]), _IOWR('!',1,resp[24]),
// _IOR('!',2,u64[8]). Verified live in the spike (RECV/SEND/deny all worked).
const (
	seccompSetModeFilter         = 1
	seccompFilterFlagNewListener = 1 << 3 // NOT 1<<4 (that is TSYNC_ESRCH) -- the spike's first bug
	seccompRetUserNotif          = 0x7fc00000

	seccompIoctlNotifRecv    = 0xc0502100
	seccompIoctlNotifSend    = 0xc0182101
	seccompIoctlNotifIDValid = 0x40082102
)

// seccompData mirrors the kernel struct the notification carries: the trapped
// syscall's number, the audit arch, and its six register arguments.
type seccompData struct {
	nr   int32
	arch uint32
	ip   uint64
	args [6]uint64
}

// seccompNotif is one pending trapped syscall: a unique id (for the response and
// the validity check), the target pid, and the syscall's data.
type seccompNotif struct {
	id    uint64
	pid   uint32
	flags uint32
	data  seccompData
}

// seccompNotifResp is the supervisor's answer: for a terminated syscall, val is
// its return and errno its negative error; flags is 0 (never CONTINUE here).
type seccompNotifResp struct {
	id    uint64
	val   int64
	errno int32
	flags uint32
}

// egressDenyNets are the destinations an egress-filtered command may not reach.
// Loopback is DELIBERATELY absent: blocking it breaks legitimate local-service
// tests and is not the SSRF-to-credentials threat; a caller that wants it adds it
// (loopbackDenyNets). Everything here is link-local or a cloud metadata ULA,
// which a build never has a legitimate reason to reach.
var egressDenyNets = mustPrefixes(
	"169.254.0.0/16", // IPv4 link-local: AWS/GCP/Azure metadata (169.254.169.254)
	"fe80::/10",      // IPv6 link-local
	"fd00:ec2::/32",  // AWS IMDSv6 ULA (fd00:ec2::254)
)

// loopbackDenyNets is added to the deny set only when a caller opts in, because
// unlike the metadata ranges, loopback has legitimate build/test uses.
var loopbackDenyNets = mustPrefixes("127.0.0.0/8", "::1/128")

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			panic("egress: bad CIDR " + c + ": " + err.Error())
		}
		out = append(out, p.Masked())
	}
	return out
}

// destFromSockaddr decodes a connect(2) sockaddr, as read from the target's
// memory, into an address. ok is false for a family this filter does not judge
// (AF_UNIX, and anything short or unknown) -- those are allowed through, since
// the Landlock/seccomp layer already governs Unix sockets and the threat here is
// only IP egress.
func destFromSockaddr(b []byte) (netip.Addr, bool) {
	if len(b) < 2 {
		return netip.Addr{}, false
	}
	switch binary.LittleEndian.Uint16(b[0:2]) { // sa_family is host byte order
	case unix.AF_INET:
		if len(b) < 8 {
			return netip.Addr{}, false
		}
		return netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), true
	case unix.AF_INET6:
		if len(b) < 24 {
			return netip.Addr{}, false
		}
		var a [16]byte
		copy(a[:], b[8:24])
		return netip.AddrFrom16(a), true
	default:
		return netip.Addr{}, false
	}
}

// egressDenied reports whether a decoded destination falls in a denied range.
// A v4-mapped v6 address (::ffff:169.254.169.254) is unmapped first, so the
// metadata range cannot be reached by dressing it up as IPv6.
func egressDenied(a netip.Addr, nets []netip.Prefix) bool {
	a = a.Unmap()
	for _, n := range nets {
		if a.Is4() == n.Addr().Is4() && n.Contains(a) {
			return true
		}
	}
	return false
}
