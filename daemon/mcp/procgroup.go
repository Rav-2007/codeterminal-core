package mcp

// Containing a Lane B server's process TREE, not just its process.
//
// WHAT THIS STOPS (M4, reproduced by testdata/badserver's orphan mode). A server
// can start a child, hand it stdout, and exit immediately. Signalling the pid we
// started then reports success while the child it left behind keeps running —
// with the user's full privileges, after the turn that authorised it ended. A
// grandchild of a Lane B server is as unconfined as its parent; this is what
// makes "the turn ended" and "the programs it started are gone" the same event.
//
// This is Lane B only. helperproc.go deliberately does none of it: the embedder
// helper is our own binary, we know it forks nothing, and killing its pid is
// killing all of it. An MCP server is somebody else's program.
//
// THE SEAM. Two backends implement the same four-step lifecycle, because the
// primitives have nothing in common — POSIX signals a negative pid, Windows has
// no such thing and must use a Job Object:
//
//	prepare(cmd)   before Start: put the child somewhere killable as a set
//	adopt(cmd)     after Start:  finish that (no-op on POSIX)
//	killAll(pid)   kill the set
//	release()      drop any handle the set needed
//
// adopt exists only because Windows needs it, and it is not cosmetic: see
// procgroup_windows.go for the CreateProcess-to-AssignProcessToJobObject race it
// closes and why the child is started suspended.
//
// The POSIX backend is the reference implementation; the Windows one reproduces
// each documented property with a different primitive and is labelled NOT RUN ON
// HARDWARE until CI has a Windows runner.
