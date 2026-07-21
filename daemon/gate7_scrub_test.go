package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// Gate 7 (FAIL-3) response-hygiene regression tests. They pin the socket
// boundary's scrub: error responses must not carry a daemon-side ABSOLUTE host
// path (which encodes home-dir layout / username / machine structure), while
// staying debuggable via the workspace-relative tail. Each assertion below
// fails if the scrub is reverted — the "<workspace>" token disappears and the
// raw absolute root reappears — so these are fail-when-neutered guards, not
// smoke tests.

// resolvedRoots returns the two absolute forms of ws that a scrubbed message
// must contain NEITHER of: ws itself and its symlink-resolved real form (they
// differ when the temp dir sits under a symlinked prefix, e.g. /tmp on macOS).
func resolvedRoots(t *testing.T, ws string) []string {
	t.Helper()
	roots := []string{ws}
	if real, err := editapply.ResolveRealWorkspaceRoot(ws); err == nil && real != ws {
		roots = append(roots, real)
	}
	return roots
}

// assertNoAbsRoot is the Gate-7 guarantee itself: whatever the message says, it
// must not carry an absolute host path. Applied to every error response.
func assertNoAbsRoot(t *testing.T, field, msg string, roots []string) {
	t.Helper()
	if msg == "" {
		t.Errorf("%s is empty; a scrubbed error must still be informative, not blank", field)
	}
	for _, r := range roots {
		if strings.Contains(msg, r) {
			t.Errorf("%s = %q\n  leaks absolute host path %q — scrub did not strip it", field, msg, r)
		}
	}
}

// assertScrubbedPath is the stronger assertion, for messages that inherently
// DO carry a path (a syscall error naming the file it failed on). There the
// "<workspace>" token is the positive proof that the scrub ran, rather than the
// path merely happening to be absent.
//
// It is deliberately not applied to every case any more. Fix 7 removed the
// absolute path from the not-found errors at the source: they used to be raw
// EvalSymlinks lstat faults ("lstat /abs/path/ghost.go: no such file or
// directory") that the scrub then had to rewrite, and are now considered
// refusals phrased in the caller's own relative path. Requiring a scrub token
// in a message that never contained a path would be requiring the daemon to
// mention the workspace for no reason — a message with no path in it is the
// stronger outcome, and assertNoAbsRoot still proves it.
func assertScrubbedPath(t *testing.T, field, msg string, roots []string) {
	t.Helper()
	assertNoAbsRoot(t, field, msg, roots)
	if !strings.Contains(msg, workspacePathToken) {
		t.Errorf("%s = %q\n  want the %q token (proves the path was scrubbed, not just absent)", field, msg, workspacePathToken)
	}
}

func TestHandleApplyEdit_ScrubsAbsolutePathsFromErrorResponses(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	roots := resolvedRoots(t, root)
	srv := &Server{logger: discardLogger(), workspace: root}

	// (a) nonexistent file: the EvalSymlinks lstat error used to carry the
	// absolute path here. Fix 7 turned this into a considered refusal phrased in
	// the caller's own relative path, so there is no longer a path to scrub --
	// the leak check still applies, and still passes.
	resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "ghost.go", Search: "x", Replace: "y"},
	})
	assertNoAbsRoot(t, "apply(ghost.go).Error", resp.Error, roots)
	if !strings.Contains(resp.Error, "ghost.go") {
		t.Errorf("apply(ghost.go).Error = %q, want the relative filename preserved (debuggability)", resp.Error)
	}

	// (b) nonexistent NESTED path: the deepest-existing-ancestor absolute path
	// was disclosed (revealed which directories exist). Same as (a) -- the
	// message no longer names any directory at all, which is what the leak
	// check confirms.
	resp = applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
		Edit: protocol.EditBlockWire{FilePath: "sub/does/not/exist.go", Search: "x", Replace: "y"},
	})
	assertNoAbsRoot(t, "apply(nested).Error", resp.Error, roots)
	if !strings.Contains(resp.Error, "sub/does/not/exist.go") {
		t.Errorf("apply(nested).Error = %q, want the relative path preserved (debuggability)", resp.Error)
	}

	// (c) exists-but-unreadable: os.ReadFile permission error carried the
	// absolute path. Skipped as root, where chmod 000 does not deny reads.
	if os.Getuid() != 0 {
		locked := filepath.Join(root, "locked.go")
		if err := os.WriteFile(locked, []byte("package main\n"), 0000); err != nil {
			t.Fatalf("writing locked fixture: %v", err)
		}
		defer os.Chmod(locked, 0644) // so t.TempDir cleanup can remove it
		resp = applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
			Edit: protocol.EditBlockWire{FilePath: "locked.go", Search: "x", Replace: "y"},
		})
		// This one DOES still carry a path (os.ReadFile's permission error names
		// the file), so it is the case that proves the scrub itself runs.
		assertScrubbedPath(t, "apply(locked.go).Error", resp.Error, roots)
	}
}

func TestHandleUndo_ScrubsAbsoluteBackupsPathFromErrorResponses(t *testing.T) {
	root := t.TempDir()
	roots := resolvedRoots(t, root)
	srv := &Server{logger: discardLogger(), workspace: root}

	// (a) unknown session dir: explicit "not found under <BK>" message.
	resp := undoViaHandler(t, srv, protocol.UndoRequest{
		Undo:             true,
		BackupSessionDir: "nope-not-real",
	})
	assertNoAbsRoot(t, "undo(unknown-session).Error", resp.Error, roots)
	if !strings.Contains(resp.Error, "not found under") {
		t.Errorf("undo(unknown-session).Error = %q, want it to still say what went wrong", resp.Error)
	}

	// (b) empty session, no backups dir exists yet: "no backups found at <BK>".
	resp = undoViaHandler(t, srv, protocol.UndoRequest{Undo: true})
	assertNoAbsRoot(t, "undo(no-backups).Error", resp.Error, roots)
	if !strings.Contains(resp.Error, "no backup") {
		t.Errorf("undo(no-backups).Error = %q, want it to still say no backups exist", resp.Error)
	}
}

// TestServeConn_ModelAPIErrorIsGenericAndLogsUpstreamLocally proves the Gate 5
// finding is closed: a model-API failure returns a stable generic message over
// the socket (no upstream URL / raw transport text), while the daemon's own log
// still carries the full detail for the operator. It also confirms the
// success-path grounding.workspace field is deliberately UNtouched by the fix.
func TestServeConn_ModelAPIErrorIsGenericAndLogsUpstreamLocally(t *testing.T) {
	const ws = "/workspace/gate7-model-error"
	const deadBase = "http://127.0.0.1:1" // nothing listens here

	var logBuf bytes.Buffer
	srv := &Server{
		apiBase:       deadBase,
		cfg:           &Config{},         // ZDR zero value = strict routing; NoScrub off
		modelOverride: "test/model-slug", // bypass the router (route() returns the override)
		logger:        log.New(&logBuf, "", 0),
		workspace:     ws,
		// embedder/store/memory nil: retrieval skipped, persistTurn no-ops.
	}

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"}); err != nil {
		t.Fatalf("encoding handshake: %v", err)
	}
	var hsResp protocol.HandshakeResponse
	if err := dec.Decode(&hsResp); err != nil {
		t.Fatalf("decoding handshake response: %v", err)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"}); err != nil {
		t.Fatalf("encoding prompt: %v", err)
	}

	// Read streamed messages until the terminal Done. Capture the grounding
	// message (sent first) and the final Done+Error.
	var groundingWorkspace string
	var doneErr, doneClass string
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding token response: %v", err)
		}
		if tok.Grounding != nil {
			groundingWorkspace = tok.Grounding.Workspace
		}
		if tok.Done {
			doneErr = tok.Error
			doneClass = tok.ErrorClass
			break
		}
	}
	<-done

	// Socket response: no upstream URL or raw transport text. Fix 9 replaced the
	// single generic string with a classified, client-safe message -- an
	// unreachable base is now reported as "upstream_unavailable" rather than a
	// shrug -- so what is pinned here is the Gate-7 property itself (nothing
	// about the deployment leaks), not the specific wording it used to have.
	if doneErr == "" {
		t.Error("Done.Error is empty; a failed request must still say something")
	}
	if strings.Contains(doneErr, deadBase) || strings.Contains(doneErr, "127.0.0.1") || strings.Contains(doneErr, "dial tcp") {
		t.Errorf("Done.Error = %q, leaks upstream URL / transport detail to the socket caller", doneErr)
	}
	if doneClass != string(ClassUpstreamUnavailable) {
		t.Errorf("Done.ErrorClass = %q, want %q", doneClass, ClassUpstreamUnavailable)
	}
	if strings.Contains(doneClass, deadBase) || strings.Contains(doneClass, "127.0.0.1") {
		t.Errorf("Done.ErrorClass = %q leaks infrastructure detail", doneClass)
	}

	// Local log: full upstream detail retained for the operator.
	if !strings.Contains(logBuf.String(), deadBase) {
		t.Errorf("local log = %q, want it to STILL carry the upstream base URL %q for debugging", logBuf.String(), deadBase)
	}

	// Success-path field untouched: grounding.workspace still reports the
	// daemon's workspace verbatim (this is by-design, not an error leak).
	if groundingWorkspace != ws {
		t.Errorf("grounding.workspace = %q, want the by-design %q (fix must not scrub the success path)", groundingWorkspace, ws)
	}
}

// TestServeConn_ZDRRefusalMessageUnchanged pins that generalizing the model-
// error rewrite did NOT regress the one case that already worked: an
// ErrZDRRefused still yields its specific message, not the new generic default.
func TestServeConn_ZDRRefusalMessageUnchanged(t *testing.T) {
	// errorServer replies with the ZDR-routing-refusal shape streamCompletion
	// maps to ErrZDRRefused (see provider_test.go).
	srv := errorServer(t, 503, `{"error":{"code":503,"message":"There is no available model provider that meets your routing requirements"}}`)
	defer srv.Close()

	s := &Server{
		apiBase:       srv.URL,
		cfg:           &Config{},
		modelOverride: "test/model-slug",
		logger:        discardLogger(),
		workspace:     "/workspace/gate7-zdr",
	}

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		s.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)
	_ = enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "test"})
	var hs protocol.HandshakeResponse
	_ = dec.Decode(&hs)
	_ = enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"})

	var doneErr, doneClass string
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding token response: %v", err)
		}
		if tok.Done {
			doneErr = tok.Error
			doneClass = tok.ErrorClass
			break
		}
	}
	<-done

	if doneErr != "inference refused: no zero-data-retention endpoint available" {
		t.Errorf("Done.Error = %q, want ErrZDRRefused's specific message unchanged", doneErr)
	}
	// The privacy refusal kept its identity through Fix 9's reclassification: it
	// is a class of its own, with the same wording it always had.
	if doneClass != string(ClassPrivacyRefused) {
		t.Errorf("Done.ErrorClass = %q, want %q", doneClass, ClassPrivacyRefused)
	}
}
