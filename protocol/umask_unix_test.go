//go:build unix

package protocol

import "syscall"

// syscallUmask wraps syscall.Umask so transport_unix_test.go can set a
// deliberately permissive umask without importing syscall itself.
func syscallUmask(mask int) int { return syscall.Umask(mask) }
