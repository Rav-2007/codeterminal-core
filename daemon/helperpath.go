package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// helperBinName is the embedder helper's binary name, as produced by
// `cd helper && go build -o codeterminal-embedder-helper .`.
const helperBinName = "codeterminal-embedder-helper"

// helperBinEnvVar names an explicit override for the helper binary's location,
// for deployments whose layout none of the candidates below describe (a package
// manager splitting bin/ and libexec/, a container mounting it elsewhere).
const helperBinEnvVar = "CODETERMINAL_HELPER_BIN"

// legacyHelperBinPath is the path this used to be: a bare relative path, which
// resolves against the CURRENT WORKING DIRECTORY. That silently disabled
// retrieval for every launch that was not from the repo root -- an IDE
// extension, a service manager, a user in a subdirectory -- and the daemon then
// reported "no embedder/index configured" as though the index were the problem
// (Fix 8). Kept as the last candidate so `go run ./daemon` from the repo root,
// where the built binary lives in a temp directory that tells us nothing, keeps
// working.
const legacyHelperBinPath = "helper/" + helperBinName

// resolveHelperBinPath finds the embedder helper independently of the working
// directory, by looking relative to the daemon's OWN binary. The candidates
// cover the layouts this ships in:
//
//	$CODETERMINAL_HELPER_BIN        explicit override, used as given
//	<exedir>/codeterminal-…-helper  installed side by side (release layout)
//	<exedir>/helper/…               built at the repo root (go build ./daemon)
//	<exedir>/../helper/…            built in-package (cd daemon && go build)
//	helper/…                        legacy CWD-relative, for `go run`
//
// The returned path is the first candidate that exists. When none do, the
// legacy path is returned along with a descriptive error, so a caller can still
// attempt the start and report a helper-specific failure rather than a generic
// one.
func resolveHelperBinPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(helperBinEnvVar)); override != "" {
		return override, nil
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, helperBinName),
			filepath.Join(exeDir, "helper", helperBinName),
			filepath.Join(filepath.Dir(exeDir), "helper", helperBinName),
		)
	}
	candidates = append(candidates, legacyHelperBinPath)

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}

	return legacyHelperBinPath, fmt.Errorf(
		"embedder helper %q not found next to the daemon binary; build it with `cd helper && go build -o %s .`, or set %s to its path",
		helperBinName, helperBinName, helperBinEnvVar)
}
