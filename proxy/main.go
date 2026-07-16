// Command codeterminal-proxy is the smallest possible managed-tier proxy in
// front of OpenRouter. It exists to hold the OpenRouter API key server-side
// so it never ships to end users. It is a dumb pipe: it adds the key and
// forwards the request body exactly as received, then streams the response
// back without buffering. Every call is first gated on a caller-supplied
// Mochiii key, validated against Supabase (see authorize) -- unauthorized
// requests never reach OpenRouter. Token usage is recorded per key after a
// successful response (see streamSSE/recordUsage), and checked BEFORE each
// call is forwarded (see checkQuota): a key at or over its token_limit is
// refused with 429 before OpenRouter is ever contacted -- fail-closed, same
// as authorize, since this gate protects real spend.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultUpstreamBase = "https://openrouter.ai/api/v1"
	chatCompletionsPath = "/chat/completions"

	// upstreamTimeout bounds a single forwarded call so a stalled upstream
	// can't hang a connection (and its goroutine) forever. Matches the
	// daemon's own requestTimeout (daemon/provider.go) since this proxy
	// sits directly in that same call path.
	upstreamTimeout = 5 * time.Minute

	// supabaseAuthTimeout bounds the key-lookup call separately from
	// upstreamTimeout: a slow/down Supabase must fail the request closed
	// in seconds, not eat minutes of the inference budget before OpenRouter
	// is ever contacted.
	supabaseAuthTimeout = 5 * time.Second

	// maxRequestBodyBytes caps the incoming request body the proxy will
	// read. Generous for a chat-completions payload (prompt + retrieved
	// context + capped history), just enough to stop an unbounded body
	// from exhausting memory.
	maxRequestBodyBytes = 4 << 20 // 4MB

	// maxAuthResponseBytes caps the Supabase lookup response. The query
	// only ever selects `id` and is filtered to at most one active row, so
	// this is generous headroom, not a real limit in practice.
	maxAuthResponseBytes = 64 * 1024

	// usageUpdateTimeout bounds the fire-and-forget per-key usage RPC call,
	// separate from the request the tokens were counted on: it runs after
	// that response has already been fully relayed to the client, on its
	// own context, so it must not hang indefinitely.
	usageUpdateTimeout = 5 * time.Second

	// keyLogPrefixLen is the most of a caller-supplied key that is ever
	// written to a log line -- never the full key.
	keyLogPrefixLen = 8

	sseInitialBufferSize = 64 * 1024
	sseMaxLineSize       = 1024 * 1024
)

func main() {
	logger := log.New(os.Stderr, "codeterminal-proxy: ", log.LstdFlags)

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		logger.Fatal("OPENROUTER_API_KEY must be set (server environment only -- never a file, never hardcoded)")
	}

	upstreamBase := os.Getenv("OPENROUTER_API_BASE")
	if upstreamBase == "" {
		upstreamBase = defaultUpstreamBase
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseServiceRoleKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	if supabaseURL == "" || supabaseServiceRoleKey == "" {
		logger.Printf("WARNING: SUPABASE_URL and/or SUPABASE_SERVICE_ROLE_KEY not set -- every request will fail auth and be rejected with 401 (fail closed)")
	}

	// RAILWAY_GIT_COMMIT_SHA is set automatically by Railway's build
	// environment; empty in any local/non-Railway run, which is not fatal
	// -- /health still serves, just with an "unknown" commit, so this can
	// never block startup.
	buildCommit := os.Getenv("RAILWAY_GIT_COMMIT_SHA")
	if buildCommit == "" {
		buildCommit = "unknown"
	}

	p := &proxy{
		apiKey:                 apiKey,
		upstreamURL:            strings.TrimRight(upstreamBase, "/") + chatCompletionsPath,
		logger:                 logger,
		supabaseURL:            supabaseURL,
		supabaseServiceRoleKey: supabaseServiceRoleKey,
		// No blanket http.Client.Timeout: a streaming response can
		// legitimately run for minutes. Each request's own context
		// deadline (upstreamTimeout, applied per-call below) is what
		// actually bounds it instead. The Supabase auth lookup uses the
		// same client but its own shorter per-call context deadline
		// (supabaseAuthTimeout).
		client: &http.Client{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", makeHealthHandler(buildCommit))
	mux.HandleFunc(chatCompletionsPath, p.handleChatCompletions)       // "/chat/completions"
	mux.HandleFunc("/v1"+chatCompletionsPath, p.handleChatCompletions) // "/v1/chat/completions" alias

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Printf("listening on :%s -> upstream %s", port, p.upstreamURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}

type proxy struct {
	apiKey                 string
	upstreamURL            string
	logger                 *log.Logger
	client                 *http.Client
	supabaseURL            string
	supabaseServiceRoleKey string
}

// healthResponse is the /health body. commit lets a deploy-verify step
// confirm which build Railway is actually running, since Railway's own
// dashboard commit doesn't guarantee the running container matches it.
type healthResponse struct {
	Status string `json:"status"`
	Commit string `json:"commit"`
}

// makeHealthHandler closes over the build's commit SHA (read once at
// startup in main) so every /health response reports it without a global.
func makeHealthHandler(commit string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(healthResponse{Status: "ok", Commit: commit})
		if err != nil {
			// Unreachable in practice (healthResponse is two plain
			// strings), but fail the same way a real health-check
			// failure would rather than write a malformed body.
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}
}

// handleChatCompletions forwards the request body AS-IS to OpenRouter --
// never parsed, never modified, since the daemon already sets
// model/messages/stream/provider exactly as it wants them (including the
// ZDR provider-routing object) -- with a server-side Authorization header
// added, and streams the response back without buffering. Request and
// response BODIES are never logged, at any point below: only method,
// status, and (if visible in-flight, from the response stream itself) the
// serving provider name. That omission is deliberate and is what keeps this
// proxy zero-data-retention-preserving rather than just a relay that
// happens to also see everything.
func (p *proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p.logger.Printf("request received: %s", r.URL.Path)

	apiKeyID, ok := p.authorize(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if !p.checkQuota(r.Context(), apiKeyID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"quota_exceeded"}`))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL, limitedBody)
	if err != nil {
		p.logger.Printf("building upstream request failed: %v", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	// Preserve the caller's declared length instead of leaving it at the
	// zero value, which net/http would otherwise send as
	// Transfer-Encoding: chunked -- harmless, but needlessly different
	// from what the daemon itself sent.
	upstreamReq.ContentLength = r.ContentLength
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		p.logger.Printf("upstream call failed: %v", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	p.logger.Printf("upstream responded: status=%d", resp.StatusCode)

	for k, values := range resp.Header {
		// Hop-by-hop / framing headers that must not survive a
		// streamed, re-chunked passthrough verbatim -- Go's server
		// manages these itself for whatever we actually write below.
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		p.streamSSE(w, resp.Body, apiKeyID)
		return
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		p.logger.Printf("copying non-streamed response failed: %v", err)
	}
}

// authorize validates the caller-supplied Mochiii key ("Authorization:
// Bearer <mochi_key>") against Supabase and returns the matching
// api_keys.id on success. It fails CLOSED: a missing/empty header, an
// unconfigured Supabase, a lookup error/timeout, a non-200 response, or a
// row count other than exactly one are all treated as unauthorized --
// OpenRouter is never contacted in any of those cases. Only the auth
// outcome and, at most, the key's first keyLogPrefixLen chars are ever
// logged; the full mochi_key, the Supabase service-role key, and the
// OpenRouter key are never logged.
func (p *proxy) authorize(r *http.Request) (string, bool) {
	const bearerPrefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		p.logger.Printf("auth: rejected (no bearer token)")
		return "", false
	}
	mochiKey := strings.TrimSpace(strings.TrimPrefix(authHeader, bearerPrefix))
	if mochiKey == "" {
		p.logger.Printf("auth: rejected (empty key)")
		return "", false
	}

	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		p.logger.Printf("auth: rejected (supabase not configured, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}

	hash := sha256.Sum256([]byte(mochiKey))
	hashHex := hex.EncodeToString(hash[:])

	ctx, cancel := context.WithTimeout(r.Context(), supabaseAuthTimeout)
	defer cancel()

	q := url.Values{}
	q.Set("key_hash", "eq."+hashHex)
	q.Set("active", "eq.true")
	q.Set("select", "id")
	lookupURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/api_keys?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		p.logger.Printf("auth: rejected (building supabase request failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}
	// apikey only -- deliberately no Authorization header. Supabase's new
	// sb_secret_/sb_publishable_ API keys are not JWTs: sending one via
	// Authorization: Bearer, even when it exactly matches apikey (a
	// backward-compat exception that lets the request past the gateway
	// instead of being blocked outright), still gets forwarded to the
	// database's own JWT parser and rejected there for not being a JWT --
	// see https://supabase.com/docs/guides/api/api-keys. This is a real,
	// documented header-format bug and worth keeping fixed regardless --
	// but note: removing it did NOT resolve a separate empty-row symptom
	// under investigation (see project handoff doc, Phase 3 security
	// review). A bare curl with only `apikey` set reproduces the same
	// 200 + [] result, so that deeper cause is still open and unrelated
	// to this specific header issue.
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("auth: rejected (supabase lookup failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.logger.Printf("auth: rejected (supabase status=%d, key prefix=%s)", resp.StatusCode, keyPrefix(mochiKey))
		return "", false
	}

	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAuthResponseBytes)).Decode(&rows); err != nil {
		p.logger.Printf("auth: rejected (decoding supabase response failed, key prefix=%s)", keyPrefix(mochiKey))
		return "", false
	}

	if len(rows) != 1 {
		p.logger.Printf("auth: rejected (key prefix=%s, matches=%d)", keyPrefix(mochiKey), len(rows))
		return "", false
	}

	p.logger.Printf("auth: ok (key prefix=%s)", keyPrefix(mochiKey))
	return rows[0].ID, true
}

// checkQuota reports whether apiKeyID (an already-authorized api_keys.id --
// never the raw Mochiii key, see authorize) is still under its token quota.
// Deliberately separate from authorize: authorize's contract is
// identity-only and is already verified, so this is a second, independent
// gate rather than folded into it. It fails CLOSED -- returns false (not
// allowed) -- on a request-build error, a Supabase call error/timeout, a
// non-200 response, a row count other than exactly one, or a null
// token_limit (no limit configured is treated as "can't verify", not as
// "unlimited"). It returns true only when exactly one row comes back with a
// non-null token_limit and tokens_used strictly under it. Only the key id
// and token counts are ever logged -- never the raw key, never request
// content.
func (p *proxy) checkQuota(ctx context.Context, apiKeyID string) bool {
	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		p.logger.Printf("quota: refused (supabase not configured, key id=%s)", apiKeyID)
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, supabaseAuthTimeout)
	defer cancel()

	q := url.Values{}
	q.Set("key_id", "eq."+apiKeyID)
	q.Set("select", "tokens_used,token_limit")
	lookupURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/usage?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		p.logger.Printf("quota: refused (building supabase request failed, key id=%s): %v", apiKeyID, err)
		return false
	}
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+p.supabaseServiceRoleKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("quota: refused (supabase lookup failed, key id=%s): %v", apiKeyID, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.logger.Printf("quota: refused (supabase status=%d, key id=%s)", resp.StatusCode, apiKeyID)
		return false
	}

	var rows []struct {
		TokensUsed int  `json:"tokens_used"`
		TokenLimit *int `json:"token_limit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAuthResponseBytes)).Decode(&rows); err != nil {
		p.logger.Printf("quota: refused (decoding supabase response failed, key id=%s): %v", apiKeyID, err)
		return false
	}

	if len(rows) != 1 {
		p.logger.Printf("quota: refused (key id=%s, matches=%d)", apiKeyID, len(rows))
		return false
	}

	row := rows[0]
	if row.TokenLimit == nil {
		p.logger.Printf("quota: refused (key id=%s, used=%d, limit=null)", apiKeyID, row.TokensUsed)
		return false
	}

	if row.TokensUsed >= *row.TokenLimit {
		p.logger.Printf("quota: refused (key id=%s, used=%d, limit=%d)", apiKeyID, row.TokensUsed, *row.TokenLimit)
		return false
	}

	p.logger.Printf("quota: ok (key id=%s, used=%d, limit=%d)", apiKeyID, row.TokensUsed, *row.TokenLimit)
	return true
}

// keyPrefix returns at most the first keyLogPrefixLen characters of key, for
// content-free log lines -- never the full key.
func keyPrefix(key string) string {
	if len(key) <= keyLogPrefixLen {
		return key
	}
	return key[:keyLogPrefixLen]
}

// streamSSE copies body to w line by line, flushing after every line so the
// caller sees each chunk as it arrives rather than after the full response
// completes -- the entire point of this proxy is to never buffer inference
// output the way a naive io.Copy-after-io.ReadAll would. It also
// opportunistically extracts and logs the serving provider name from a
// "provider" field on a chunk, purely for observability (mirrors the
// daemon's own onProvider callback in provider.go), without ever logging
// chunk content: extractProvider's return type is a bare string containing
// only the provider name, never the raw line.
//
// keyID is the authorized caller's api_keys.id (from authorize; never the
// raw Mochiii key -- that value is never seen this far into the call path).
// If the upstream response includes a final usage chunk (see extractUsage;
// requires the daemon to have set stream_options.include_usage, which it
// always does -- see daemon/provider.go), streamSSE records it against keyID
// AFTER the full response has already been relayed to the client: a
// fire-and-forget call on its own detached context (see recordUsage) that
// never delays, blocks, or fails the client's completion.
func (p *proxy) streamSSE(w http.ResponseWriter, body io.Reader, keyID string) {
	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	providerLogged := false
	totalTokens := 0
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}

		if !providerLogged {
			if provider, ok := extractProvider(line); ok {
				p.logger.Printf("provider served: %s", provider)
				providerLogged = true
			}
		}
		if tokens, ok := extractUsage(line); ok {
			totalTokens = tokens
		}
	}
	if err := scanner.Err(); err != nil {
		p.logger.Printf("streaming upstream response failed: %v", err)
	}

	if totalTokens > 0 {
		go p.recordUsage(keyID, totalTokens)
	}
}

// extractProvider looks at one SSE line and, if it's a "data: {...}" chunk
// carrying OpenRouter's optional top-level "provider" field, returns it.
// The decode target only ever has a Provider field -- content/delta fields
// are deliberately not part of this struct, so message text can never end
// up in a log line even by accident.
func extractProvider(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return "", false
	}
	var peek struct {
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(data), &peek); err != nil || peek.Provider == "" {
		return "", false
	}
	return peek.Provider, true
}

// extractUsage looks at one SSE line and, if it's a "data: {...}" chunk
// carrying OpenRouter's optional top-level "usage" field (present on the
// final chunk of a stream when the request set stream_options.include_usage
// -- see daemon/provider.go), returns the total token count. Mirrors
// extractProvider deliberately: the decode target has ONLY a
// Usage.TotalTokens field. Content/delta fields are not part of this struct,
// so message text can never end up in a log or a DB write even by accident.
func extractUsage(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return 0, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return 0, false
	}
	var peek struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &peek); err != nil || peek.Usage.TotalTokens == 0 {
		return 0, false
	}
	return peek.Usage.TotalTokens, true
}

// recordUsage increments keyID's tokens_used by tokens via the
// increment_usage Postgres RPC. An RPC (rather than a PostgREST PATCH) is
// required here: PATCH can only set a column to a literal value, not express
// "tokens_used = tokens_used + tokens", and a read-then-write from this
// process would race concurrent requests against the same key and lose
// updates. If the RPC is missing (404) or errors, that is logged (key id +
// token count only -- never content, never any key material) and dropped;
// it is never retried and never surfaced to the client, which already has
// its complete response by the time this runs (see streamSSE). Runs on a
// context detached from the original request, since that request's context
// may already be canceled by the time streamSSE's scan loop finishes.
func (p *proxy) recordUsage(keyID string, tokens int) {
	if p.supabaseURL == "" || p.supabaseServiceRoleKey == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), usageUpdateTimeout)
	defer cancel()

	payload, err := json.Marshal(struct {
		KeyID  string `json:"p_key_id"`
		Tokens int    `json:"p_tokens"`
	}{KeyID: keyID, Tokens: tokens})
	if err != nil {
		p.logger.Printf("usage: encoding RPC body failed (key_id=%s): %v", keyID, err)
		return
	}

	rpcURL := strings.TrimRight(p.supabaseURL, "/") + "/rest/v1/rpc/increment_usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		p.logger.Printf("usage: building RPC request failed (key_id=%s): %v", keyID, err)
		return
	}
	req.Header.Set("apikey", p.supabaseServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+p.supabaseServiceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Printf("usage: RPC call failed (key_id=%s, tokens=%d): %v", keyID, tokens, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxAuthResponseBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.logger.Printf("usage: RPC returned status=%d (key_id=%s, tokens=%d)", resp.StatusCode, keyID, tokens)
		return
	}

	p.logger.Printf("usage: recorded tokens=%d for key_id=%s", tokens, keyID)
}
