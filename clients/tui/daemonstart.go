package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mochiii/protocol"
)

// THE TERMINAL APP STARTS ITS OWN DAEMON (2026-10-17).
//
// Until now `mochiii` only connected. With no daemon running for the folder it
// printed "daemon not found ... start it with: mochiii-daemon" and exited, and
// only the developer script run-tui.sh started one. An installed copy has no
// such script, so this does what that script did: start the daemon for this
// folder, detached so it outlives the chat, and wait until it answers.
//
// WHICH DAEMON. Only the one installed beside this binary, found through this
// binary's own symlink-resolved path, and only with its models.json beside it,
// which is passed explicitly so a models.json in the project is never the one
// loaded. Never PATH, an environment variable or the workspace: those are how a
// repository or a stray PATH entry would get a program of its choosing run --
// the rule slash_hostileworkspace_test.go pins for every local command. A build
// with no daemon beside it (`go run`, a source checkout, run-tui.sh's cache)
// keeps the old behaviour exactly.

const daemonBinName = "mochiii-daemon"

// daemonStartTimeout bounds the wait for a freshly started daemon to answer.
// Startup includes starting the embedder helper, which loads a 33 MB model;
// 30 seconds is what run-tui.sh has always allowed.
const daemonStartTimeout = 30 * time.Second

// daemonStopTimeout bounds the wait for a stopped daemon to stop answering.
const daemonStopTimeout = 15 * time.Second

// daemonAlreadyServing is the daemon's exit code when another daemon already
// serves the same folder (daemon/exitcodes.go). It is not a failure: that
// other daemon is the one to talk to.
const daemonAlreadyServing = 3

// executableFileName is name as an executable file on goos.
func executableFileName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// installedDaemon returns the daemon and models.json installed beside this
// binary. ok is false when either is missing.
func installedDaemon() (daemon, config string, ok bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return installedDaemonIn(filepath.Dir(exe))
}

// installedDaemonIn is installedDaemon for a given directory. It looks in that
// directory and nowhere else.
func installedDaemonIn(dir string) (daemon, config string, ok bool) {
	daemon = filepath.Join(dir, executableFileName(daemonBinName, runtime.GOOS))
	config = filepath.Join(dir, "models.json")
	if !isRegularFile(daemon) || !isRegularFile(config) {
		return "", "", false
	}
	return daemon, config, true
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// daemonAnswers reports whether a daemon for this folder completes a
// handshake right now.
func daemonAnswers() bool {
	sess, err := connectToDaemon("mochiii-tui")
	if err != nil {
		return false
	}
	_ = sess.Close()
	return true
}

// stateDir is where this client keeps per-user state: the same directory the
// daemon uses (daemon/statedir_*.go) -- %LOCALAPPDATA%\mochiii on Windows, or
// ~\AppData\Local\mochiii without it and never XDG_STATE_HOME there;
// $XDG_STATE_HOME/mochiii or ~/.local/state/mochiii elsewhere.
func stateDir() (string, error) {
	return stateDirFor(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func stateDirFor(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	base := []string{".local", "state"}
	if goos == "windows" {
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "mochiii"), nil
		}
		base = []string{"AppData", "Local"}
	} else if d := getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "mochiii"), nil
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(append(append([]string{h}, base...), "mochiii")...), nil
}

// daemonLogPath is where a daemon this client starts writes its log: one file
// per folder, so two projects' logs never interleave.
func daemonLogPath(root string) string {
	dir, err := stateDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "logs", "daemon-"+protocol.WorkspaceTag(root)+".log")
}

// startInstalledDaemon starts the installed daemon for root and waits for it
// to answer.
func startInstalledDaemon(daemon, config, root string, status io.Writer) error {
	logPath := daemonLogPath(root)
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	say(status, "Starting Mochiii for %s…\n", root)
	return daemonStart{
		start: func() (<-chan error, error) {
			cmd := exec.Command(daemon, "--workspace", root, "--config", config, "--log-file", logPath)
			cmd.Dir = root
			cmd.SysProcAttr = detachedProcAttr()
			// stdio stays unset, which is the null device: the log file is the
			// daemon's channel, and a pipe to a process that outlives this one
			// would block it the day the pipe filled.
			if err := cmd.Start(); err != nil {
				return nil, fmt.Errorf("starting %s: %w", daemon, err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			return exited, nil
		},
		answers: daemonAnswers,
		logPath: logPath,
		timeout: daemonStartTimeout,
		poll:    150 * time.Millisecond,
	}.run()
}

// daemonStart is the start-and-wait logic, with its process and its probe
// passed in so the waiting can be tested without a real daemon.
type daemonStart struct {
	start   func() (exited <-chan error, err error)
	answers func() bool
	logPath string
	timeout time.Duration
	poll    time.Duration
}

func (d daemonStart) run() error {
	exited, err := d.start()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(d.timeout)
	for {
		if d.answers() {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("Mochiii's background service did not answer within %s; its log is %s", d.timeout, d.logPath)
		}
		select {
		case err := <-exited:
			if exitCode(err) == daemonAlreadyServing {
				// Another daemon took this folder first. Keep waiting for it.
				exited = nil
				continue
			}
			return fmt.Errorf("Mochiii's background service stopped while starting (%v). The end of its log, %s:\n%s",
				err, d.logPath, logTail(d.logPath, 15))
		case <-time.After(d.poll):
		}
	}
}

// exitCode is a finished process's exit code, or -1 when err says nothing
// about one. Any error with an ExitCode method counts, *exec.ExitError among
// them.
func exitCode(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}

// logTail returns the last n lines of the file at path, indented.
func logTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "  (no log was written)"
	}
	defer func() { _ = f.Close() }() // read-only: closing loses nothing
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if len(lines) == 0 {
		return "  (the log is empty)"
	}
	return "  " + strings.Join(lines, "\n  ")
}

// errNoDaemonRunning is stopDaemon's answer when nothing serves the folder.
var errNoDaemonRunning = errors.New("no Mochiii background service is running for this folder")

// stopDaemon stops the daemon serving this folder and waits until it no longer
// answers.
//
// THE PID COMES FROM THE DAEMON ITSELF, over the connection the lockfile names
// and after a completed handshake -- never from the lockfile alone, whose PID
// can be stale and by now belong to an unrelated process.
func stopDaemon(status io.Writer) error {
	sess, err := connectToDaemon("mochiii-tui")
	if err != nil {
		return errNoDaemonRunning
	}
	if err := sess.enc.Encode(protocol.StatusRequest{ProtocolVersion: protocol.ProtocolVersion, Status: true}); err != nil {
		_ = sess.Close()
		return fmt.Errorf("asking the background service for its process: %w", err)
	}
	var resp protocol.StatusResponse
	err = sess.dec.Decode(&resp)
	_ = sess.Close()
	if err != nil {
		return fmt.Errorf("reading the background service's status: %w", err)
	}
	if resp.PID <= 0 {
		return errors.New("the background service did not report its process id")
	}
	proc, err := os.FindProcess(resp.PID)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", resp.PID, err)
	}
	say(status, "Stopping Mochiii's background service for this folder (process %d)…\n", resp.PID)
	if err := terminateProcess(proc); err != nil {
		return fmt.Errorf("stopping process %d: %w", resp.PID, err)
	}
	deadline := time.Now().Add(daemonStopTimeout)
	for daemonAnswers() {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("process %d was asked to stop and is still answering after %s", resp.PID, daemonStopTimeout)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}
