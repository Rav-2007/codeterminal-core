//go:build windows

package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"codeterminal/protocol"
)

// testPipeSeq makes each pipe name unique within a process. The PID handles
// uniqueness ACROSS processes, which matters because \\.\pipe\ is a single
// machine-global namespace with no directories -- two packages testing at once
// would otherwise collide, and the loser sees a confusing "already exists"
// rather than a test failure.
var testPipeSeq atomic.Uint64

// testAddress returns a unique address on the platform's REAL transport.
//
// MIRRORED VERBATIM in daemon/testaddr_windows_test.go and its _unix_ sibling.
// See the Unix file for why the duplication is deliberate.
//
// A pipe name is NOT a filesystem path: no directory, no permission bits, and
// no residue when the owner dies, which is why there is no t.TempDir() here and
// nothing to clean up. Closing the listener is the whole teardown.
func testAddress(t *testing.T) protocol.Address {
	t.Helper()
	return protocol.Address{
		Transport: protocol.TransportNamedPipe,
		Address:   fmt.Sprintf(`\\.\pipe\codeterminal-test-%d-%d`, os.Getpid(), testPipeSeq.Add(1)),
	}
}
