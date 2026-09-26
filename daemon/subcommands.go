package main

import (
	"flag"
	"fmt"
	"io"
	"log"
)

// THE ONE-SHOT SUBCOMMANDS, AS A SINGLE SOURCE OF TRUTH.
//
// This table is read by BOTH the dispatch in main() and by --help. That is the
// whole point of it existing: `connect` shipped working and was then reported
// missing, because --help printed the flag list and nothing named the
// subcommands. Fixing that with a second hand-written list would have set up the
// next version of the same bug -- a ninth subcommand dispatched but undocumented,
// or documented but not dispatched.
//
// Every runner already had the same signature, so nothing had to be bent to fit.

// subcommand is one command that runs and exits, as opposed to the long-running
// serve path. None of them is ever invoked automatically on daemon start or
// per-prompt: reaching one requires the user to have typed its name.
type subcommand struct {
	name string
	// summary is one line for --help. Written for someone who does not know this
	// codebase and is looking for the thing they need.
	summary string
	run     func(args []string, logger *log.Logger) error
}

// subcommands is ordered for a READER, not alphabetically and not by age:
// `connect` is first because supplying a key is the first thing a new user needs
// and the thing that was impossible to find.
var subcommands = []subcommand{
	{"connect", "store the provider API key used for inference (--show, --forget)", runConnectCommand},
	{"status", "print what a running daemon reports, as JSON", runStatusCommand},
	{"index", "build or refresh the retrieval index for a workspace", runIndexCommand},
	{"retrieve", "run one retrieval query against the index", runRetrieveCommand},
	{"edits", "apply or inspect edit payloads", runEditsCommand},
	{"mcp", "inspect the configured MCP servers", runMCPCommand},
	{"download-model", "fetch the embedding model this machine needs", runDownloadModelCommand},
	{"helper-smoketest", "check that the embedder helper runs here", runHelperSmoketestCommand},
}

// findSubcommand resolves a name to its entry.
//
// Matching is EXACT. A prefix match would be a trap rather than a convenience:
// `mochiii-daemon conn` silently running `connect` means a typo executes a
// command that writes a credential file, and an abbreviation that works today
// becomes ambiguous the moment a second subcommand shares its prefix.
func findSubcommand(name string) (subcommand, bool) {
	for _, sc := range subcommands {
		if sc.name == name {
			return sc, true
		}
	}
	return subcommand{}, false
}

// writeUsage renders --help: what this binary is, the subcommands it accepts, and
// then the daemon's own flags.
//
// It takes an io.Writer rather than reaching for flag.CommandLine.Output() so a
// test can read exactly what a user would see. The version of this that lived
// inside main() as a closure could not be tested at all.
func writeUsage(w io.Writer, binary string) {
	_, _ = fmt.Fprintf(w, "Usage: %s [subcommand] [flags]\n\n", binary)
	_, _ = fmt.Fprint(w, "With no subcommand, runs the long-running daemon that clients connect to.\n\n")
	_, _ = fmt.Fprint(w, "Subcommands (each runs and exits; `<subcommand> -h` for its own flags):\n")

	width := 0
	for _, sc := range subcommands {
		if len(sc.name) > width {
			width = len(sc.name)
		}
	}
	for _, sc := range subcommands {
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", width, sc.name, sc.summary)
	}

	_, _ = fmt.Fprint(w, "\nFlags (daemon mode):\n")
	flag.PrintDefaults()
}
