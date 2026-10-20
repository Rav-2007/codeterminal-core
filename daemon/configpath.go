package main

import (
	"os"
	"path/filepath"
	"strings"
)

// configFileName is models.json's name wherever it is found.
const configFileName = "models.json"

// legacyConfigPath is what the -config default used to be: a bare relative
// path, resolved against the CURRENT WORKING DIRECTORY. Since 1.2.0 it is
// offered only under `go run`; see resolveConfigPath.
const legacyConfigPath = "./" + configFileName

// resolveConfigPath finds models.json independently of the working directory,
// by looking relative to the daemon's OWN binary first:
//
//	<exedir>/models.json     installed side by side (the packaged extension
//	                         ships one there)
//	<exedir>/../models.json  built in-package (cd daemon && go build)
//	./models.json            legacy CWD-relative, ONLY under `go run`
//
// With none of them, it returns "" and LoadConfig uses the copy built into
// this binary (builtinconfig.go): since 1.2.0 the one `mochiii` binary carries
// its own models.json, so an install is one file.
//
// THE WORKING-DIRECTORY CANDIDATE IS A HIJACK WHEN INSTALLED. The daemon's
// working directory is the workspace it serves, and models.json names the
// programs agent mode spawns, so a repository shipping its own models.json
// would choose them. Until 1.2.0 the terminal client closed that by always
// passing --config explicitly; the daemon it now starts is itself, with no
// --config, so the rule has to live here. It is the one the helper's lookup
// enforced until the helper became this binary: the candidate exists for
// `go run ./daemon` from the repo root, and is offered only there.
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
	if goRunDetector() {
		candidates = append(candidates, legacyConfigPath)
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// runningFromGoRun reports whether this binary was built by `go run`, which
// places it under the system temp directory.
//
// It gates the one candidate that resolves against the WORKING DIRECTORY, and
// is not attacker-controllable: it asks where OUR OWN binary is, which a
// hostile repository has no say over. A developer who does hit this path is
// running `go run` inside this repository, not inside one they are auditing.
//
// Split in two on purpose. pathIsUnder is the whole security decision and is a
// pure function of two strings, so it is tested exhaustively against real path
// shapes. runningFromGoRun is the thin part that asks the OS where we are.
//
// goRunDetector is a variable only so the resolver's behaviour can be asserted
// for an INSTALLED daemon. A test binary is itself built into the temp
// directory, so without this the installed case -- the one that matters --
// could never be exercised and its test would permanently skip. It stubs a
// boolean, not an OS mechanism.
var goRunDetector = runningFromGoRun

func runningFromGoRun() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	tmp := os.TempDir()
	if resolved, rerr := filepath.EvalSymlinks(tmp); rerr == nil {
		tmp = resolved
	}
	return pathIsUnder(exe, tmp)
}

// pathIsUnder reports whether path lies inside dir.
//
// filepath.Rel rather than strings.HasPrefix: "/tmpfoo" has "/tmp" as a string
// prefix while being nowhere near it, and getting that wrong here would hand
// the working-directory candidate to a daemon installed at such a path.
func pathIsUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
