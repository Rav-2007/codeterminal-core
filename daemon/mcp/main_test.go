package mcp

import (
	"os"
	"testing"
)

// TestMain lets this package's test binary serve as the landlock sandbox
// helper: under `go test`, selfExecutable is the test binary, and the helper
// tests re-execute it. Without this dispatch the re-executed binary would run
// the whole suite again instead of the command.
func TestMain(m *testing.M) {
	MaybeRunSandboxHelper()
	os.Exit(m.Run())
}
