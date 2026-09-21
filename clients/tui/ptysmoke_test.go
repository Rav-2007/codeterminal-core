//go:build linux

// A REAL BINARY, IN A REAL TERMINAL, TALKING TO A REAL SOCKET.
//
// Linux-only, and deliberately: allocating a pty is per-kernel (Linux uses
// TIOCSPTLCK/TIOCGPTN, the BSDs use TIOCPTYUNLK/TIOCPTYGNAME), and a portable
// version means a second implementation nobody runs. One platform that actually
// executes beats two that are skipped.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"mochiii/protocol"
)

// THE TWO BUGS THIS CLASS SHIPPED, AND WHY 185 TESTS MISSED BOTH.
//
// 1. The client could not reach ANY daemon for three weeks. The lockfile went
//    per-workspace; this package kept calling protocol.LockPath(). Every run
//    failed "daemon not found", naming a path nothing writes.
// 2. A terminal too narrow (or one reporting no size at all) panicked the
//    client on its first render, inside bubbles/textinput.
//
// Both are now pinned by unit tests -- lockpath_test.go compares the real
// derivation against the daemon's, narrowterminal_test.go sweeps inputWidthFor
// -- and BOTH OF THOSE TESTS WERE WRITTEN AFTER THE FACT, because the failure
// was only ever visible when the binary ran. Every test in this package drives
// the Bubble Tea model in memory with lockPathFunc substituted; nothing starts
// the program, and a program that cannot start passes all of them.
//
// This is that gap: build it, give it a terminal, and require it to draw.

// ptyPair allocates a pseudo-terminal and returns the master and the slave's
// path. The master is the test's end; the slave becomes the child's console.
func ptyPair(t *testing.T) (master *os.File, slaveName string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("unlocking the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(fd)
		t.Fatalf("naming the pty: %v", err)
	}
	return os.NewFile(uintptr(fd), "ptmx"), fmt.Sprintf("/dev/pts/%d", n)
}

func setWinsize(t *testing.T, f *os.File, rows, cols uint16) {
	t.Helper()
	if err := unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
		t.Fatalf("setting the window size: %v", err)
	}
}

// buildTUI compiles the client under test. Built fresh from source, the same
// convention the MCP suite uses, so this exercises current code rather than
// whatever binary happens to be lying around -- but built ONCE for the package,
// not once per test: the binary is identical for every test here and each build
// is a second of wall clock spent proving nothing.
var tuiBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "tuibin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "mochiii")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the client: %w\n%s", err, out)
	}
	return bin, nil
})

func buildTUI(t *testing.T) string {
	t.Helper()
	bin, err := tuiBinary()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// fakeDaemonForPTY is a real listener with a real per-workspace lockfile, so
// the client has to derive the path the way production does. NOTHING here
// substitutes lockPathFunc -- that substitution is exactly what hid bug 1.
//
// The runtime dir is short on purpose: a Unix socket path is capped near 104
// bytes by sockaddr_un, and t.TempDir()'s name plus a socket name has measured
// over that on this project's own scratch paths.
func fakeDaemonForPTY(t *testing.T, workspace string) {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("/tmp", "ctp")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	sockDir := filepath.Join(runtimeDir, "mochiii")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	addr := protocol.Address{Transport: protocol.TransportUnix, Address: filepath.Join(sockDir, "d.sock")}
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	lock, err := json.Marshal(protocol.LockFile{SocketPath: addr.Address, PID: os.Getpid(), Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	// LockPathFor, computed independently here, is what pins the client's own
	// derivation: if the two ever drift again, the client finds nothing.
	if err := os.WriteFile(protocol.LockPathFor(workspace), lock, 0o600); err != nil {
		t.Fatalf("writing the lockfile: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				if enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}) != nil {
					return
				}
				var req protocol.PromptRequest
				if dec.Decode(&req) != nil {
					return
				}
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: "pong"})
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
			}()
		}
	}()
}

// screen drives the child's terminal: it drains output and answers the two
// queries Bubble Tea makes on startup. WITHOUT THESE ANSWERS IT NEVER DRAWS --
// it waits on the background-colour reply, and a test that did not answer would
// fail with an empty screen and no hint why.
type screen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *screen) pump(master *os.File) {
	chunk := make([]byte, 4096)
	for {
		n, err := master.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			s.mu.Lock()
			s.buf.Write(data)
			s.mu.Unlock()
			if strings.Contains(string(data), "\x1b]11;?") {
				_, _ = master.Write([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
			}
			if strings.Contains(string(data), "\x1b[6n") {
				_, _ = master.Write([]byte("\x1b[1;1R"))
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// size is how many bytes the client has written so far. Output growing is the
// only evidence available here that the client rendered: at two columns there
// is nothing recognisable on screen to match against.
func (s *screen) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

// waitForQuiet blocks until the client has written something and then stopped
// writing for quiet, or until timeout. It returns whether the client ever drew.
//
// THIS REPLACED A time.Sleep, and the sleep was not just slow but unsound: it
// assumed 1.5 seconds was enough for a build-cold client to start and paint,
// and on a machine where it was not, the keystroke meant to force the render
// that panics went nowhere and the test passed having tested nothing.
func (s *screen) waitForQuiet(quiet, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	last, stableSince := -1, time.Now()
	for time.Now().Before(deadline) {
		n := s.size()
		if n != last {
			last, stableSince = n, time.Now()
		} else if n > 0 && time.Since(stableSince) >= quiet {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.size() > 0
}

func (s *screen) waitFor(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(s.text(), want) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// startTUI builds, launches under a pty at the given size, and returns the
// screen plus the running command.
func startTUI(t *testing.T, rows, cols uint16) (*screen, *exec.Cmd, *os.File) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary and starts a subprocess")
	}

	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildTUI(t)
	fakeDaemonForPTY(t, real)

	master, slaveName := ptyPair(t)
	setWinsize(t, master, rows, cols)

	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening the pty slave: %v", err)
	}

	cmd := exec.Command(bin, "--workspace", real)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Its own session with the pty as controlling terminal, which is what makes
	// isatty true for the child and lets it size itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the client: %v", err)
	}
	_ = slave.Close() // the child owns it now

	sc := &screen{}
	go sc.pump(master)

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = master.Close()
	})
	return sc, cmd, master
}

// IT STARTS, IT FINDS THE DAEMON, IT DRAWS. Bug 1 fails this at "daemon not
// found" before a single cell is painted.
func TestTheClientStartsAndDrawsAgainstARealDaemon(t *testing.T) {
	sc, _, master := startTUI(t, 30, 100)

	if !sc.waitFor("Mochiii", 20*time.Second) {
		t.Fatalf("the client never drew its own name. Screen so far:\n%s", sc.text())
	}
	// The splash swallows the first key, so this dismisses it and the next
	// keystrokes reach the input.
	_, _ = master.Write([]byte("x\x7f"))

	if !sc.waitFor(helpText, 10*time.Second) {
		t.Fatalf("the idle footer never appeared, so the chat UI did not come up. Screen:\n%s", sc.text())
	}
	if strings.Contains(sc.text(), "daemon not found") {
		t.Error("the client could not reach a daemon whose lockfile is exactly where " +
			"protocol.LockPathFor says it is -- the derivation has drifted again")
	}
	if strings.Contains(sc.text(), "panic:") {
		t.Errorf("the client panicked:\n%s", sc.text())
	}
}

// A TERMINAL TOO NARROW TO DRAW IN MUST NOT BE A CRASH. narrowterminal_test.go
// pins the arithmetic; this pins the program, which is where it actually died.
//
// TWO COLUMNS, not twelve, and the difference is the whole test: the input
// width is terminalWidth minus the prompt minus its padding, so twelve columns
// still yields a comfortable eight and reproduces nothing. It has to be narrow
// enough to go NEGATIVE -- which is what a pty reporting no size at all does,
// and how this was found. Measured: at 12 columns the neutered floor passes
// this test; at 2 it panics inside bubbles/textinput and takes the client with
// it.
//
// Nothing is asserted about what was drawn. At two columns there is barely
// anything to draw, and requiring particular output would be asserting the
// shape of a rendering nobody will ever read. The property is that the program
// SURVIVES a size it cannot lay out.
func TestANarrowTerminalDoesNotKillTheClient(t *testing.T) {
	sc, cmd, master := startTUI(t, 4, 2)

	// Wait for the first render to land and settle, then dismiss the splash --
	// which is what puts the INPUT on screen, and the input is what panicked --
	// then wait for that second render to settle too. Waiting on the output
	// rather than on the clock is what makes the keystroke provably arrive
	// after something was drawn.
	if !sc.waitForQuiet(250*time.Millisecond, 20*time.Second) {
		t.Fatalf("the client never wrote a byte at 2 columns; nothing was rendered to survive")
	}
	before := sc.size()
	_, _ = master.Write([]byte("x"))
	sc.waitForQuiet(250*time.Millisecond, 10*time.Second)
	if sc.size() == before && cmd.Process.Signal(syscall.Signal(0)) == nil {
		t.Fatalf("the client drew nothing after the splash was dismissed, so the "+
			"input was never rendered and this test proves nothing. Screen:\n%s", sc.text())
	}

	if strings.Contains(sc.text(), "panic:") {
		t.Fatalf("a narrow terminal panicked the client:\n%s", sc.text())
	}
	// Still running after being asked to render at a size that used to kill it.
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the client is gone after rendering at 2 columns: %v\nScreen:\n%s", err, sc.text())
	}
}
