//go:build linux && amd64

package mcp

import "golang.org/x/sys/unix"

// The architecture the seccomp filter admits, and whether this architecture has
// an x32 ABI to shut: on amd64, x32 syscalls arrive with AUDIT_ARCH_X86_64 and
// the x32 bit set in the number.
const (
	seccompSupported   = true
	seccompAuditArch   = unix.AUDIT_ARCH_X86_64
	seccompX32Possible = true
)

// seccompBlockedSyscalls are the System V IPC and POSIX message-queue calls the
// filter refuses with EPERM, so landlock reaches bwrap's --unshare-ipc: they are
// neither path-based (landlock cannot see them) nor covered by a landlock scope,
// yet they let a command attach to another of the user's processes' shared
// memory, semaphores or queues. Builds never use them; bwrap already denies
// them, so this is parity, not a new limit on legitimate work.
//
// Listed per-arch, by number, because the numbers differ between architectures
// and a shared list would have to compile on every linux GOARCH.
var seccompBlockedSyscalls = []uint32{
	unix.SYS_SHMGET, unix.SYS_SHMAT, unix.SYS_SHMDT, unix.SYS_SHMCTL,
	unix.SYS_SEMGET, unix.SYS_SEMOP, unix.SYS_SEMTIMEDOP, unix.SYS_SEMCTL,
	unix.SYS_MSGGET, unix.SYS_MSGSND, unix.SYS_MSGRCV, unix.SYS_MSGCTL,
	unix.SYS_MQ_OPEN, unix.SYS_MQ_TIMEDSEND, unix.SYS_MQ_TIMEDRECEIVE,
	unix.SYS_MQ_NOTIFY, unix.SYS_MQ_GETSETATTR, unix.SYS_MQ_UNLINK,
}
