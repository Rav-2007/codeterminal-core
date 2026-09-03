//go:build linux

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"codeterminal/protocol"
)

// THE END-TO-END PROOF, AND THE ONLY ONE THAT READS REAL TERMINAL BYTES.
//
// Every other test in this package drives the model in memory and inspects a
// string. This one builds the binary, gives it a pty, puts a hostile daemon on
// the other end, and then reads what actually arrived at the terminal. The
// difference matters: a frame returned by View() is not proof that the same
// bytes reached a screen, and this package has already shipped one bug --
// twice -- that only existed once the program ran.
//
// The payloads carry distinctive markers rather than being checked with the
// general escape scanner, because the stream legitimately contains Bubble
// Tea's own escapes (alt-screen, cursor moves, SGR from our styles). What is
// asserted is that the ATTACKER'S bytes are not in it, and that the one
// allowed sequence still is.
func hostileDaemonForPTY(t *testing.T, workspace, payload string) {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("/tmp", "cth")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	sockDir := filepath.Join(runtimeDir, "codeterminal")
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
				// ONE BYTE PER MESSAGE. The far end picks the split points, so
				// the test picks the worst ones: every sequence in the payload
				// is cut across as many messages as it has bytes.
				for i := 0; i < len(payload); i++ {
					if enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: payload[i : i+1]}) != nil {
						return
					}
				}
				_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
			}()
		}
	}()
}

func TestHostileAnswerNeverReachesARealTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary and starts a subprocess")
	}
	const (
		titleMarker = "PWNED_TITLE_MARKER"
		urlMarker   = "PWNED_URL_MARKER"
		survives    = "\x1b[38;2;17;34;51m" // a truecolour SGR, which is allowed
	)
	payload := "ANSWER_BEGIN" +
		"\x1b]0;" + titleMarker + "\x07" + // rewrite the window title
		"\x1b]8;;http://evil.invalid/" + urlMarker + "\x07link\x1b]8;;\x07" + // hyperlink
		"\x1b]52;c;UFdORUQ=\x07" + // write the system clipboard
		"\x1b[9;13r" + // lock the scroll region
		"\x1b[8mconcealed\x1b[28m" + // conceal, a spoofing primitive
		survives + "coloured\x1b[0m" +
		"ANSWER_END"

	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildTUI(t)
	hostileDaemonForPTY(t, real, payload)

	master, slaveName := ptyPair(t)
	setWinsize(t, master, 40, 100)
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening the pty slave: %v", err)
	}
	cmd := exec.Command(bin, "--workspace", real)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
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
		t.Fatalf("the client never drew. Screen:\n%q", sc.text())
	}
	_, _ = master.Write([]byte("x\x7f"))
	if !sc.waitFor(helpText, 15*time.Second) {
		t.Fatalf("never reached idle. Screen:\n%q", sc.text())
	}
	_, _ = master.Write([]byte("say something\r"))
	if !sc.waitFor("ANSWER_END", 20*time.Second) {
		t.Fatalf("the answer never arrived. Screen:\n%q", sc.text())
	}
	sc.waitForQuiet(500*time.Millisecond, 10*time.Second)

	got := sc.text()
	for _, bad := range []struct{ name, seq string }{
		{"OSC 0 window title", titleMarker},
		{"OSC 8 hyperlink target", urlMarker},
		{"OSC 52 clipboard write", "]52;"},
		{"DECSTBM scroll region", "\x1b[9;13r"},
		{"SGR 8 conceal", "\x1b[8m"},
	} {
		if strings.Contains(got, bad.seq) {
			t.Errorf("%s reached the terminal", bad.name)
		}
	}
	// The text itself must still be there, and so must the allowed colour --
	// a filter that ate the answer would pass every check above.
	if !strings.Contains(got, "concealed") {
		t.Error("the answer text was lost along with the escape")
	}
	if !strings.Contains(got, survives) {
		t.Error("an allowed truecolour SGR was stripped; the allowlist is wrong")
	}
}
