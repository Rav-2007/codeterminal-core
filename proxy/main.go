// Command codeterminal-proxy is the smallest possible managed-tier proxy in
// front of OpenRouter. It exists to hold the OpenRouter API key server-side
// so it never ships to end users. It is a dumb pipe: it adds the key and
// forwards the request body exactly as received, then streams the response
// back without buffering. It deliberately does nothing else yet -- no auth,
// no metering, no persistence; those are later steps.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
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

	// maxRequestBodyBytes caps the incoming request body the proxy will
	// read. Generous for a chat-completions payload (prompt + retrieved
	// context + capped history), just enough to stop an unbounded body
	// from exhausting memory.
	maxRequestBodyBytes = 4 << 20 // 4MB

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

	p := &proxy{
		apiKey:      apiKey,
		upstreamURL: strings.TrimRight(upstreamBase, "/") + chatCompletionsPath,
		logger:      logger,
		// No blanket http.Client.Timeout: a streaming response can
		// legitimately run for minutes. Each request's own context
		// deadline (upstreamTimeout, applied per-call below) is what
		// actually bounds it instead.
		client: &http.Client{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v1/chat/completions", p.handleChatCompletions)

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
	apiKey      string
	upstreamURL string
	logger      *log.Logger
	client      *http.Client
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
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
		p.streamSSE(w, resp.Body)
		return
	}

	if _, err := io.Copy(w, resp.Body); err != nil {
		p.logger.Printf("copying non-streamed response failed: %v", err)
	}
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
func (p *proxy) streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	providerLogged := false
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
	}
	if err := scanner.Err(); err != nil {
		p.logger.Printf("streaming upstream response failed: %v", err)
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
