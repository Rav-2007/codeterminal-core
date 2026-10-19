package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The daemon this client starts is the one installed beside it, with its
// models.json, and nothing else: not PATH, not an environment variable, not the
// workspace. Those are how a repository or a stray PATH entry would get a
// program of its choosing run.
func TestTheDaemonIsFoundOnlyBesideThisBinary(t *testing.T) {
	daemonFile := executableFileName(daemonBinName, runtime.GOOS)

	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, daemonFile), "x")
	writeFile(t, filepath.Join(installed, "models.json"), "{}")
	daemon, config, ok := installedDaemonIn(installed)
	if !ok || daemon != filepath.Join(installed, daemonFile) || config != filepath.Join(installed, "models.json") {
		t.Fatalf("installedDaemonIn(installed) = %q, %q, %v", daemon, config, ok)
	}

	noConfig := t.TempDir()
	writeFile(t, filepath.Join(noConfig, daemonFile), "x")
	if _, _, ok := installedDaemonIn(noConfig); ok {
		t.Error("a daemon with no models.json beside it was accepted; it would load whatever config the project has")
	}
	noDaemon := t.TempDir()
	writeFile(t, filepath.Join(noDaemon, "models.json"), "{}")
	if _, _, ok := installedDaemonIn(noDaemon); ok {
		t.Error("a folder with no daemon in it was accepted")
	}

	// A daemon offered everywhere except beside the binary must not be found.
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, daemonFile), "x")
	writeFile(t, filepath.Join(elsewhere, "models.json"), "{}")
	t.Setenv("PATH", elsewhere)
	t.Setenv("MOCHIII_DAEMON_BIN", filepath.Join(elsewhere, daemonFile))
	t.Chdir(elsewhere)
	if d, _, ok := installedDaemonIn(t.TempDir()); ok {
		t.Errorf("found %q through PATH, the environment or the working directory", d)
	}
}

func TestTheDaemonFileIsNamedAsEachPlatformShipsIt(t *testing.T) {
	if got := executableFileName("mochiii-daemon", "windows"); got != "mochiii-daemon.exe" {
		t.Errorf("windows: %q", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := executableFileName("mochiii-daemon", goos); got != "mochiii-daemon" {
			t.Errorf("%s: %q", goos, got)
		}
	}
}

// codedErr is a stand-in for *exec.ExitError: an error with an exit code.
type codedErr int

func (c codedErr) Error() string { return "exit status " + string(rune('0'+int(c))) }
func (c codedErr) ExitCode() int { return int(c) }

// countingProbe answers true from its nth call on, and counts the calls.
func countingProbe(n int) (probe func() bool, calls *int) {
	calls = new(int)
	return func() bool { *calls++; return *calls >= n }, calls
}

func TestStartWaitsUntilTheDaemonAnswers(t *testing.T) {
	probe, calls := countingProbe(3)
	err := daemonStart{
		start:   func() (<-chan error, error) { return make(chan error), nil },
		answers: probe,
		timeout: 5 * time.Second,
		poll:    time.Millisecond,
	}.run()
	if err != nil {
		t.Fatalf("run() = %v", err)
	}
	if *calls != 3 {
		t.Errorf("the daemon was asked %d time(s); run returned before it answered", *calls)
	}
}

// Exit code 3 means another daemon already serves this folder, which is
// success: the wait goes on until that one answers.
func TestAnotherDaemonServingTheFolderIsNotAFailure(t *testing.T) {
	probe, _ := countingProbe(5)
	exited := make(chan error, 1)
	exited <- codedErr(daemonAlreadyServing)
	err := daemonStart{
		start:   func() (<-chan error, error) { return exited, nil },
		answers: probe,
		timeout: 5 * time.Second,
		poll:    time.Millisecond,
	}.run()
	if err != nil {
		t.Fatalf("run() = %v, want the wait to continue for the daemon already serving", err)
	}
}

// A daemon that dies while starting says why: the end of its log is the
// message, not a bare exit status.
func TestADaemonThatDiesWhileStartingShowsItsLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, logPath, "starting\nmodels.json: unexpected end of JSON input\n")
	exited := make(chan error, 1)
	exited <- codedErr(1)
	err := daemonStart{
		start:   func() (<-chan error, error) { return exited, nil },
		answers: func() bool { return false },
		logPath: logPath,
		timeout: 5 * time.Second,
		poll:    time.Millisecond,
	}.run()
	if err == nil || !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("run() = %v, want the log's last lines in the error", err)
	}
}

func TestADaemonThatNeverAnswersTimesOut(t *testing.T) {
	err := daemonStart{
		start:   func() (<-chan error, error) { return make(chan error), nil },
		answers: func() bool { return false },
		logPath: "daemon.log",
		timeout: 30 * time.Millisecond,
		poll:    5 * time.Millisecond,
	}.run()
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("run() = %v, want a timeout naming the log", err)
	}
}

func TestAFailedStartIsReported(t *testing.T) {
	err := daemonStart{
		start:   func() (<-chan error, error) { return nil, errors.New("permission denied") },
		answers: func() bool { return true },
		timeout: time.Second,
		poll:    time.Millisecond,
	}.run()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("run() = %v", err)
	}
}

func TestTheLogTailIsTheLastLines(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "d.log")
	writeFile(t, logPath, "1\n2\n3\n4\n5\n")
	if got := logTail(logPath, 2); got != "  4\n  5" {
		t.Errorf("logTail = %q", got)
	}
	if got := logTail(filepath.Join(t.TempDir(), "missing"), 2); !strings.Contains(got, "no log") {
		t.Errorf("logTail of a missing file = %q", got)
	}
}

// TestTheStateDirIsTheDaemons pins stateDirFor to the rules of
// daemon/statedir_unix.go and statedir_windows.go, Windows ignoring
// XDG_STATE_HOME included (the daemon's TestStateDir_IgnoresXDGStateHomeOnWindows).
func TestTheStateDirIsTheDaemons(t *testing.T) {
	home := func() (string, error) { return "H", nil }
	for _, c := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"windows", map[string]string{"LOCALAPPDATA": "L", "XDG_STATE_HOME": "X"}, filepath.Join("L", "mochiii")},
		{"windows", map[string]string{"XDG_STATE_HOME": "X"}, filepath.Join("H", "AppData", "Local", "mochiii")},
		{"linux", map[string]string{"XDG_STATE_HOME": "X", "LOCALAPPDATA": "L"}, filepath.Join("X", "mochiii")},
		{"linux", nil, filepath.Join("H", ".local", "state", "mochiii")},
		{"darwin", nil, filepath.Join("H", ".local", "state", "mochiii")},
	} {
		got, err := stateDirFor(c.goos, func(k string) string { return c.env[k] }, home)
		if err != nil || got != c.want {
			t.Errorf("stateDirFor(%s, %v) = %q, %v; want %q", c.goos, c.env, got, err, c.want)
		}
	}
}
