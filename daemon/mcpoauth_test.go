package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"mochiii/protocol"
)

// SIGNING IN TO A REMOTE SERVER (mcpoauth.go), against a fake authorization
// server and a resource server that wants its token -- discovery, client
// registration, the browser's round trip, the code exchange and a refresh.

type fakeOAuth struct {
	ts        *httptest.Server
	mu        sync.Mutex
	valid     map[string]bool
	issued    atomic.Int32
	refreshed atomic.Int32
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{valid: map[string]bool{}}
	srv := sdk.NewServer(&sdk.Implementation{Name: "signed", Version: "v0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "whoami", Description: "Say who."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ remoteNoArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "you"}}}, nil, nil
		})
	mcpHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	mux := http.NewServeMux()
	base := func() string { return f.ts.URL }
	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		ok := f.valid[tok]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, base()))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"resource": base() + "/mcp", "authorization_servers": []string{base()}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer": base(), "authorization_endpoint": base() + "/authorize", "token_endpoint": base() + "/token",
			"registration_endpoint": base() + "/register", "response_types_supported": []string{"code"},
			"grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		meta["client_id"] = "client-1"
		writeJSON(w, 201, meta)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge") == "" {
			http.Error(w, "PKCE required", 400)
			return
		}
		to, _ := url.Parse(q.Get("redirect_uri"))
		back := to.Query()
		back.Set("code", "code-1")
		back.Set("state", q.Get("state"))
		to.RawQuery = back.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		var access string
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "code-1" || r.Form.Get("code_verifier") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			access = fmt.Sprintf("access-token-7f3a9c2e5b1d8046-%d", f.issued.Add(1))
		case "refresh_token":
			f.refreshed.Add(1)
			access = fmt.Sprintf("access-token-refreshed-9e4b2d7a1c-%d", f.refreshed.Load())
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
			return
		}
		f.mu.Lock()
		f.valid[access] = true
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": "refresh-token-1", "token_type": "Bearer", "expires_in": 3600})
	})
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

// browser plays the user's browser: it opens the address and follows the
// authorization server's redirect back to the daemon's callback.
func browser(t *testing.T, authURL string) {
	t.Helper()
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("the browser could not open %s: %v", authURL, err)
	}
	_ = resp.Body.Close()
}

func waitSignedIn(t *testing.T, s *Server, id string) protocol.MCPResponse {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthWait, AuthID: id})
		if resp.AuthState != protocol.MCPAuthPending {
			return resp
		}
	}
	t.Fatal("the sign-in did not end")
	return protocol.MCPResponse{}
}

func TestSigningInToARemoteServer(t *testing.T) {
	f := newFakeOAuth(t)
	s := mcpAdminTestServer(t)
	path := withKeyFile(t, s)
	addRemote(t, s, "signed", f.ts.URL+"/mcp", nil)

	// Before a sign-in: a check says so, at once, without opening anything.
	start := time.Now()
	s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionCheck, Name: "signed"})
	if got := serverInfo(t, s, "signed").StatusLine; got != "! Needs authentication" {
		t.Fatalf("before signing in: %q", got)
	}
	// Far under a sign-in's own window: a check never waits on a browser.
	if elapsed := time.Since(start); elapsed > mcpSignInWindow/30 {
		t.Errorf("a check without a sign-in waited %v", elapsed)
	}

	begun := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthStart, Name: "signed"})
	if !begun.Ok || begun.AuthURL == "" || begun.AuthID == "" || begun.AuthState != protocol.MCPAuthPending {
		t.Fatalf("auth_start: %+v", begun)
	}
	if !strings.HasPrefix(begun.AuthURL, f.ts.URL+"/authorize?") {
		t.Errorf("the address to open is %q", begun.AuthURL)
	}
	browser(t, begun.AuthURL)
	done := waitSignedIn(t, s, begun.AuthID)
	if done.AuthState != protocol.MCPAuthDone || !strings.Contains(done.Detail, "Signed in to signed") {
		t.Fatalf("auth_wait: %+v", done)
	}

	info := serverInfo(t, s, "signed")
	if !info.SignedIn || info.StatusLine != "✔ Connected (1 tool)" {
		t.Errorf("after signing in: %+v", info)
	}
	if !registryOffers(t, s, "signed__whoami") {
		t.Error("a turn after signing in does not reach the server")
	}
	if strings.Contains(readFileString(t, s.userServersPath), "access-token") {
		t.Fatal("the token is in the server list")
	}
	const firstToken = "access-token-7f3a9c2e5b1d8046-1"
	if !strings.Contains(readFileString(t, path), firstToken) {
		t.Error("the token was not kept in the key file")
	}
	if got, _ := s.credRedact("token " + firstToken); strings.Contains(got, firstToken) {
		t.Error("the token is not scrubbed")
	}
	if got, _ := s.credRedact("a Bearer token"); got != "a Bearer token" {
		t.Errorf("the word Bearer is scrubbed: %q", got)
	}

	// An expired token is refreshed -- and the new one kept -- by a turn.
	if err := s.mcpSecrets.update(func(file *mcpSecretsFile) {
		srv := file.Servers["signed"]
		var o mcpOAuthSecret
		_ = json.Unmarshal(srv.Extra["oauth"], &o)
		o.Expiry = time.Now().Add(-time.Hour)
		srv.Extra["oauth"], _ = json.Marshal(o)
		file.Servers["signed"] = srv
	}); err != nil {
		t.Fatal(err)
	}
	if !registryOffers(t, s, "signed__whoami") {
		t.Fatal("an expired token was not refreshed")
	}
	if f.refreshed.Load() != 1 || !strings.Contains(readFileString(t, path), "access-token-refreshed-9e4b2d7a1c-1") {
		t.Errorf("refreshed %d time(s); the new token kept: %v", f.refreshed.Load(), strings.Contains(readFileString(t, path), "refreshed"))
	}

	// Signing out deletes the token.
	s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionSignOut, Name: "signed"})
	if data, err := os.ReadFile(path); (err == nil && strings.Contains(string(data), "access-token")) || serverInfo(t, s, "signed").SignedIn {
		t.Error("signing out kept the token")
	}
}

// A browser that cannot reach this machine: the address it ended on, pasted.
// Only an address on the flow's own secret path is taken.
func TestASignInCanBeFinishedWithThePastedAddress(t *testing.T) {
	f := newFakeOAuth(t)
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	addRemote(t, s, "signed", f.ts.URL+"/mcp", nil)
	begun := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthStart, Name: "signed"})
	if !begun.Ok {
		t.Fatalf("auth_start: %+v", begun)
	}

	// The browser is sent to the callback but never arrives: read where it
	// was sent instead of following it.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(begun.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	landed := resp.Header.Get("Location")

	wrong := strings.Replace(landed, "/cb/", "/cb/x", 1)
	if bad := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthComplete,
		AuthID: begun.AuthID, CallbackURL: wrong}); bad.Error == "" {
		t.Error("an address on another path was taken")
	}
	done := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthComplete,
		AuthID: begun.AuthID, CallbackURL: landed})
	if done.AuthState != protocol.MCPAuthDone {
		t.Fatalf("auth_complete: %+v", done)
	}
}

// A sign-in's wait does not hold every other MCP request behind it.
func TestASignInWaitDoesNotBlockTheList(t *testing.T) {
	f := newFakeOAuth(t)
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	addRemote(t, s, "signed", f.ts.URL+"/mcp", nil)
	begun := s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthStart, Name: "signed"})
	go s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthWait, AuthID: begun.AuthID})
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	s.mcpResult(context.Background(), manageRequest(protocol.MCPActionServers, ""))
	// The wait it ran beside lasts up to mcpSignInWindow; the list must not.
	if elapsed := time.Since(start); elapsed > mcpSignInWindow/30 {
		t.Errorf("the list waited %v behind a sign-in", elapsed)
	}
	s.mcpResult(context.Background(), protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthCancel, AuthID: begun.AuthID})
}
