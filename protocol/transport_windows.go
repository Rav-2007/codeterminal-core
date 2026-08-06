//go:build windows

package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// The named-pipe backend.
//
// NOT RUN ON HARDWARE. Compile-verified for windows/amd64 and reasoned against
// the Win32 documentation and go-winio's source; no Windows machine has
// executed it. Labelled the way peercred_darwin.go is, until CI has a Windows
// runner (launch plan Stage 2.3).
//
// A pipe is not a file, and three of the Unix backend's assumptions do not
// survive the move:
//
//   - There is no directory to place it in and no mode bits to chmod. Access
//     control is the pipe's own DACL, applied at creation, and it is enforced
//     by the object manager at open time — BEFORE any of this daemon's code
//     runs. That is stronger than the Unix arrangement, where the 0600 mode
//     gates open() and the SO_PEERCRED check gates use.
//   - The namespace is MACHINE-GLOBAL. Two users on one machine share it, so
//     the name carries a per-user discriminator or they collide.
//   - A dead daemon leaves NO residue. There is no stale pipe file to reclaim,
//     which is why reclaimStaleSocket's unlink half is Unix-only.

// pipePrefix is the Windows named-pipe namespace. Not a filesystem path: it has
// no directory and cannot be stat'd.
const pipePrefix = `\\.\pipe\`

// defaultAddress is ALWAYS the local transport. See transport.go's note on why
// $HOST no longer selects TCP.
func defaultAddress() Address {
	return Address{Transport: TransportNamedPipe, Address: pipeName()}
}

// pipeName derives this user's pipe name.
//
// The discriminator is a hash of the user's SID rather than a username: the
// name only has to be collision-free and stable, and a SID is both, where a
// display name is neither. It is NOT a secret and is not treated as one —
// clients read the name out of the per-user lockfile, and the security property
// comes from the DACL below, not from the name being hard to guess.
//
// A daemon that cannot determine its own SID still gets a working, if
// unqualified, name: failing to start over a naming detail would be a worse
// outcome than a name that collides only in the multi-user case, and the DACL
// still refuses the other user.
func pipeName() string {
	suffix := "default"
	if sid, err := currentUserSID(); err == nil {
		sum := sha256.Sum256([]byte(sid))
		suffix = hex.EncodeToString(sum[:])[:16]
	}
	return pipePrefix + serviceDirName + "-" + suffix
}

// currentUserSID returns this process's user SID in string form.
//
// The pipe DACL wants this and not socketdir_windows.go's currentTokenOwnerSID,
// and the asymmetry is deliberate. Granting the token OWNER would grant
// BUILTIN\Administrators on an elevated token — every administrator on the
// machine, where the point of this ACE is one individual. Ownership of a
// directory we created and access to a pipe we serve are different questions.
func currentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// ownerOnlySDDL builds the pipe's security descriptor: this user, full control,
// and nobody else.
//
// "D:P" is a PROTECTED dacl — it blocks inherited ACEs, so nothing from a
// parent object can widen it. The single ACE grants GA (GENERIC_ALL) to the
// owning SID. Administrators and SYSTEM can still take ownership, exactly as
// root can on Unix; that is the same boundary, not a weaker one.
//
// This is the load-bearing access control on Windows. It is applied by the
// object manager at CreateFile time, so a process belonging to another user
// cannot open the pipe at all — it never reaches the daemon's own peer check.
func ownerOnlySDDL() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", fmt.Errorf("determining this user's SID for the pipe's access control: %w", err)
	}
	return "D:P(A;;GA;;;" + sid + ")", nil
}

// listen creates the pipe.
//
// SQUATTING, AND WHY THIS IS ALREADY SAFE. The \\.\pipe\ namespace is
// unprivileged and machine-global: any user can create a pipe by any name. If
// creation merely ADDED an instance to somebody else's existing pipe, this
// daemon's own clients would connect to that attacker. Verified in go-winio
// v0.6.2's source rather than assumed: makeServerPipeHandle calls
// NtCreateNamedPipeFile with disposition FILE_CREATE for the first instance
// (pipe.go:378-381), which is the NT-level "create new, fail if it exists" —
// the same guarantee Win32's FILE_FLAG_FIRST_PIPE_INSTANCE provides. A squatted
// name therefore fails loudly here instead of being silently joined.
//
// go-winio also sets FILE_PIPE_REJECT_REMOTE_CLIENTS, so the pipe cannot be
// reached over SMB from another machine. That matches the daemon's standing
// promise that it never listens on a network.
//
// This function pins both properties in a comment because they come from a
// dependency: if go-winio is ever upgraded, they are what to re-check.
func listen(a Address) (net.Listener, error) {
	if a.Transport != "" && a.Transport != TransportNamedPipe && a.Transport != TransportTCP {
		return nil, fmt.Errorf("transport %q is not supported on this platform", a.Transport)
	}
	if a.Transport == TransportTCP {
		return net.Listen("tcp", a.Address)
	}
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(a.Address, &winio.PipeConfig{SecurityDescriptor: sddl})
}

func dial(a Address, timeout time.Duration) (net.Conn, error) {
	if a.Transport != "" && a.Transport != TransportNamedPipe && a.Transport != TransportTCP {
		return nil, fmt.Errorf("transport %q is not supported on this platform", a.Transport)
	}
	if a.Transport == TransportTCP {
		if timeout > 0 {
			return net.DialTimeout("tcp", a.Address, timeout)
		}
		return net.Dial("tcp", a.Address)
	}
	if timeout > 0 {
		return winio.DialPipe(a.Address, &timeout)
	}
	return winio.DialPipe(a.Address, nil)
}
