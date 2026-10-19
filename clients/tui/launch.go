package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"mochiii/editapply"
)

// launchOptions are the command-line choices about setting a folder up.
type launchOptions struct {
	forceIndex bool // --index: (re)build the folder's index without asking
	noIndex    bool // --no-index or MOCHIII_NO_INDEX=1: never offer to
}

// prepareDaemon makes sure a daemon serves workspaceRoot before the client
// connects: the first-run offer when a person is at the terminal, then the
// daemon itself if none answers. It does nothing for a build with no daemon
// installed beside it, so connectToDaemon reports a missing daemon exactly as
// it always has.
func prepareDaemon(workspaceRoot string, launch launchOptions, interactive bool) {
	daemon, config, ok := installedDaemon()
	if !ok {
		return
	}
	if interactive && (launch.forceIndex || !launch.noIndex) {
		home, _ := os.UserHomeDir()
		folderSetup{
			root:     workspaceRoot,
			home:     home,
			in:       bufio.NewReader(os.Stdin),
			out:      os.Stderr,
			runner:   runDaemonCommand(daemon),
			declined: newDeclinedFolders(),
			running:  daemonAnswers,
			stop:     func() error { return stopDaemon(os.Stderr) },
		}.offer(launch.forceIndex)
	}
	if daemonAnswers() {
		return
	}
	if err := startInstalledDaemon(daemon, config, workspaceRoot, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runStop is `mochiii --stop`: stop the background service for this folder. It
// is what to run before re-indexing by hand, after an upgrade, or when the
// service seems stuck; the next `mochiii` starts a fresh one.
func runStop(workspace string) int {
	root, err := editapply.ResolveRealWorkspaceRoot(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	setDaemonWorkspaceRoot(root)
	if err := stopDaemon(os.Stderr); err != nil {
		if errors.Is(err, errNoDaemonRunning) {
			fmt.Fprintln(os.Stderr, err)
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Stopped.")
	return 0
}
