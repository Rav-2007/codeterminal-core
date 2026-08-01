package main

import (
	"os"
	"path/filepath"
)

// configFileName is models.json's name wherever it is found.
const configFileName = "models.json"

// legacyConfigPath is what the -config default used to be: a bare relative
// path, resolved against the CURRENT WORKING DIRECTORY. Kept as the last
// candidate so `go run ./daemon` from the repo root still works, exactly as
// legacyHelperBinPath is kept in helperpath.go.
const legacyConfigPath = "./" + configFileName

// resolveConfigPath finds models.json independently of the working directory,
// by looking relative to the daemon's OWN binary first:
//
//	<exedir>/models.json     installed side by side (release layout, and where
//	                         the packaged extension puts it)
//	<exedir>/../models.json  built in-package (cd daemon && go build)
//	./models.json            legacy CWD-relative, for `go run` from the root
//
// This mirrors resolveHelperBinPath deliberately — same problem, same shape,
// and its doc comment records what the CWD-relative version cost (Fix 8): the
// daemon could only ever be started from the repo root, which "Daemon must run
// from repo root (config-path gotcha)" has been an open backlog item about
// since 2026-07-08.
//
// The first candidate that exists wins. When none do, the legacy path is
// returned unchanged so LoadConfig produces its own error naming that path,
// which is the message operators already recognise.
func resolveConfigPath() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, configFileName),
			filepath.Join(filepath.Dir(exeDir), configFileName),
		)
	}
	candidates = append(candidates, legacyConfigPath)

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return legacyConfigPath
}
