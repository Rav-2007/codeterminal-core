package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codeterminal/protocol"
)

// The repo's first proxy<->daemon integration test.
//
// WHY THIS EXISTS. P1-1 was invisible to both suites individually: the proxy
// suite asserts its kill chunk is emitted, the daemon suite asserts its own
// parsing, and nothing spanned the two -- so a chunk the daemon had no field to
// receive passed both. The two components are separate Go modules that cannot
// import each other (proxy/Dockerfile copies only *.go and go.mod, deliberately
// keeping the proxy dependency-free), which is exactly the condition under which
// a shared wire contract drifts unnoticed. The only way to test the contract is
// to run both halves for real, which is what this does: the real proxy binary,
// built from source, in front of the real streamCompletion parser.
//
// It is the durable fix for the class. Any future change to writeBudgetExceeded's
// bytes, to the chunk struct, or to the budget kill's placement breaks it here.

// buildProxyBinary compiles the sibling proxy module through the go.work
// workspace and returns the binary path.
func buildProxyBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), exeName("codeterminal-proxy"))
	cmd := exec.Command("go", "build", "-o", bin, "codeterminal/proxy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the proxy binary: %v\n%s", err, out)
	}
	return bin
}

// seamSupabase stubs the two Supabase RPCs the proxy needs to admit and settle a
// request: the api_keys lookup behind authorize, and reserve_usage. The token
// limit is set so the reservation leaves only `headroom` tokens, which is what
// sizes the per-request ceiling and therefore the byte guard.
func seamSupabase(t *testing.T, keyID string, reserved, headroom int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var corrections atomic.Int64
	corrections.Store(int64(^uint64(0) >> 1)) // sentinel: no correction seen yet

	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{{"id": keyID}})
	})
	mux.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]int64{{
			"tokens_used": reserved,
			"token_limit": reserved + headroom,
			"pending_id":  1,
		}})
	})
	mux.HandleFunc("/rest/v1/rpc/apply_correction", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tokens int64 `json:"p_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		corrections.Store(body.Tokens)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/v1/rpc/sweep_pending_corrections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	return httptest.NewServer(mux), &corrections
}

// TestSeam_ProxyBudgetKillReachesTheUser drives a budget kill end to end: a stub
// OpenRouter streams chunks big enough to cross the proxy's byte guard, the real
// proxy kills the stream and emits its budget_exceeded chunk, and the real daemon
// parser must turn that into a user-visible IncompleteInfo.
//
// Both halves of the launch-gate batch are asserted here, in the one place they
// meet: the user is TOLD the answer was cut short (P1-1), and the kill did not
// refund the reservation (P0-1).
func TestSeam_ProxyBudgetKillReachesTheUser(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the proxy binary; skipped under -short")
	}

	const (
		keyID    = "seam0000-0000-0000-0000-000000000001"
		model    = "seam/test-model"
		reserved = 4096
		headroom = 100 // ceiling 4196 => byte guard at 4196*512 = 2,148,352 bytes
	)

	supabase, corrections := seamSupabase(t, keyID, reserved, headroom)
	defer supabase.Close()

	// A provider batching an entire answer into a few huge chunks: crosses the
	// BYTE guard while the chunk count stays at 3, far below the token ceiling.
	// This is the bound the proxy's own suite never exercised.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		filler := strings.Repeat("A", 900_000)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", filler)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer upstream.Close()

	proxyBase := startProxy(t, buildProxyBinary(t), supabase.URL, upstream.URL, model)

	var content strings.Builder
	finish := "<unset>"
	routing := ZDRConfig{}.resolvedProviderRouting()
	_, err := streamCompletion(context.Background(), proxyBase, "mochi_test_key", model, buildChatMessages("sys", nil, "hi"), nil, routing,
		func(tok string) error { content.WriteString(tok); return nil },
		nil, nil,
		func(reason string) { finish = reason },
	)
	if err != nil {
		t.Fatalf("streamCompletion through the real proxy: %v", err)
	}

	// P1-1: the user must be able to tell this answer is not whole.
	if finish != protocol.IncompleteBudgetExceeded {
		t.Errorf("across the real proxy->daemon seam, onFinish reported %q, want %q. "+
			"The proxy killed this stream for crossing its spending ceiling; the user "+
			"is being shown a truncated answer as a complete success", finish, protocol.IncompleteBudgetExceeded)
	}
	info := incompleteInfoFor(finish)
	if info == nil || info.Detail == "" {
		t.Fatalf("IncompleteInfo = %+v, want a non-nil report with client-facing detail -- "+
			"this is what both clients render", info)
	}
	t.Logf("user-visible notice: %s", info.Detail)

	// The streamed output must still reach the user: it is real, billed content.
	if content.Len() == 0 {
		t.Error("no content reached the user at all -- a budget kill must truncate the " +
			"answer, not discard it")
	}

	// P0-1, at the seam: the kill must not have moved quota in the caller's favour.
	waitFor(t, 3*time.Second, func() bool {
		return corrections.Load() != int64(^uint64(0)>>1)
	}, "the proxy never settled its reservation")
	if delta := corrections.Load(); delta < 0 {
		t.Errorf("the real proxy refunded %d of %d reserved tokens for a stream it killed "+
			"at its byte guard (P0-1)", -delta, reserved)
	}
}

// startProxy runs the compiled proxy against the stub Supabase and stub upstream,
// waits for /health, and returns the base URL the daemon should be pointed at
// (the CODETERMINAL_PROXY_BASE shape: ".../v1").
func startProxy(t *testing.T, bin, supabaseURL, upstreamURL, allowedModel string) string {
	t.Helper()

	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"OPENROUTER_API_KEY=test-upstream-key",
		"OPENROUTER_API_BASE="+upstreamURL,
		"SUPABASE_URL="+supabaseURL,
		"SUPABASE_SERVICE_ROLE_KEY=test-service-role-key",
		"ALLOWED_MODELS="+allowedModel,
		fmt.Sprintf("PORT=%d", port),
	)
	// The proxy logs to stderr; surface it only on failure, via t.Log below.
	var logs strings.Builder
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the proxy: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Logf("proxy logs:\n%s", logs.String())
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, 10*time.Second, func() bool {
		resp, err := http.Get(base + "/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, "the proxy never became healthy")
	return base + "/v1"
}

func freePort(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().(*net.TCPAddr)
	srv.Close()
	return addr.Port
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The last hop: the daemon SOCKET. The test above proves the real proxy binary
// reaches streamCompletion; this one carries it the rest of the way, through
// Server.serveConn, and asserts the WIRE BYTES a client actually receives --
// which is what the TUI prints and what the VS Code webview renders.
//
// P1-1's symptom was defined in exactly these terms ("the daemon then reports
// Done:true with no Error and no IncompleteInfo"), so this is where that claim
// is falsifiable. A unit test on the parser cannot state it.
func TestSeam_BudgetKillReachesTheClientOverTheSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the proxy binary; skipped under -short")
	}

	const (
		keyID    = "seam0000-0000-0000-0000-000000000002"
		model    = "seam/socket-model"
		reserved = 4096
		headroom = 100
	)

	supabase, _ := seamSupabase(t, keyID, reserved, headroom)
	defer supabase.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		filler := strings.Repeat("A", 900_000)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", filler)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer upstream.Close()

	// The daemon pointed at the real proxy: the CODETERMINAL_PROXY_BASE shape a
	// pilot user actually runs.
	srv := &Server{
		apiBase:       startProxy(t, buildProxyBinary(t), supabase.URL, upstream.URL, model),
		apiKey:        "mochi_seam_key",
		cfg:           &Config{},
		modelOverride: model,
		logger:        discardLogger(),
		workspace:     "/workspace/seam",
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
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "seam"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake response: %v", err)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "explain this repo"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	var contentMessages int
	var final protocol.TokenResponse
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("decoding token response: %v", err)
		}
		if tok.Token != "" {
			contentMessages++
		}
		if tok.Done {
			final = tok
			break
		}
	}
	<-done

	wire, _ := json.Marshal(final)
	t.Logf("terminal TokenResponse on the wire: %s", wire)

	if final.Incomplete == nil {
		t.Fatalf("the terminal TokenResponse carries NO IncompleteInfo, so a budget-killed "+
			"answer reaches the user as a complete success. Wire: %s", wire)
	}
	if final.Incomplete.Reason != protocol.IncompleteBudgetExceeded {
		t.Errorf("Incomplete.Reason = %q, want %q", final.Incomplete.Reason, protocol.IncompleteBudgetExceeded)
	}
	if final.Incomplete.Detail == "" {
		t.Error("Incomplete.Detail is empty; it is the string both clients render")
	}
	// A budget kill is a TRUNCATION, not a failure. The partial answer is real
	// output the caller was billed for, so presenting it as an error would be a
	// different wrong answer to the same question.
	if final.Error != "" {
		t.Errorf("Done.Error = %q, want empty -- the answer is incomplete, not failed", final.Error)
	}
	if contentMessages == 0 {
		t.Error("no content reached the client before the kill")
	}
}
