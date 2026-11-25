//go:build linux

package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"mochiii/protocol"
)

// PASTING INTO THE PROMPT, IN A REAL TERMINAL (2026-11-24).
//
// A terminal with bracketed paste (every one Mochiii is used in: GNOME
// Terminal, VS Code's, iTerm2, Windows Terminal) wraps a paste in ESC[200~ and
// ESC[201~. Bubble Tea turns that into one paste, so the newlines inside it are
// text and never Enter: a multi-line paste must not send each line as a
// message. A paste longer than the box holds is cut, and the prompt says so.
//
// These write the bytes a terminal writes, into a pty, to the built client,
// against a daemon that records every question it is sent.

// fakeDaemonRecordingPrompts is fakeDaemonForPTYSaying's daemon, keeping every
// prompt it is sent and answering each with one word.
func fakeDaemonRecordingPrompts(t *testing.T, workspace string) <-chan string {
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
	if err := os.WriteFile(protocol.LockPathFor(workspace), lock, 0o600); err != nil {
		t.Fatalf("writing the lockfile: %v", err)
	}
	prompts := make(chan string, 64)
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
				var raw map[string]json.RawMessage
				if dec.Decode(&raw) != nil {
					return
				}
				// Only questions: the client also asks for its chat, its models
				// and the like on connections of their own.
				if p, ok := raw["prompt"]; ok {
					var text string
					_ = json.Unmarshal(p, &text)
					prompts <- text
				}
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: "pong"})
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
			}()
		}
	}()
	return prompts
}

// startTUIRecording is startTUI against that daemon, at the prompt and ready.
func startTUIRecording(t *testing.T) (*screen, *os.File, <-chan string) {
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
	prompts := fakeDaemonRecordingPrompts(t, real)
	master, slaveName := ptyPair(t)
	setWinsize(t, master, 30, 120)
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening the pty slave: %v", err)
	}
	cmd := exec.Command(bin, "--workspace", real)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the client: %v", err)
	}
	_ = slave.Close()
	sc := &screen{}
	go sc.pump(master)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = master.Close()
	})
	if !sc.waitFor("Mochiii", 20*time.Second) {
		t.Fatalf("the client never drew. Screen:\n%s", sc.text())
	}
	_, _ = master.Write([]byte("x\x7f")) // past a splash, if there is one
	if !sc.waitFor(helpText, 10*time.Second) {
		t.Fatalf("the prompt never came up. Screen:\n%s", sc.text())
	}
	if !strings.Contains(sc.text(), "\x1b[?2004h") {
		t.Fatal("the client did not turn on bracketed paste, so a terminal would type a paste's newlines as Enter")
	}
	sc.waitForQuiet(300*time.Millisecond, 3*time.Second)
	return sc, master, prompts
}

func noPromptFor(t *testing.T, prompts <-chan string, d time.Duration, what string) {
	t.Helper()
	select {
	case p := <-prompts:
		t.Fatalf("%s sent a question: %q", what, p)
	case <-time.After(d):
	}
}

func onePrompt(t *testing.T, prompts <-chan string) string {
	t.Helper()
	select {
	case p := <-prompts:
		noPromptFor(t, prompts, 800*time.Millisecond, "one Enter")
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("Enter sent nothing")
	}
	return ""
}

func TestAMultiLinePasteIsOneQuestionInARealTerminal(t *testing.T) {
	sc, master, prompts := startTUIRecording(t)
	_, _ = master.Write([]byte("\x1b[200~first line\nsecond line\r\nthird line\x1b[201~"))
	noPromptFor(t, prompts, 1500*time.Millisecond, "the paste's newlines")
	if !sc.waitFor("third line", 5*time.Second) {
		t.Fatalf("the paste is not in the prompt. Screen:\n%s", sc.text())
	}
	_, _ = master.Write([]byte("\r"))
	got := onePrompt(t, prompts)
	for _, line := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(got, line) {
			t.Errorf("the question lost %q: %q", line, got)
		}
	}
}

func TestA50KBPasteIsCutToTheBoxAndSaysSoInARealTerminal(t *testing.T) {
	sc, master, prompts := startTUIRecording(t)
	big := strings.Repeat(strings.Repeat("0123456789abcdef", 4)+"\n", 788) // 51,220 characters
	_, _ = master.Write([]byte("\x1b[200~" + big + "\x1b[201~"))
	noPromptFor(t, prompts, 2*time.Second, "a 50 KB paste")
	noticed := sc.waitFor("pasted text was cut to 4,000 characters", 10*time.Second)
	_, _ = master.Write([]byte("\r"))
	got := onePrompt(t, prompts)
	if !noticed {
		t.Errorf("a 50 KB paste was cut to %d characters without a word (cut seen: %v, kept seen: %v)", len([]rune(got)),
			strings.Contains(sc.text(), "cut"), strings.Contains(sc.text(), "kept the first"))
	}
	if n := len([]rune(got)); n != 4000 {
		t.Errorf("the question is %d characters; the box holds 4,000", n)
	}
}
