package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"codeterminal/protocol"
)

// --- Server memory helpers: direct unit tests -------------------------

func TestServer_MemoryHelpersAreNoOpsWhenMemoryIsNil(t *testing.T) {
	srv := &Server{logger: discardLogger(), workspace: "/workspace/x"}

	if got := srv.loadPersistedHistory(); got != nil {
		t.Errorf("loadPersistedHistory() = %+v, want nil when s.memory is nil", got)
	}
	srv.persistTurn("q", "a")   // must not panic
	srv.resetPersistedHistory() // must not panic
}

func TestServer_PersistTurnAppendsUserThenAssistant(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	srv := &Server{logger: discardLogger(), workspace: "/workspace/persist", memory: memStore}

	srv.persistTurn("what is a goroutine?", "a lightweight thread")

	got, err := memStore.LoadRecentTurns(context.Background(), "/workspace/persist", 12)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	want := []protocol.Turn{
		{Role: "user", Content: "what is a goroutine?"},
		{Role: "assistant", Content: "a lightweight thread"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("turn %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestServer_LoadPersistedHistoryReflectsWhatWasPersisted(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/load"
	if err := memStore.AppendTurn(ctx, ws, "user", "q1"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := memStore.AppendTurn(ctx, ws, "assistant", "a1"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore}
	got := srv.loadPersistedHistory()
	if len(got) != 2 || got[0].Content != "q1" || got[1].Content != "a1" {
		t.Errorf("loadPersistedHistory() = %+v, want the 2 persisted turns in order", got)
	}
}

func TestServer_ResetPersistedHistoryClearsOnlyThisWorkspace(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	if err := memStore.AppendTurn(ctx, "/workspace/x", "user", "q1"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := memStore.AppendTurn(ctx, "/workspace/y", "user", "other"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	srv := &Server{logger: discardLogger(), workspace: "/workspace/x", memory: memStore}
	srv.resetPersistedHistory()

	gotX, err := memStore.LoadRecentTurns(ctx, "/workspace/x", 12)
	if err != nil {
		t.Fatalf("LoadRecentTurns x: %v", err)
	}
	if len(gotX) != 0 {
		t.Errorf("workspace x = %+v, want empty after reset", gotX)
	}
	gotY, err := memStore.LoadRecentTurns(ctx, "/workspace/y", 12)
	if err != nil {
		t.Fatalf("LoadRecentTurns y: %v", err)
	}
	if len(gotY) != 1 {
		t.Errorf("workspace y = %+v, want untouched by resetting workspace x", gotY)
	}
}

// --- handleConn integration: the safety-critical control-flow guarantees ---

// TestHandleConn_ResetNeverCallsModelAndClearsPersistedHistory drives
// handleConn's post-auth half (serveConn) over an in-memory net.Pipe with
// apiBase pointing at a port nothing listens on. If Reset ever fell through to the normal
// model-calling path, streamCompletion's dial would fail and the response
// would be a Done:true WITH a non-empty Error — not the bare Done:true this
// test requires. This is the regression test for the "stale daemon /
// Reset" concern: Reset must short-circuit before s.route()/streamCompletion
// are ever reached, and it also proves loadPersistedHistory/resetPersisted-
// History are correctly wired into the real connection handler, not just
// correct in isolation.
func TestHandleConn_ResetNeverCallsModelAndClearsPersistedHistory(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/reset-integration"
	if err := memStore.AppendTurn(ctx, ws, "user", "old question"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if err := memStore.AppendTurn(ctx, ws, "assistant", "old answer"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}

	srv := &Server{
		apiBase:   "http://127.0.0.1:1", // nothing listens here
		logger:    discardLogger(),
		workspace: ws,
		memory:    memStore,
	}

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		// serveConn is handleConn's post-authentication half. A net.Pipe conn
		// carries no kernel peer credentials for handleConn's authorizePeer gate
		// (covered in peercred_test.go), so these control-flow tests drive the
		// post-auth path directly — which is what executes for an authorized peer.
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
	if !hsResp.Ok {
		t.Fatalf("handshake rejected: %+v", hsResp)
	}
	if len(hsResp.PersistedHistory) != 2 {
		t.Fatalf("PersistedHistory = %+v, want the 2 previously-appended turns", hsResp.PersistedHistory)
	}

	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Reset: true}); err != nil {
		t.Fatalf("encoding reset request: %v", err)
	}

	var tokResp protocol.TokenResponse
	if err := dec.Decode(&tokResp); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	if !tokResp.Done || tokResp.Error != "" || tokResp.Token != "" {
		t.Errorf("reset response = %+v, want a bare Done:true with no error/token (proves the model was never called)", tokResp)
	}

	<-done

	remaining, err := memStore.LoadRecentTurns(ctx, ws, 12)
	if err != nil {
		t.Fatalf("LoadRecentTurns after reset: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %+v, want empty after Reset", remaining)
	}
}
