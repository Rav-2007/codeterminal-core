package main

import "errors"

// errAlreadyRunning is the condition exitAlreadyRunning reports.
//
// A sentinel rather than a string match, because it is produced in two places
// that fail differently and must be treated identically: reclaimStaleSocket's
// probe (a daemon that was ALREADY up when this one started) and a bind that
// loses EADDRINUSE (two daemons starting AT ONCE, arbitrated by the kernel).
// See main.go for why one cannot cover the other.
var errAlreadyRunning = errors.New("another daemon already serves this workspace")

// The daemon's exit codes, which are a CONTRACT with whatever supervises it.
//
// clients/vscode/src/daemonSupervisor.ts is the consumer, and the question it
// has to answer on every unexpected exit is: retry, or report? Those are
// opposite actions and picking wrong is costly in both directions -- retrying a
// broken daemon is an infinite loop, reporting a benign one is a false alarm the
// user cannot act on -- so the daemon has to say which it was.
//
// Before these existed everything went through logger.Fatal, which is os.Exit(1)
// for every cause: an unreadable models.json, an unset CODETERMINAL_API_BASE, a
// workspace that is not a directory, and "another window already started a
// daemon for this exact repository, and it is healthy, and you should simply use
// it". A supervisor seeing 1 cannot distinguish those, so it must guess.
//
// Numbers are part of the contract and must not be renumbered. Mirrored in
// clients/vscode/src/daemonSupervisor.ts as EXIT_ALREADY_RUNNING.
const (
	// exitFailure is the ordinary "this daemon cannot run" code, and what
	// logger.Fatal already produces. Named here so the contrast with the code
	// below is stated rather than implied.
	//
	// It means: something is wrong with this daemon's configuration or
	// environment. Starting it again, unchanged, will fail again. A supervisor
	// should report it, and should stop retrying after a bounded number of
	// attempts.
	exitFailure = 1

	// exitAlreadyRunning means another daemon is ALREADY SERVING THIS EXACT
	// WORKSPACE, and this process stopped because it was redundant -- not
	// because anything is broken.
	//
	// This is a NORMAL outcome, not an error. Two VS Code windows on one
	// repository should share one daemon: same root, same index, same answers.
	// A supervisor seeing this should probe for the running daemon, adopt it,
	// and say nothing to the user -- it must NOT count the exit against a
	// restart budget, because nothing failed.
	//
	// 3, not 2: the flag package exits 2 on a usage error, so 2 is already
	// spoken for by the runtime and would collide.
	exitAlreadyRunning = 3
)
