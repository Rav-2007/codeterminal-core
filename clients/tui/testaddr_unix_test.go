//go:build unix

package main

import (
	"path/filepath"
	"testing"

	"codeterminal/protocol"
)

// testAddress returns a unique address on the platform's REAL transport.
//
// MIRRORED VERBATIM in daemon/testaddr_unix_test.go and its _windows_ sibling.
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
	// t.TempDir(), not a fixed path: sun_path caps a Unix socket around 108
	// bytes, and a long test name plus a nested temp dir gets close enough that
	// "d.sock" rather than "daemon.sock" is a deliberate saving.
	return protocol.Address{
		Transport: protocol.TransportUnix,
		Address:   filepath.Join(shortTempDir(t), "d.sock"),
	}
}
