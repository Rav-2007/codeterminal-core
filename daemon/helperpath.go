package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// helperBinEnvVar names a separate helper program to run instead of this
// binary's own helper role: the tests' fakehelper, or a developer comparing
// one build of the helper with another.
const helperBinEnvVar = "MOCHIII_HELPER_BIN"

// resolveHelperProgram is the embedder helper to run: a program, and the
// arguments that go before its own flags.
//
// SINCE 1.2.0 THE HELPER IS THIS BINARY, run as `<self> __embedder-helper`
// (programrole.go). There is no longer a file beside the daemon to look for,
// so there is nothing for a layout to get wrong and nothing for a repository
// to plant. The candidate list this used to walk -- beside the binary, helper/
// beside it, helper/ above it, and helper/ in the working directory under
// `go run` -- went with the file, and with it the hijack the last candidate
// invited (a workspace shipping helper/mochiii-embedder-helper), the Windows
// lookup that never added .exe (code search off on every Windows install until
// 2026-10-17), and the reason Fix 8 had to exist: retrieval switching itself
// off whenever the daemon started anywhere but the repository root.
//
// MOCHIII_HELPER_BIN still names a separate helper program, run as given.
func resolveHelperProgram() (bin string, roleArgs []string, err error) {
	if override := strings.TrimSpace(os.Getenv(helperBinEnvVar)); override != "" {
		return override, nil, nil
	}
	exe, err := ownProgram()
	if err != nil {
		return "", nil, fmt.Errorf("finding this program to run its embedder helper: %w", err)
	}
	return exe, []string{helperRoleArg}, nil
}

// ownProgram is this binary's file, symlinks resolved. Besides being the
// program the helper role runs as, it goes in the lockfile
// (protocol.LockFile.Executable), where the terminal app reads it to tell a
// daemon of its own install, left running by an upgrade that replaced the file,
// from anyone else's.
func ownProgram() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return exe, nil
}
