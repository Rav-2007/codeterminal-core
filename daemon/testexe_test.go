package main

import "runtime"

// exeName appends the platform's executable suffix to a binary name.
//
// MIRRORED VERBATIM in daemon/mcp/testexe_test.go. Two packages build helper
// binaries with `go build -o` and then exec them, and Go test helpers do not
// cross package boundaries.
//
// Without it, `go build -o <dir>/fakehelper ./testdata/fakehelper` on Windows
// produces a file the loader will not run:
//
//	exec: "C:\...\fakehelper": executable file not found in %PATH%
//
// which is a confusing way to say "that is not an executable". Windows decides
// executability from the EXTENSION, not from a mode bit -- and exec.LookPath
// consults %PATHEXT% (.COM;.EXE;.BAT;…), so a suffixless file is never a
// candidate no matter where it sits. That single missing ".exe" accounted for 31
// of the 40 daemon failures on the first Windows test run.
//
// The production code needs no equivalent: it execs `gopls`, `npm`, `docker` and
// friends by bare name and lets LookPath apply PATHEXT itself. This is purely an
// artefact of tests COMPILING their own fixtures.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
