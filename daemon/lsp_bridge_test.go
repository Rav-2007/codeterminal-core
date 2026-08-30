package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/editapply"
)

// lsp_bridge.go shipped with every function at 0.0% coverage, and it is not a
// leaf: main.go:294 builds a bridge for every daemon, and two model-facing
// tools call into it.
//
// These tests drive it through the door the daemon uses. GetServer resolves
// "gopls" through PATH, so the harness builds testdata/fakelsp, installs it on
// PATH under that name, and lets the real exec.Command find it. Real process,
// real pipes, real Content-Length framing. Nothing is stubbed — which is the
// whole lesson of this campaign, where six sandbox tests passed against a
// sandbox that had never once executed.

var fakeLSPOnce struct {
	sync.Once
	dir string
	err error
}

// buildFakeLSP compiles testdata/fakelsp into a directory suitable for PATH,
// installed under each language-server name the bridge knows.
func buildFakeLSP(t *testing.T) string {
	t.Helper()
	fakeLSPOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakelsp")
		if err != nil {
			fakeLSPOnce.err = err
			return
		}
		bin := filepath.Join(dir, exeName("gopls"))
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakelsp").CombinedOutput()
		if err != nil {
			fakeLSPOnce.err = err
			t.Logf("build output: %s", out)
			return
		}
		fakeLSPOnce.dir = dir
	})
	if fakeLSPOnce.err != nil {
		t.Fatalf("building the fake language server: %v", fakeLSPOnce.err)
	}
	return fakeLSPOnce.dir
}

// newFakeBridge returns a bridge whose "go" server is the fake, running in the
// given mode, with HOME pointed at a scratch directory the fake uses for its
// mode file and environment dump.
func newFakeBridge(t *testing.T, mode string) (*LSPBridge, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is driven through a POSIX-shaped PATH and HOME; NOT RUN on Windows")
	}
	binDir := buildFakeLSP(t)

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}

	// Both variables matter twice over: the child is found through PATH, and
	// mcp.ServerEnv reads the PARENT's PATH and HOME to build the child's
	// environment. Setting them here is what makes the fake reachable at all.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)

	ws := t.TempDir()
	b := NewLSPBridge(ws)
	t.Cleanup(b.Close)
	return b, home
}

// THE PANIC. A language server that sends a negative Content-Length used to
// reach make([]byte, -1) in readLoop, which panics — and readLoop runs in a
// bare `go` statement, so an unrecovered panic there terminates the entire
// daemon process. handleConn's recover is on a different goroutine and cannot
// see it.
//
// Neuter check: restore `body := make([]byte, l)` without the length gate in
// readHeaders and this test does not fail, it CRASHES THE TEST BINARY with
// "panic: runtime error: makeslice: len out of range".
func TestLSPServer_NegativeContentLengthDoesNotPanicTheDaemon(t *testing.T) {
	b, _ := newFakeBridge(t, "negative-length")

	srv, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake should still succeed in this mode: %v", err)
	}

	// The hostile frame arrives in answer to this call.
	_, err = srv.Call("textDocument/definition", map[string]any{})
	if err == nil {
		t.Fatal("a negative Content-Length was accepted; it must end the stream")
	}
	if !strings.Contains(err.Error(), "no longer running") {
		t.Errorf("got %v, want the stream to have been abandoned", err)
	}

	// Surviving is the assertion: reaching this line at all means the panic did
	// not happen, because a panic in readLoop would have taken this process
	// down before the assertion above could run.
	if srv.alive() {
		t.Error("the stream should have been abandoned after an impossible frame")
	}
}

// THE ALLOCATION. Content-Length was the sole input to a heap allocation, with
// no ceiling. 17179869184 asks for 16 GiB from a process the user never
// authorised beyond "this repo has .go files in it".
//
// The header is rejected on its face, so no allocation is attempted and the
// test is safe to run anywhere.
func TestLSPServer_HugeContentLengthIsRefusedBeforeAllocating(t *testing.T) {
	b, _ := newFakeBridge(t, "huge-length")

	srv, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	done := make(chan error, 1)
	go func() { _, e := srv.Call("textDocument/definition", map[string]any{}); done <- e }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a 16 GiB Content-Length was accepted")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call neither completed nor failed; the header was not refused")
	}
}

// The cap must bound a hostile server WITHOUT breaking an honest one that has a
// lot to say. Both sides of the boundary, so a future change that fixes the
// first by breaking the second is caught.
func TestLSPServer_MessageCapBoundary(t *testing.T) {
	t.Run("oversize body is refused", func(t *testing.T) {
		b, _ := newFakeBridge(t, "oversize-body")
		srv, err := b.GetServer("go")
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		if _, err := srv.Call("textDocument/definition", map[string]any{}); err == nil {
			t.Fatal("a body over maxLSPMessageBytes was accepted")
		}
	})

	t.Run("large but legal body is delivered", func(t *testing.T) {
		b, _ := newFakeBridge(t, "atcap-body")
		srv, err := b.GetServer("go")
		if err != nil {
			t.Fatalf("handshake: %v", err)
		}
		res, err := srv.Call("textDocument/definition", map[string]any{})
		if err != nil {
			t.Fatalf("a 64 KiB reply is well within the cap and must be delivered: %v", err)
		}
		if len(res) < 64*1024 {
			t.Errorf("reply was truncated: %d bytes", len(res))
		}
	})
}

// THE HANG. Call used to block on a receive with no other case, so a server
// that accepted a request and never answered wedged the caller forever — for
// the life of the process, holding the user's turn open.
func TestLSPServer_SilentServerTimesOutRatherThanHanging(t *testing.T) {
	b, _ := newFakeBridge(t, "silent")

	srv, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, err = srv.CallContext(ctx, "textDocument/definition", map[string]any{})
	if err == nil {
		t.Fatal("a silent server produced a successful call")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("got %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s to give up", elapsed)
	}

	// The pending entry must be gone, or every timed-out request leaks a
	// channel and a map slot for the life of the daemon.
	srv.mu.Lock()
	n := len(srv.pending)
	srv.mu.Unlock()
	if n != 0 {
		t.Errorf("%d pending entries survived a timeout; the map leaks", n)
	}
}

// THE DEADLOCK, and the sharpest of the three. GetServer holds b.mu across the
// initialize handshake. When that handshake was unbounded, one unresponsive
// server did not just break its own language — it held the bridge mutex
// forever, so EVERY language deadlocked for the life of the daemon.
//
// Go's own deadlock detector cannot catch this: the goroutines are blocked on a
// live channel and a live mutex, not a provably dead one.
func TestLSPBridge_UnresponsiveServerDoesNotDeadlockOtherLanguages(t *testing.T) {
	b, _ := newFakeBridge(t, "silent-init")

	// First caller wedges on the handshake for as long as lspInitTimeout.
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = b.GetServer("go")
	}()

	// Second caller wants an entirely different language. It must not be held
	// hostage indefinitely by the first.
	second := make(chan error, 1)
	go func() {
		_, err := b.GetServer("nonexistent-language")
		second <- err
	}()

	select {
	case err := <-second:
		if err == nil {
			t.Fatal("an unsupported language returned a server")
		}
	case <-time.After(lspInitTimeout + 15*time.Second):
		t.Fatal("a second language deadlocked behind an unresponsive server's handshake")
	}
	<-first
}

// A server that dies must fail its callers immediately, not leave them waiting
// for a timeout that will never produce an answer.
func TestLSPServer_DeadServerFailsCallersImmediately(t *testing.T) {
	b, _ := newFakeBridge(t, "die")

	srv, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	start := time.Now()
	if _, err := srv.Call("textDocument/definition", map[string]any{}); err == nil {
		t.Fatal("a call to a dead server succeeded")
	}
	if elapsed := time.Since(start); elapsed > lspCallTimeout {
		t.Errorf("waited %s for a server that had already exited", elapsed)
	}
}

// THE CREDENTIAL ASSERTION, made from inside a real child process rather than
// from source. The fake dumps its own environment; nothing that could pay for
// inference may appear in it.
//
// That this test has to route its own configuration through $HOME/mode, because
// an LSPFAKE_MODE variable would itself be scrubbed away, is the fix working.
func TestLSPServer_ChildProcessNeverReceivesCredentials(t *testing.T) {
	// Set the real credential names in the parent, exactly as a running daemon
	// holds them, then prove none crosses the exec boundary.
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-CANARY-must-not-leak")
	t.Setenv("CODETERMINAL_MOCHIII_KEY", "mochi_CANARY-must-not-leak")
	t.Setenv("CODETERMINAL_API_KEY", "CANARY-must-not-leak")

	b, home := newFakeBridge(t, "ok")
	if _, err := b.GetServer("go"); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	dump, err := os.ReadFile(filepath.Join(home, "env.dump"))
	if err != nil {
		t.Fatalf("the child did not record its environment: %v", err)
	}
	got := string(dump)

	for _, name := range []string{"OPENROUTER_API_KEY", "CODETERMINAL_MOCHIII_KEY", "CODETERMINAL_API_KEY"} {
		if strings.Contains(got, name) {
			t.Errorf("%s crossed into the language server process", name)
		}
	}
	if strings.Contains(got, "CANARY") {
		t.Error("a credential VALUE reached the language server process")
	}
	// The scrubber is an allow-list, not a deny-list: prove it passed what a
	// server legitimately needs, so a future change cannot "fix" this test by
	// passing nothing at all.
	if !strings.Contains(got, "PATH=") {
		t.Error("PATH was not passed; the server could not find its own toolchain")
	}
}

// A path with a space or a '#' used to be concatenated into a file:// URI
// unescaped, and a Windows path produced something that was not a URI at all.
func TestFileURI(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/home/u/src/main.go", "file:///home/u/src/main.go"},
		{"/home/u/my code/main.go", "file:///home/u/my%20code/main.go"},
		{"/home/u/c#/main.go", "file:///home/u/c%23/main.go"},
		{"/home/u/a?b/main.go", "file:///home/u/a%3Fb/main.go"},
		{"relative/path.go", "file:///relative/path.go"},
	} {
		if got := fileURI(tc.in); got != tc.want {
			t.Errorf("fileURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Separator handling is platform-specific and must not be faked.
//
// filepath.ToSlash rewrites backslashes only on Windows, and that is correct
// both ways round: on POSIX a backslash is an ordinary, legal filename
// character, so escaping it preserves the name; on Windows it is the separator,
// so converting it produces the path component a file URI requires. Asserting
// the Windows answer from Linux would be asserting a lie.
func TestFileURI_SeparatorsArePlatformSpecific(t *testing.T) {
	got := fileURI(`C:\src\main.go`)
	if runtime.GOOS == "windows" {
		if got != "file:///C:/src/main.go" {
			t.Errorf("fileURI = %q, want %q", got, "file:///C:/src/main.go")
		}
		return
	}
	// POSIX: `C:\src\main.go` is one relative filename containing backslashes.
	if got != "file:///C:%5Csrc%5Cmain.go" {
		t.Errorf("fileURI = %q; on POSIX a backslash is part of the NAME and must be escaped, not converted", got)
	}
	t.Log("the Windows separator conversion is NOT RUN on this platform")
}

func TestServerCommand(t *testing.T) {
	for _, tc := range []struct {
		lang editapply.Language
		want string
	}{
		{editapply.LangGo, "gopls"},
		{editapply.LangTypeScript, "typescript-language-server"},
		{editapply.LangJavaScript, "typescript-language-server"},
		{editapply.LangPython, "pyright-langserver"},
	} {
		got, err := serverCommand(tc.lang)
		if err != nil || got != tc.want {
			t.Errorf("serverCommand(%q) = %q, %v; want %q", tc.lang, got, err, tc.want)
		}
	}
	if _, err := serverCommand(editapply.LangUnknown); err == nil {
		t.Error("LangUnknown returned a command; a file we could not identify must not " +
			"be routed to any language server -- that IS the bug this type exists to prevent")
	}
}

// THE SUPPORTED-LANGUAGE MESSAGE MUST NOT LIE.
//
// lspServerForFile refuses an unrecognised file by listing what IS covered, and
// it builds that list from editapply.KnownLanguages(). If a language can be
// named by the table but has no server binary behind it, that sentence promises
// a capability this daemon does not have -- and the user is sent to try
// something that cannot work. Adding a language to the table is therefore a
// decision about this bridge too, and this is where the build says so.
func TestEveryKnownLanguageHasAServerCommand(t *testing.T) {
	langs := editapply.KnownLanguages()
	if len(langs) == 0 {
		t.Fatal("KnownLanguages() is empty; this test would assert nothing")
	}
	for _, lang := range langs {
		cmd, err := serverCommand(lang)
		if err != nil {
			t.Errorf("KnownLanguages() names %q but serverCommand has no binary for it: %v. "+
				"Either give it one, or drop it from the table -- the refusal message "+
				"tells users this language is covered.", lang, err)
			continue
		}
		if cmd == "" {
			t.Errorf("serverCommand(%q) returned an empty command with no error", lang)
		}
	}
}

// A missing binary is the common case for a user without gopls installed. It
// must be a clean error naming the cause, not a hang or a panic.
func TestLSPBridge_MissingBinaryIsAClearError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := NewLSPBridge(t.TempDir())
	defer b.Close()

	_, err := b.GetServer("go")
	if err == nil {
		t.Fatal("a server was returned with no binary on PATH")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("got %v, want an error mentioning PATH", err)
	}
}

// Close must be safe on a server that never started, and safe twice. The
// original killed s.cmd.Process unconditionally, which is a nil dereference
// when Start failed.
func TestLSPServer_CloseIsSafeAndIdempotent(t *testing.T) {
	var s LSPServer
	s.done = make(chan struct{})
	s.Close()
	s.Close() // doneOnce must absorb the second close, not panic on a closed channel

	b, _ := newFakeBridge(t, "ok")
	srv, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	srv.Close()
	srv.Close()
	b.Close()
	b.Close()
}

// A cached server that has since died must not be handed out again, or the
// language stays broken for the life of the daemon.
func TestLSPBridge_ReplacesADeadCachedServer(t *testing.T) {
	b, _ := newFakeBridge(t, "ok")

	first, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	first.Close()

	second, err := b.GetServer("go")
	if err != nil {
		t.Fatalf("second GetServer: %v", err)
	}
	if second == first {
		t.Error("a dead server was handed out from the cache")
	}
	if !second.alive() {
		t.Error("the replacement is not alive")
	}
}

// The fake asserts on a boundary defined by the daemon; keep the two in sync.
func TestFakeLSPCapConstantMatchesDaemon(t *testing.T) {
	src, err := os.ReadFile("testdata/fakelsp/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "maxLSPMessageBytes = 8 * 1024 * 1024") {
		t.Error("testdata/fakelsp's cap constant has drifted from maxLSPMessageBytes")
	}
}
