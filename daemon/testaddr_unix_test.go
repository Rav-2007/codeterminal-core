//go:build unix

package main

import (
	"path/filepath"
	"testing"

	"codeterminal/protocol"
)

// testAddress returns a unique address on the platform's REAL transport.
//
// MIRRORED VERBATIM in clients/tui/testaddr_unix_test.go and its _windows_ sibling.
// Two modules need the same thing and Go test helpers do not cross module
// boundaries, so the duplication is deliberate and labelled, exactly as
// confinement_conformance_test.go duplicates its vector table.
//
// The point of routing through protocol.Address at all: every test in this
// package used to call net.Listen("unix", …) directly. That is a transport this
// product does not use on Windows -- protocol.Listen picks a named pipe there --
// so the tests were pinning a code path the user never takes, and on Windows
// they failed at the bind with "invalid argument" before asserting anything.
// The named-pipe transport consequently had ZERO test coverage on the platform
// it exists for. PART 0 rule 4: a test must enter through the same door.
func testAddress(t *testing.T) protocol.Address {
	t.Helper()
	// shortTempDir, not t.TempDir(): sun_path is 104 bytes on macOS and
	// $TMPDIR there is ~49 of them before a test name is added. See its doc
	// comment -- this is the fixture fault that failed 15 tests on the first
	// macOS CI run. "d.sock" over "daemon.sock" is kept, but it was never the
	// part that mattered.
	return protocol.Address{
		Transport: protocol.TransportUnix,
		Address:   filepath.Join(shortTempDir(t), "d.sock"),
	}
}
