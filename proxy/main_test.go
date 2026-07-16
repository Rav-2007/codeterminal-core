package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUsageStore stands in for the `usage` table's single row, guarded by
// a mutex so reserve mirrors reserve_usage's real atomic
// UPDATE...WHERE...RETURNING semantics under concurrent callers -- see
// QUOTA_RESERVATION_DESIGN.md §8: real Postgres's single-statement
// atomicity is a documented guarantee, not what's under test here; this
// fake exists to prove the Go-side request/response handling around it is
// correct under real goroutine concurrency.
type fakeUsageStore struct {
	mu          sync.Mutex
	tokensUsed  int64
	tokenLimit  int64
	corrections []int64
}

func (s *fakeUsageStore) reserve(reserved int64) (tokensUsed, tokenLimit int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokensUsed+reserved > s.tokenLimit {
		return 0, 0, false
	}
	s.tokensUsed += reserved
	return s.tokensUsed, s.tokenLimit, true
}

func (s *fakeUsageStore) correct(delta int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokensUsed += delta
	s.corrections = append(s.corrections, delta)
}

func (s *fakeUsageStore) correctionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.corrections)
}

// newFakeSupabase serves the three PostgREST endpoints reserveQuota,
// correctUsage, and authorize actually call: /rest/v1/api_keys (identity
// lookup -- always resolves to keyID here, since authorize's own hashing
// logic is unchanged and untested by this file), /rest/v1/rpc/reserve_usage,
// and /rest/v1/rpc/increment_usage, both backed by store.
func newFakeSupabase(store *fakeUsageStore, keyID string) *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{{"id": keyID}})
	})

	mux.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			KeyID    string `json:"p_key_id"`
			Reserved int64  `json:"p_reserved"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		tokensUsed, tokenLimit, ok := store.reserve(body.Reserved)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.Write([]byte(`[]`))
			return
		}
		json.NewEncoder(w).Encode([]map[string]int64{{"tokens_used": tokensUsed, "token_limit": tokenLimit}})
	})

	mux.HandleFunc("/rest/v1/rpc/increment_usage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			KeyID  string `json:"p_key_id"`
			Tokens int64  `json:"p_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		store.correct(body.Tokens)
		w.WriteHeader(http.StatusOK)
	})

	return httptest.NewServer(mux)
}

// sseUsageBody builds a minimal OpenRouter-shaped SSE stream carrying a
// final usage chunk, mirroring what extractUsage/streamSSE expect.
func sseUsageBody(totalTokens int) string {
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"+
			"data: {\"usage\":{\"total_tokens\":%d}}\n\n"+
			"data: [DONE]\n\n", totalTokens)
}

func newFakeUpstream(totalTokens int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, sseUsageBody(totalTokens))
	}))
}

func newTestProxy(supabaseURL, upstreamURL string) *proxy {
	return &proxy{
		apiKey:                 "test-openrouter-key",
		upstreamURL:            upstreamURL,
		logger:                 log.New(io.Discard, "", 0),
		supabaseURL:            supabaseURL,
		supabaseServiceRoleKey: "test-service-role-key",
		client:                 &http.Client{},
	}
}

func newAuthorizedRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mochi_test_key")
	return req
}

func waitForCorrections(t *testing.T, store *fakeUsageStore, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.correctionCount() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d correction(s), got %d", n, store.correctionCount())
}

// 1. Normal single request: reservation succeeds, upstream succeeds with a
// usage chunk, correction delta matches actual-reserved exactly (including
// the negative case, when actual usage undershoots the reservation).
func TestHandleChatCompletions_NormalRequest(t *testing.T) {
	const keyID = "11111111-1111-1111-1111-111111111111"
	const actualTokens = 123 // deliberately far under defaultReservationTokens

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := newFakeUpstream(actualTokens)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	p.handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	waitForCorrections(t, store, 1)
	got := store.corrections[0]
	want := int64(actualTokens - defaultReservationTokens)
	if got != want {
		t.Errorf("correction delta = %d, want %d (actual %d - reserved %d)", got, want, actualTokens, defaultReservationTokens)
	}
}

// 2. Over-limit rejection: reserve_usage returns zero rows -> 429, and
// OpenRouter must never be contacted (the entire point of reserving before
// forwarding).
func TestHandleChatCompletions_OverLimitRejected(t *testing.T) {
	const keyID = "22222222-2222-2222-2222-222222222222"

	// Only 1000 headroom left; defaultReservationTokens (4096) can't fit.
	store := &fakeUsageStore{tokensUsed: 99000, tokenLimit: 100000}
	supabase := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	p.handleChatCompletions(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalled.Load() {
		t.Error("OpenRouter was called despite quota rejection -- must be refused before forwarding")
	}
	if got := store.correctionCount(); got != 0 {
		t.Errorf("correction count = %d, want 0 (nothing was reserved, nothing to refund)", got)
	}
}

// 3. The 8-concurrent scenario that originally exposed the race, reproduced
// against the fake store: limit=100, 50 reserved each -- exactly 2 of 8
// concurrent reservations must succeed, and the store's final tokens_used
// must land at exactly 100, not less (lost update) and not more (the race
// re-admitted).
func TestReserveQuota_EightConcurrent(t *testing.T) {
	const keyID = "33333333-3333-3333-3333-333333333333"
	const n = 8
	const reserved = 50

	store := &fakeUsageStore{tokenLimit: 100}
	supabase := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")

	results := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = p.reserveQuota(context.Background(), keyID, reserved)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, ok := range results {
		if ok {
			succeeded++
		}
	}
	if succeeded != 2 {
		t.Errorf("succeeded = %d, want 2 (limit=100, reserved=50 each -> exactly 2 fit)", succeeded)
	}

	store.mu.Lock()
	finalUsed := store.tokensUsed
	store.mu.Unlock()
	if finalUsed != 100 {
		t.Errorf("final tokens_used = %d, want 100 (no lost updates, no over-admission)", finalUsed)
	}
}

// 4. Failed-call refund: the upstream call errors out entirely (connection
// refused, before any response) after a successful reservation -- must
// refund the full reservation, and the client must see 502.
func TestHandleChatCompletions_FailedUpstreamRefunds(t *testing.T) {
	const keyID = "44444444-4444-4444-4444-444444444444"

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// Start then immediately close a server to get a reliable
	// connection-refused, rather than depending on an unroutable address.
	deadUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadUpstreamURL := deadUpstream.URL
	deadUpstream.Close()

	p := newTestProxy(supabase.URL, deadUpstreamURL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	p.handleChatCompletions(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}

	waitForCorrections(t, store, 1)
	got := store.corrections[0]
	want := int64(-defaultReservationTokens)
	if got != want {
		t.Errorf("refund delta = %d, want %d (full refund of the reservation)", got, want)
	}
}

// Bonus coverage for the two new narrow-peek functions the reservation
// sizing (§3) and the non-SSE true-up path (§5(c)) both depend on --
// small, but a silent bug in either would misprice every reservation.
func TestPeekMaxTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		ok   bool
	}{
		{"present and positive", `{"model":"x","max_tokens":512}`, 512, true},
		{"absent", `{"model":"x"}`, 0, false},
		{"zero", `{"model":"x","max_tokens":0}`, 0, false},
		{"negative", `{"model":"x","max_tokens":-5}`, 0, false},
		{"malformed json", `{not json`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := peekMaxTokens([]byte(c.body))
			if got != c.want || ok != c.ok {
				t.Errorf("peekMaxTokens(%q) = (%d, %v), want (%d, %v)", c.body, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestPeekUsageTotal(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
		ok   bool
	}{
		{"present", `{"usage":{"total_tokens":77}}`, 77, true},
		{"absent", `{"choices":[]}`, 0, false},
		{"zero", `{"usage":{"total_tokens":0}}`, 0, false},
		{"malformed json", `{not json`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := peekUsageTotal([]byte(c.body))
			if got != c.want || ok != c.ok {
				t.Errorf("peekUsageTotal(%q) = (%d, %v), want (%d, %v)", c.body, got, ok, c.want, c.ok)
			}
		})
	}
}
