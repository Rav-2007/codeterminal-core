package main

import (
	"fmt"
	"io"
	"strings"

	"mochiii/clients/tui"
	"mochiii/helper"
)

// THE ONE BINARY (1.2.0). `mochiii` is the terminal client, the background
// daemon and the embedder helper in one executable; this decides which of
// them a run is. Until 1.1.0 they were three programs, because the helper
// needed cgo and the daemon had to stay pure Go; helper/internal/ort removed
// that reason.
//
//	mochiii [--workspace ... --prompt ... --stop ...]   the terminal client
//	mochiii daemon [daemon flags | subcommand]          the daemon
//	mochiii index|download-model|mcp|...                a daemon subcommand
//	mochiii __embedder-helper --socket ...              the helper (the daemon
//	                                                    starts it, never a user)
//	mochiii-daemon ...                                  the daemon, as before
//
// The NAME rule keeps every caller of the old daemon working unchanged: the
// VS Code extension ships this binary as mochiii-daemon[.exe], run-tui.sh and
// the tests build it under that name, and docs say `mochiii-daemon index`.
// Any other name -- mochiii, mochiii.exe, a downloaded mochiii-linux-x64 nobody
// renamed -- is the client. The sandbox helper and the reaper are dispatched
// before any of this (main), since they must run nothing else first.

type programRole int

const (
	programClient programRole = iota
	programDaemon
	programHelper
	programUnknown
)

// helperRoleArg is the first argument that makes a run the embedder helper.
const helperRoleArg = "__embedder-helper"

// daemonRoleArg is the first argument that makes a run the daemon.
const daemonRoleArg = "daemon"

// daemonProgramName is the name under which this binary is the daemon.
const daemonProgramName = "mochiii-daemon"

// programOf decides the role from the program name and arguments, and
// returns the arguments that role parses (without the program name or the
// role word). Only a bare word can name a role or subcommand: anything that
// starts with "-" is the client's, so `mochiii --version` is the client's
// version flag and not the daemon's version subcommand.
func programOf(args []string) (programRole, []string) {
	var name string
	var rest []string
	if len(args) > 0 {
		// Both separators on every OS: Windows hands over backslashes, and a
		// rule that only split on the host's separator could not be tested
		// for Windows anywhere else.
		name = args[0][strings.LastIndexAny(args[0], `/\`)+1:]
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
		rest = args[1:]
	}
	// The helper role word first, under ANY name: the daemon starts its helper
	// as itself, and the VS Code extension's copy of this binary is called
	// mochiii-daemon, so its helper runs as `mochiii-daemon __embedder-helper`.
	// Checking the name first made that child a second daemon, which refused
	// the helper's --socket flag: code search off in every VS Code install.
	if len(rest) > 0 && rest[0] == helperRoleArg {
		return programHelper, rest[1:]
	}
	if name == daemonProgramName {
		return programDaemon, rest
	}
	if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
		return programClient, rest
	}
	switch first := rest[0]; {
	case first == daemonRoleArg:
		return programDaemon, rest[1:]
	default:
		if _, ok := findSubcommand(first); ok {
			return programDaemon, rest
		}
		return programUnknown, rest
	}
}

// searchCommands are the daemon's subcommands that only code search uses. The
// static build for musl Linux has none (helper.CodeSearchBuilt), and refuses
// them by name rather than downloading a model it cannot run or failing deep
// inside an embedder that cannot start.
var searchCommands = map[string]bool{"index": true, "retrieve": true, "download-model": true, "helper-smoketest": true}

// asksForMissingSearch reports whether a daemon command line asks for code
// search in a build that has none (built false).
func asksForMissingSearch(rest []string, built bool) bool {
	return !built && len(rest) > 0 && searchCommands[rest[0]]
}

// noCodeSearch is what the build without code search says when asked for it.
const noCodeSearch = "code search isn't in this build of Mochiii, the static one for musl Linux (Alpine, for one). Chat and agent mode work; code search needs a glibc Linux, macOS or Windows."

// runProgramRole runs the client or the helper, neither of which comes back
// to main, or reports that this run is the daemon and the arguments it parses.
func runProgramRole(args []string, stderr io.Writer) (daemonArgs []string, exit int, isDaemon bool) {
	r, rest := programOf(args)
	switch r {
	case programClient:
		tui.InOneBinary = true
		tui.Version = daemonVersion
		tui.CodeSearch = helper.CodeSearchBuilt
		tui.Main(rest)
		return nil, 0, false
	case programHelper:
		helper.Version = daemonVersion
		return nil, helper.Main(rest), false
	case programDaemon:
		if asksForMissingSearch(rest, helper.CodeSearchBuilt) {
			_, _ = fmt.Fprintf(stderr, "mochiii: %s: %s\n", rest[0], noCodeSearch)
			return nil, 1, false
		}
		return rest, 0, true
	}
	_, _ = fmt.Fprintf(stderr, "mochiii: unknown command %q. Run `mochiii` in a project folder, or `mochiii daemon --help` for the background service's commands.\n", rest[0])
	return nil, 2, false
}
