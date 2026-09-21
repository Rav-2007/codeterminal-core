package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"

	"mochiii/protocol"
)

// searchViaHandler drives handleSearch exactly like handleConn does --
// encode into a buffer, decode the single response back out. Mirrors
// undoViaHandler in undo_request_test.go.
func searchViaHandler(t *testing.T, srv *Server, req protocol.SearchRequest) protocol.SearchResponse {
	t.Helper()
	var buf bytes.Buffer
	srv.handleSearch(t.Context(), json.NewEncoder(&buf), req)
	var resp protocol.SearchResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decoding SearchResponse: %v (raw: %s)", err, buf.String())
	}
	return resp
}

func TestIsSearchRequest_TrueForSearchRequestJSON(t *testing.T) {
	raw, err := json.Marshal(protocol.SearchRequest{ProtocolVersion: protocol.ProtocolVersion, Search: true, Query: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !isSearchRequest(raw) {
		t.Errorf("isSearchRequest(%s) = false, want true for a real SearchRequest", raw)
	}
}

// TestIsSearchRequest_FalseForPlainPromptRequestJSON is the old-client
// safety check at the sniff level: a PromptRequest (no "search" key at all)
// must never be mistaken for a SearchRequest, exactly the same property
// isUndoRequest/isApplyEditRequest already rely on for their own keys.
func TestIsSearchRequest_FalseForPlainPromptRequestJSON(t *testing.T) {
	raw, err := json.Marshal(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if isSearchRequest(raw) {
		t.Errorf("isSearchRequest(%s) = true, want false for a plain PromptRequest with no \"search\" key", raw)
	}
}

func TestHandleSearch_FindsByKeyword(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-handler"
	if err := memStore.AppendTurn(ctx, ws, "user", "what is a goroutine?"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore}

	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "goroutine"})
	if resp.Error != "" {
		t.Fatalf("search = %+v, want no error", resp)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v, want 1 hit", resp.Results)
	}
	if resp.Results[0].Role != "user" {
		t.Errorf("Role = %q, want %q", resp.Results[0].Role, "user")
	}
	if resp.Results[0].Snippet == "" {
		t.Errorf("Snippet is empty, want a non-empty snippet")
	}
	if resp.Results[0].CreatedAt == "" {
		t.Errorf("CreatedAt is empty, want a non-empty timestamp")
	}
}

// TestHandleSearch_FindsBySubstringCodeToken proves the trigram tokenizer
// path is reachable end-to-end through the wire types, not just inside
// SearchTurns directly (already covered by search_test.go).
func TestHandleSearch_FindsBySubstringCodeToken(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-substring"
	if err := memStore.AppendTurn(ctx, ws, "assistant", `use fmt.Println("hi") to print in Go`); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore}

	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "fmt.Println"})
	if resp.Error != "" {
		t.Fatalf("search = %+v, want no error", resp)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v, want 1 hit", resp.Results)
	}
}

// TestHandleSearch_ScopedToServerWorkspaceNotClientSupplied reuses
// sub-slice 2's A/B/C workspace-isolation shape at the protocol layer:
// turns exist in two different workspaces, and the daemon always searches
// its OWN configured s.workspace -- a client-supplied req.Workspace is
// advisory only (same convention as PromptRequest.Workspace/
// UndoRequest.Workspace) and must never redirect the search to a different
// workspace's history.
func TestHandleSearch_ScopedToServerWorkspaceNotClientSupplied(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	if err := memStore.AppendTurn(ctx, "/workspace/a", "user", "channels question in workspace a"); err != nil {
		t.Fatalf("AppendTurn a: %v", err)
	}
	if err := memStore.AppendTurn(ctx, "/workspace/b", "user", "channels question in workspace b"); err != nil {
		t.Fatalf("AppendTurn b: %v", err)
	}

	srv := &Server{logger: discardLogger(), workspace: "/workspace/a", memory: memStore}

	// Even though the client claims Workspace: "/workspace/b", the daemon's
	// own configured workspace ("/workspace/a") wins.
	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "channels", Workspace: "/workspace/b"})
	if resp.Error != "" {
		t.Fatalf("search = %+v, want no error", resp)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Results = %+v, want exactly 1 hit (workspace a's turn only)", resp.Results)
	}

	// A daemon actually configured for a workspace that never wrote
	// anything finds nothing, proving there's no accidental cross-talk.
	srvC := &Server{logger: discardLogger(), workspace: "/workspace/c-never-wrote-anything", memory: memStore}
	respC := searchViaHandler(t, srvC, protocol.SearchRequest{Search: true, Query: "channels"})
	if respC.Error != "" {
		t.Fatalf("search c = %+v, want no error", respC)
	}
	if len(respC.Results) != 0 {
		t.Fatalf("Results c = %+v, want 0 (workspace c never wrote anything)", respC.Results)
	}
}

func TestHandleSearch_NoMatchReturnsEmptyResultsNotError(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-no-match"
	if err := memStore.AppendTurn(ctx, ws, "user", "something unrelated"); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore}

	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "nonexistentxyzzy"})
	if resp.Error != "" {
		t.Fatalf("search = %+v, want no error for a legitimate no-match", resp)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("Results = %+v, want empty", resp.Results)
	}
}

// TestHandleSearch_MemoryUnavailableReturnsExplicitError is the "don't let
// two different outcomes look identical" case: memory disabled must NOT
// look like an empty-but-successful search.
func TestHandleSearch_MemoryUnavailableReturnsExplicitError(t *testing.T) {
	srv := &Server{logger: discardLogger(), workspace: "/workspace/no-memory"} // memory left nil

	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "anything"})
	if resp.Error == "" {
		t.Fatalf("search = %+v, want an explicit error when memory is unavailable", resp)
	}
	if resp.Results != nil {
		t.Errorf("Results = %+v, want nil alongside the error", resp.Results)
	}
}

func TestHandleSearch_LimitDefaultsWhenOmittedOrRespectsExplicitValue(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-limit"
	for i := 0; i < 25; i++ {
		if err := memStore.AppendTurn(ctx, ws, "user", "repeated marker token appears in every turn"); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
	}
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore}

	resp := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "marker", Limit: 0})
	if resp.Error != "" {
		t.Fatalf("search = %+v, want no error", resp)
	}
	if len(resp.Results) != defaultSearchLimit {
		t.Errorf("Results count = %d, want the default limit %d applied when Limit is omitted", len(resp.Results), defaultSearchLimit)
	}

	respSmall := searchViaHandler(t, srv, protocol.SearchRequest{Search: true, Query: "marker", Limit: 3})
	if len(respSmall.Results) != 3 {
		t.Errorf("Results count = %d, want exactly the requested limit 3 respected", len(respSmall.Results))
	}
}

// TestHandleConn_SearchRequestRoutesThroughRealDispatchNotPromptPath proves
// isSearchRequest is actually wired into the real connection handler, not
// just correct in isolation: apiBase deliberately points at a port nothing
// listens on, so if a SearchRequest ever fell through to the PromptRequest
// path, the response would carry a dial error instead of a clean
// SearchResponse. Mirrors
// TestHandleConn_ResetNeverCallsModelAndClearsPersistedHistory's shape.
func TestHandleConn_SearchRequestRoutesThroughRealDispatchNotPromptPath(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/workspace/search-dispatch"
	if err := memStore.AppendTurn(ctx, ws, "user", "what is a goroutine?"); err != nil {
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
		// serveConn is handleConn's post-authentication half. These tests drive
		// it directly because a net.Pipe conn carries no kernel peer credentials
		// for handleConn's authorizePeer gate (proven separately in
		// peerauth_test.go); the dispatch behavior under test is what runs once
		// a peer is authorized.
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

	if err := enc.Encode(protocol.SearchRequest{ProtocolVersion: protocol.ProtocolVersion, Search: true, Query: "goroutine"}); err != nil {
		t.Fatalf("encoding search request: %v", err)
	}

	var searchResp protocol.SearchResponse
	if err := dec.Decode(&searchResp); err != nil {
		t.Fatalf("decoding search response: %v", err)
	}
	<-done

	if searchResp.Error != "" {
		t.Fatalf("search response = %+v, want no error (proves it never fell through to the model-calling prompt path)", searchResp)
	}
	if len(searchResp.Results) != 1 {
		t.Fatalf("Results = %+v, want exactly 1 (proves real dispatch reached handleSearch)", searchResp.Results)
	}
}

// TestHandleConn_PlainPromptRequestStillRoutesNormally is the old-client
// safety check at the serveConn dispatch level: a real PromptRequest (which
// never serializes a "search" key) must still reach the ordinary prompt
// path unaffected by the new isSearchRequest check being consulted first.
// apiBase deliberately points nowhere, so reaching the model-calling path
// is visible as a Done:true response WITH a non-empty Error, exactly like
// TestHandleConn_ResetNeverCallsModelAndClearsPersistedHistory's use of the
// same signal for the opposite assertion.
func TestHandleConn_PlainPromptRequestStillRoutesNormally(t *testing.T) {
	srv := &Server{
		apiBase:   "http://127.0.0.1:1", // nothing listens here
		cfg:       testConfig(false),
		logger:    discardLogger(),
		workspace: "/workspace/prompt-still-works",
	}

	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		// serveConn is handleConn's post-authentication half. These tests drive
		// it directly because a net.Pipe conn carries no kernel peer credentials
		// for handleConn's authorizePeer gate (proven separately in
		// peerauth_test.go); the dispatch behavior under test is what runs once
		// a peer is authorized.
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
		t.Fatalf("encoding prompt request: %v", err)
	}

	// The prompt path sends a Grounding message before any tokens (see
	// handleConn) -- decode past it to the first message that actually
	// reveals whether the model-calling path was reached.
	var groundingResp protocol.TokenResponse
	if err := dec.Decode(&groundingResp); err != nil {
		t.Fatalf("decoding grounding response: %v", err)
	}

	var tokResp protocol.TokenResponse
	if err := dec.Decode(&tokResp); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	<-done

	if !tokResp.Done || tokResp.Error == "" {
		t.Errorf("prompt response = %+v, want Done:true WITH a dial error (proves the prompt path, and only the prompt path, was reached)", tokResp)
	}
}
