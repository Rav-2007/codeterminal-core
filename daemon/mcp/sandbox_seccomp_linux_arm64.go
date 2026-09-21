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
