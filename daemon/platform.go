package main

// Two syscall-level primitives that have no portable form, split per platform
// in platform_unix.go and platform_windows.go:
//
//	openNoFollow(path, flag, perm)  open a file, REFUSING a link at the leaf
//	                                rather than following it
//	processAlive(pid)               is this process still running?
//	killProcess(pid)                terminate it unconditionally
//
// openNoFollow only guards the LEAF. Ancestor directories must be confined
// separately — see confinedRestorePath, which is what actually keeps a
// restore inside the workspace. It is shared by the two write paths that take
// an attacker-influenceable destination (restoreOne, ensureGitignoreEntry) and
// by the log, audit and model-download sinks, which are internal paths but get
// the same treatment because the cost is one flag.
//
// The POSIX implementations are the reference; the Windows ones reproduce each
// property with a different primitive and are labelled NOT RUN ON HARDWARE
// until CI has a Windows runner.
