//go:build aix || linux || solaris || zos

package main

import "golang.org/x/sys/unix"

// See termecho_ioctl_bsd.go.
const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
