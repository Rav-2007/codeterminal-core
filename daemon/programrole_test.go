package main

import (
	"slices"
	"testing"
)

// The one binary's dispatch, case by case. Each row is a way the binary is
// started in the field: by a user, by the installers, by the VS Code
// extension (as mochiii-daemon), by the client starting its own daemon, and by
// the daemon starting its helper.
func TestTheProgramIsChosenByNameAndFirstWord(t *testing.T) {
	for _, c := range []struct {
		args []string
		want programRole
		rest []string
	}{
		// The client: no arguments, or flags.
		{[]string{"/home/u/.mochiii/bin/mochiii"}, programClient, nil},
		{[]string{"mochiii", "--version"}, programClient, []string{"--version"}},
		{[]string{"mochiii", "--workspace", "/p", "--prompt", "hi"}, programClient, []string{"--workspace", "/p", "--prompt", "hi"}},
		{[]string{`C:\Users\u\Programs\Mochiii\bin\mochiii.exe`, "--stop"}, programClient, []string{"--stop"}},
		// A downloaded standalone binary nobody renamed is still the client.
		{[]string{"./mochiii-linux-x64"}, programClient, nil},
		// The daemon, by role word: what the client starts.
		{[]string{"mochiii", "daemon", "--workspace", "/p"}, programDaemon, []string{"--workspace", "/p"}},
		// A daemon subcommand, by its own name.
		{[]string{"mochiii", "index", "/p"}, programDaemon, []string{"index", "/p"}},
		{[]string{"mochiii", "download-model", "--check"}, programDaemon, []string{"download-model", "--check"}},
		{[]string{"mochiii", "version"}, programDaemon, []string{"version"}},
		// The daemon, by name: the VS Code extension ships this binary as
		// mochiii-daemon[.exe], and every flag of the old daemon means what it did.
		{[]string{"/ext/daemon/mochiii-daemon", "--workspace", "/p"}, programDaemon, []string{"--workspace", "/p"}},
		{[]string{`C:\ext\daemon\MOCHIII-DAEMON.EXE`, "--version"}, programDaemon, []string{"--version"}},
		{[]string{"mochiii-daemon", "index", "/p"}, programDaemon, []string{"index", "/p"}},
		// The helper, which the daemon starts as itself -- under the daemon's
		// name too, which is how the VS Code extension's copy is called.
		{[]string{"mochiii", helperRoleArg, "--socket", "/s"}, programHelper, []string{"--socket", "/s"}},
		{[]string{"/ext/daemon/mochiii-daemon", helperRoleArg, "--socket", "/s"}, programHelper, []string{"--socket", "/s"}},
		{[]string{`C:\ext\daemon\mochiii-daemon.exe`, helperRoleArg, "--socket", `\\.\pipe\h`}, programHelper, []string{"--socket", `\\.\pipe\h`}},
		// Anything else is refused by name, not run as something.
		{[]string{"mochiii", "frobnicate"}, programUnknown, []string{"frobnicate"}},
	} {
		got, rest := programOf(c.args)
		if got != c.want || !slices.Equal(rest, c.rest) {
			t.Errorf("programOf(%q) = %v %q, want %v %q", c.args, got, rest, c.want, c.rest)
		}
	}
}

// --version as a FLAG is the client's (mochiii <v>); as a bare word it is the
// daemon's version subcommand. findSubcommand maps "--version" to "version",
// so only bare words may reach it -- or `mochiii --version` would print the
// daemon's line, and the installers, which compare it with "mochiii <v>",
// would refuse every install.
func TestTheVersionFlagIsTheClients(t *testing.T) {
	if got, _ := programOf([]string{"mochiii", "--version"}); got != programClient {
		t.Fatalf("`mochiii --version` went to %v, want the client", got)
	}
	if _, ok := findSubcommand("--version"); !ok {
		t.Skip("the daemon no longer treats --version as a subcommand; the ordering this pins is moot")
	}
}

// The static musl build refuses code search's commands by name, and only those.
func TestTheBuildWithoutSearchRefusesOnlySearchCommands(t *testing.T) {
	for _, tc := range []struct {
		rest  []string
		built bool
		want  bool
	}{
		{[]string{"index", "."}, false, true},
		{[]string{"download-model", "--check"}, false, true},
		{[]string{"retrieve", "q"}, false, true},
		{[]string{"helper-smoketest"}, false, true},
		{[]string{"connect"}, false, false},
		{[]string{"--workspace", "."}, false, false},
		{nil, false, false},
		{[]string{"index", "."}, true, false},
		{[]string{"download-model"}, true, false},
	} {
		if got := asksForMissingSearch(tc.rest, tc.built); got != tc.want {
			t.Errorf("asksForMissingSearch(%q, built=%v) = %v, want %v", tc.rest, tc.built, got, tc.want)
		}
	}
	for name := range searchCommands {
		if _, ok := findSubcommand(name); !ok {
			t.Errorf("searchCommands names %q, which is no daemon subcommand", name)
		}
	}
}
