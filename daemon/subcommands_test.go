package main

import (
	"strings"
	"testing"
)

// EVERY SUBCOMMAND IS DISPATCHED AND DOCUMENTED, BY CONSTRUCTION.
//
// `connect` shipped working and was reported missing because --help named no
// subcommands. The fix was not to write a second list -- that only moves the
// problem -- but to make one table serve the dispatch and the help text. These
// tests hold the properties that table has to have for that to be true.
func TestEverySubcommandIsUsable(t *testing.T) {
	if len(subcommands) == 0 {
		t.Fatal("the subcommand table is empty; the daemon would accept no subcommands at all")
	}

	seen := map[string]bool{}
	for i, sc := range subcommands {
		if sc.name == "" {
			t.Errorf("subcommands[%d] has no name, so nothing can invoke it", i)
		}
		if seen[sc.name] {
			t.Errorf("%q appears twice; the second entry is unreachable because lookup stops at the first", sc.name)
		}
		seen[sc.name] = true

		// A nil runner is the failure that a hand-written help list hides: the
		// name is advertised, and typing it panics instead of running anything.
		if sc.run == nil {
			t.Errorf("%q is listed but has no runner, so --help offers a command that cannot run", sc.name)
		}
		if strings.TrimSpace(sc.summary) == "" {
			t.Errorf("%q has no summary, so --help lists a bare name that explains nothing", sc.name)
		}
	}
}

// The help text must name every entry. This is the property that used to need a
// regex over main.go, and is now a plain assertion over the same data both sides
// read.
func TestUsageNamesEverySubcommand(t *testing.T) {
	var b strings.Builder
	writeUsage(&b, "mochiii-daemon")
	out := b.String()

	for _, sc := range subcommands {
		if !strings.Contains(out, sc.name) {
			t.Errorf("--help never mentions %q, so a user cannot discover it", sc.name)
		}
		if !strings.Contains(out, sc.summary) {
			t.Errorf("--help lists %q without its summary", sc.name)
		}
	}

	// The two things that make the list legible rather than a bare dump.
	if !strings.Contains(out, "mochiii-daemon [subcommand] [flags]") {
		t.Errorf("the usage line does not say a subcommand is accepted:\n%s", out)
	}
	if !strings.Contains(out, "Flags (daemon mode):") {
		t.Errorf("the flags are not separated from the subcommands:\n%s", out)
	}

	// `connect` is first deliberately: it is what a new user needs and what was
	// impossible to find. If it stops being first, that was a decision, not a slip.
	subcmdList := out[strings.Index(out, "Subcommands"):]
	if !strings.Contains(strings.SplitN(subcmdList, "\n", 3)[1], "connect") {
		t.Errorf("connect is no longer the first subcommand listed:\n%s", out)
	}
}

// LOOKUP IS EXACT, AND THAT IS A SAFETY PROPERTY.
//
// A prefix match would mean a typo runs a real command: `conn` reaching `connect`
// makes a mistyped word write a credential file. It also rots -- an abbreviation
// that resolves today becomes ambiguous as soon as a second subcommand shares the
// prefix.
func TestFindSubcommandMatchesExactlyAndNothingElse(t *testing.T) {
	for _, sc := range subcommands {
		got, ok := findSubcommand(sc.name)
		if !ok {
			t.Errorf("%q is in the table but cannot be found, so typing it falls through to daemon mode", sc.name)
			continue
		}
		if got.name != sc.name {
			t.Errorf("looking up %q returned %q", sc.name, got.name)
		}
	}

	for _, name := range []string{"", "conn", "connec", "connects", "CONNECT", " connect", "--connect", "serve"} {
		if _, ok := findSubcommand(name); ok {
			t.Errorf("%q resolved to a subcommand; only exact names may", name)
		}
	}
}
