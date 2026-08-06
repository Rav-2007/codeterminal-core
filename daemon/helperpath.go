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
//
// IT IS NOW OFFERED ONLY UNDER `go run` -- see runningFromGoRun for why that is
// the same rule this comment always described, rather than a new restriction.
const legacyHelperBinPath = "helper/" + helperBinName

// runningFromGoRun reports whether this binary was built by `go run`, which
// places it under the system temp directory.
//
// This gates the one candidate that resolves against the WORKING DIRECTORY, and
// it exists because that candidate is otherwise a code-execution primitive: the
// daemon's working directory is the workspace it was asked to serve, so a
// repository shipping helper/codeterminal-embedder-helper could have it started
// as this daemon's embedder. Same defect class as the TUI's /mcp-server binary
// hijack, which was CONFIRMED by execution; this one is narrower only because
// it is the LAST candidate and so needs a real helper to be absent first.
//
// The condition is not a new rule -- legacyHelperBinPath's comment already said
// the candidate exists for "`go run ./daemon` from the repo root, where the
// built binary lives in a temp directory". This makes that sentence executable
// instead of assumed. An installed daemon's binary is never under the temp
// directory, so the candidate is simply not offered to it, and the developer
// workflow the comment protects is untouched.
//
// Not attacker-controllable: it asks where OUR OWN binary is, which a hostile
// repository has no say over. A developer who does hit this path is running
// `go run` inside this repository, not inside a repository they are auditing.
//
// Split in two on purpose. pathIsUnder is the whole security decision and is a
// pure function of two strings, so it is tested exhaustively against real path
// shapes. runningFromGoRun is the thin part that asks the OS where we are.
//
// goRunDetector is a variable only so the resolver's behaviour can be asserted
// for an INSTALLED daemon. A test binary is itself built into the temp
// directory, so without this the installed case -- the one that matters -- could
// never be exercised and its test would permanently skip. It stubs a boolean,
// not an OS mechanism: nothing about process lookup, exec or the filesystem is
// faked, and the function it replaces is separately tested.
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
	// fallback is what comes back ALONGSIDE the error. It matters as much as the
	// candidates: defaultHelperBinPath discards the error and feeds this value
	// to `helper-smoketest` as the binary to run. When it was
	// legacyHelperBinPath, that meant a failed resolution inside a hostile
	// repository handed the smoketest that repository's own
	// helper/codeterminal-embedder-helper to execute -- the error path was the
	// vulnerability. It is now anchored next to this binary, which is also what
	// the error message below claims to have checked.
	fallback := legacyHelperBinPath
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
		fallback = candidates[0]
	}
	if goRunDetector() {
		candidates = append(candidates, legacyHelperBinPath)
		fallback = legacyHelperBinPath
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}

	return fallback, fmt.Errorf(
		"embedder helper %q not found next to the daemon binary; build it with `cd helper && go build -o %s .`, or set %s to its path",
		helperBinName, helperBinName, helperBinEnvVar)
}
