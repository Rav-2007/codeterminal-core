//go:build linux && arm64

package mcp

import "golang.org/x/sys/unix"

// The architecture the seccomp filter admits. arm64 has no x32 equivalent; its
// 32-bit compat syscalls arrive as AUDIT_ARCH_ARM and are refused as foreign.
const (
	seccompSupported   = true
	seccompAuditArch   = unix.AUDIT_ARCH_AARCH64
	seccompX32Possible = false
)

// seccompBlockedSyscalls: see the amd64 file for what these are and why. Listed
// per-arch because the syscall numbers differ; arm64's asm-generic ABI defines
// each of them.
var seccompBlockedSyscalls = []uint32{
	unix.SYS_SHMGET, unix.SYS_SHMAT, unix.SYS_SHMDT, unix.SYS_SHMCTL,
	unix.SYS_SEMGET, unix.SYS_SEMOP, unix.SYS_SEMTIMEDOP, unix.SYS_SEMCTL,
	unix.SYS_MSGGET, unix.SYS_MSGSND, unix.SYS_MSGRCV, unix.SYS_MSGCTL,
	unix.SYS_MQ_OPEN, unix.SYS_MQ_TIMEDSEND, unix.SYS_MQ_TIMEDRECEIVE,
	unix.SYS_MQ_NOTIFY, unix.SYS_MQ_GETSETATTR, unix.SYS_MQ_UNLINK,
}
