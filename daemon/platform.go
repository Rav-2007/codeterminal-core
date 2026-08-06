package main

// Two syscall-level primitives that have no portable form, split per platform
// in platform_unix.go and platform_windows.go:
//
//	openNoFollow(path, flag, perm)  open a file, REFUSING a link at the leaf
//	                                rather than following it
//	processAlive(pid)               is this process still running?
//	killProcess(pid)                terminate it unconditionally
//	restrictToOwner(path, perm)     make path readable by its owner and nobody
//	                                else
//	openControllingTerminal()       open this process's terminal, for prompting
//	                                when stdin has been consumed
//
// restrictToOwner replaced five bare os.Chmod calls, and it is here rather than
// inline because os.Chmod DOES NOTHING USEFUL ON WINDOWS. Go maps a Unix mode
// onto the single FILE_ATTRIBUTE_READONLY bit, and os.Stat maps it back out as
// a flat 0666 or 0777. So every one of those chmods was a silent no-op on the
// platform almost everyone uses, and the five tests asserting the result were
// reporting 0666 where they wanted 0600.
//
// That was not a test problem. The index, the memory database, the skills
// database and the log all hold the user's own source text and conversation
// transcripts, and on Windows they were protected only by whatever DACL they
// happened to inherit from their parent -- which for a workspace at C:\projects
// is nothing in particular. The POSIX half of this product enforced a control
// the Windows half did not have.
//
// The POSIX implementations are the reference; the Windows ones reproduce each
// property with a different primitive and are labelled NOT RUN ON HARDWARE
// until CI has a Windows runner.
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
