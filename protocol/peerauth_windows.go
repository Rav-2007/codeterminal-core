//go:build windows

package protocol

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Windows peer identity.
//
// NOT RUN ON HARDWARE. Compile-verified for windows/amd64 and reasoned against
// the Win32 documentation and go-winio's source; no Windows machine has
// executed it. Labelled the way peercred_darwin.go is, until CI has a Windows
// runner (launch plan Stage 2.3).
//
// TWO LAYERS, AND THE FIRST ONE IS THE LOAD-BEARING ONE.
//
// Layer 1 is the pipe's DACL (protocol/transport_windows.go): the pipe is
// created granting GENERIC_ALL to this user's SID and nobody else, and the
// object manager enforces that at CreateFile time. A process belonging to
// another user cannot open the pipe at all — it is refused BEFORE any byte
// reaches this daemon and before any code here runs. That is a stronger
// position than the Unix side, where the 0600 mode gates open() and the
// SO_PEERCRED check gates use.
//
// Layer 2 is this file: confirm, from the kernel, who actually connected.
//
// WHY NOT ImpersonateNamedPipeClient, WHICH IS THE OBVIOUS ANSWER. It does not
// work with this stack, and that was established by reading go-winio rather
// than by trying it: every DialPipe* entry point connects at
// PipeImpLevelAnonymous (pipe.go:272-280, and its doc comment says so). A
// server that impersonates an anonymous client gets an anonymous token and no
// usable SID — so an impersonation-based check would refuse THIS PRODUCT'S OWN
// clients, every time. That is the same shape as the macOS outage
// peercred_darwin.go was written to fix, and it is not worth repeating.
//
// GetNamedPipeClientProcessId sidesteps it entirely: the kernel records which
// process opened the pipe, independently of impersonation level and of anything
// the client sends on the wire. That is what makes this authentication rather
// than a self-asserted claim, which is the same property SO_PEERCRED provides
// on Linux.

// peerCred is the connecting peer's OS-reported identity.
//
// sid is a string rather than a numeric uid because that is what Windows
// identity IS — there is no uid to compare, and mapping one on would invent a
// number the OS does not recognise.
type peerCred struct {
	sid string
	pid uint32
}

// fdConn is any connection exposing its underlying handle.
//
// go-winio's pipe connection satisfies this: PipeConn does not declare Fd, but
// the concrete type behind it has an exported Fd method (file.go:277), and an
// interface assertion reaches an exported method on an unexported type. Asserted
// rather than reached for with reflection or unsafe.
type fdConn interface{ Fd() uintptr }

// readPeerCredFromConn retrieves the connecting peer's identity from a named
// pipe connection.
//
// Every failure is returned as an error so authorizePeer can fail CLOSED rather
// than guess at an identity.
func readPeerCredFromConn(conn any) (peerCred, error) {
	f, ok := conn.(fdConn)
	if !ok {
		return peerCred{}, fmt.Errorf("connection type %T exposes no pipe handle", conn)
	}
	pipe := windows.Handle(f.Fd())

	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &pid); err != nil {
		return peerCred{}, fmt.Errorf("reading the pipe's client process id: %w", err)
	}

	sid, err := sidOfProcess(pid)
	if err != nil {
		return peerCred{}, err
	}
	return peerCred{sid: sid, pid: pid}, nil
}

// sidOfProcess resolves a pid to the SID of the user running it.
//
// PID REUSE, STATED RATHER THAN IGNORED. Between the kernel recording the
// client's pid and this opening it, the client could exit and its pid be
// reused by an unrelated process. On Unix the equivalent race does not arise,
// because SO_PEERCRED captures the credential at connect() time. Here it is
// bounded by layer 1: the DACL means only THIS user can have opened the pipe,
// so a reused pid still belongs to this user and the comparison below still
// yields the right answer. The race can change which process is named in a log
// line; it cannot turn a refusal into an admission.
func sidOfProcess(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("opening peer process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("opening the token of peer process %d: %w", pid, err)
	}
	defer func() { _ = token.Close() }()

	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading the user of peer process %d: %w", pid, err)
	}
	return user.User.Sid.String(), nil
}

// selfSID returns this daemon's own user SID, for comparison against a peer's.
//
// DELIBERATELY GetTokenUser, AND IT MUST STAY THAT WAY. protocol's
// ensureOwnerOnlyDir was just changed from GetTokenUser to TokenOwner, and the
// same edit here would be a privilege-boundary collapse rather than a fix.
//
// The two ask different questions. Object ownership asks "did a token like mine
// create this?", and TokenOwner answers it. Peer authentication asks "is the
// process on the other end the SAME HUMAN as me?", and only the user SID answers
// that: on an elevated token TokenOwner is BUILTIN\Administrators, which every
// administrator on the machine shares, so comparing it would let any admin
// connect as any other admin. Same SID, opposite meaning.
func selfSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading this daemon's own user identity: %w", err)
	}
	return user.User.Sid.String(), nil
}
