package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
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

	// The pending_corrections outbox (§5(e), migration 0002). reserve opens a
	// row, correct closes the one it is handed, sweep claims what is left.
	// Modeled here rather than stubbed because the whole point of the outbox is
	// what survives when a correction never arrives -- a stub that always closed
	// would test nothing.
	nextPendingID int64
	pending       map[int64]fakePendingRow
	// pendingIDsSeen records every p_pending_id apply_correction was called
	// with, including 0 -- which is what a correction that dropped the field
	// would send, and is how the fail-when-neutered assertions catch it.
	pendingIDsSeen []int64
}

type fakePendingRow struct {
	id        int64
	keyID     string
	reserved  int64
	createdAt time.Time
}

func (s *fakeUsageStore) reserve(keyID string, reserved int64) (tokensUsed, tokenLimit, pendingID int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokensUsed+reserved > s.tokenLimit {
		return 0, 0, 0, false
	}
	s.tokensUsed += reserved
	// Mirrors reserve_usage's data-modifying CTE: the outbox row is opened by
	// the same call that reserves, never a second one, and a refusal (above)
	// opens none.
	s.nextPendingID++
	if s.pending == nil {
		s.pending = make(map[int64]fakePendingRow)
	}
	s.pending[s.nextPendingID] = fakePendingRow{keyID: keyID, reserved: reserved, createdAt: time.Now()}
	return s.tokensUsed, s.tokenLimit, s.nextPendingID, true
}

// correct mirrors apply_correction: increment AND close the outbox row, in one
// step. A pendingID that matches nothing (already swept, or never sent) leaves
// the map alone, exactly as the real DELETE matching no row would.
func (s *fakeUsageStore) correct(delta, pendingID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokensUsed += delta
	s.corrections = append(s.corrections, delta)
	s.pendingIDsSeen = append(s.pendingIDsSeen, pendingID)
	delete(s.pending, pendingID)
}

// sweep mirrors sweep_pending_corrections: claim every row older than the
// cutoff and hand it back. Deliberately does NOT touch tokensUsed -- a swept
// reservation stays charged, which several tests assert directly.
func (s *fakeUsageStore) sweep(staleMinutes int) []fakePendingRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(staleMinutes) * time.Minute)
	var claimed []fakePendingRow
	var ids []int64
	for id, row := range s.pending {
		if row.createdAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		row := s.pending[id]
		row.id = id
		claimed = append(claimed, row)
		delete(s.pending, id)
	}
	return claimed
}

// backdatePending ages every open outbox row, standing in for the passage of
// time between a crash and the sweep that finds what it stranded.
func (s *fakeUsageStore) backdatePending(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, row := range s.pending {
		row.createdAt = row.createdAt.Add(-d)
		s.pending[id] = row
	}
}

func (s *fakeUsageStore) correctionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.corrections)
}

// openPendingCount is the assertion that matters for §5(e): a healthy request
// must leave zero rows behind, or the sweep would later report it as an
// abandoned reservation that never happened.
func (s *fakeUsageStore) openPendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// newFakeSupabase serves the four PostgREST endpoints the proxy actually
// calls: /rest/v1/api_keys (identity lookup -- always resolves to keyID here,
// since authorize's own hashing logic is unchanged and untested by this file),
// /rest/v1/rpc/reserve_usage, /rest/v1/rpc/apply_correction, and
// /rest/v1/rpc/sweep_pending_corrections, all backed by store.
//
// /rest/v1/rpc/increment_usage is deliberately NOT served. That RPC still
// exists in the real database (migration 0002 leaves it in place), but nothing
// in the proxy calls it any more, and leaving it unserved is what makes the
// fail-when-neutered check bite: reverting correctUsage to increment_usage
// makes the call 404, a 404 is non-retryable, so no correction is ever
// recorded and every correction assertion in this file fails.
//
// It also counts requests, so a test can assert that a refused reservation
// makes exactly one call -- i.e. that the outbox row is opened by the
// reservation statement itself and never by a second round trip that a crash
// could land between.
func newFakeSupabase(store *fakeUsageStore, keyID string) (*httptest.Server, *atomic.Int64) {
	var calls atomic.Int64
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{{"id": keyID}})
	})

	mux.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			KeyID    string `json:"p_key_id"`
			Reserved int64  `json:"p_reserved"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		tokensUsed, tokenLimit, pendingID, ok := store.reserve(body.KeyID, body.Reserved)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.Write([]byte(`[]`))
			return
		}
		json.NewEncoder(w).Encode([]map[string]int64{{
			"tokens_used": tokensUsed,
			"token_limit": tokenLimit,
			"pending_id":  pendingID,
		}})
	})

	mux.HandleFunc("/rest/v1/rpc/apply_correction", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			KeyID     string `json:"p_key_id"`
			Tokens    int64  `json:"p_tokens"`
			PendingID int64  `json:"p_pending_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		store.correct(body.Tokens, body.PendingID)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/rest/v1/rpc/sweep_pending_corrections", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			StaleMinutes int `json:"p_stale_minutes"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		claimed := store.sweep(body.StaleMinutes)
		rows := make([]map[string]any, 0, len(claimed))
		for _, row := range claimed {
			rows = append(rows, map[string]any{
				"id":         row.id,
				"key_id":     row.keyID,
				"reserved":   row.reserved,
				"created_at": row.createdAt.UTC().Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	})

	return httptest.NewServer(mux), &calls
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

// newTestProxy goes through the real constructor rather than a struct literal,
// so tests exercise the same wiring production uses -- including the admission
// limiters, which a literal would leave nil (i.e. silently "unlimited", the very
// state this suite should be able to catch).
func newTestProxy(supabaseURL, upstreamURL string) *proxy {
	// Unrestricted model set here so existing tests (which use model "x") are
	// unaffected; the allow-list has its own dedicated tests.
	return newProxy("test-openrouter-key", upstreamURL, supabaseURL,
		"test-service-role-key", log.New(io.Discard, "", 0), nil)
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
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := newFakeUpstream(actualTokens)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")

	results := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = p.reserveQuota(context.Background(), keyID, reserved)
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
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// Start then immediately close a server to get a reliable
	// connection-refused, rather than depending on an unroutable address.
	deadUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadUpstreamURL := deadUpstream.URL
	deadUpstream.Close()

	p := newTestProxy(supabase.URL, deadUpstreamURL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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

// TestModelAllowList_RefusesUnlistedModelBeforeForwarding covers cost
// authorization (endpoint audit class 1): quota is metered in TOKENS but billed
// in DOLLARS, so an unrestricted model choice lets a small token budget buy a
// large spend. Proven live before the fix: an arbitrary model was relayed
// upstream verbatim.
func TestModelAllowList_RefusesUnlistedModelBeforeForwarding(t *testing.T) {
	const keyID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var upstreamCalled atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, sseUsageBody(5))
	}))
	defer upstream.Close()

	p := newProxy("k", upstream.URL, supabase.URL, "sr", log.New(io.Discard, "", 0),
		parseAllowedModels(defaultAllowedModels))

	// An expensive model outside the shipped tier set must be refused.
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(
		`{"model":"openai/o1-pro-very-expensive","messages":[],"stream":true}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a model outside the allow-list; body=%s",
			rec.Code, rec.Body.String())
	}
	if upstreamCalled.Load() {
		t.Error("a disallowed model still reached OpenRouter — the check must run BEFORE forwarding")
	}
	if store.correctionCount() != 0 || store.tokensUsed != 0 {
		t.Error("a refused model must not reserve or spend quota")
	}

	// The shipped primary must still work.
	rec2 := httptest.NewRecorder()
	p.handleChatCompletions(rec2, newAuthorizedRequest(
		`{"model":"deepseek/deepseek-v4-flash","messages":[],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`))
	if rec2.Code != http.StatusOK {
		t.Fatalf("allowed model got %d, want 200; the allow-list must not break real traffic", rec2.Code)
	}
	if !upstreamCalled.Load() {
		t.Error("allowed model never reached upstream")
	}
}

func TestParseAllowedModels(t *testing.T) {
	got := parseAllowedModels(" a/b , c/d ,, ")
	if len(got) != 2 || !got["a/b"] || !got["c/d"] {
		t.Errorf("parseAllowedModels = %v, want {a/b, c/d} with blanks dropped", got)
	}
	if len(parseAllowedModels("")) != 0 {
		t.Error("empty string must yield an empty (unrestricted) set")
	}
}

// --- admission control (endpoint audit class 4: missing limits) ---------------

// TestRateLimiter_AllowsBurstThenThrottles pins the token-bucket contract: a
// fresh key starts full (a first request is never throttled), the burst is
// spendable, and the next request past it is refused.
func TestRateLimiter_AllowsBurstThenThrottles(t *testing.T) {
	l := newRateLimiter(1.0, 3.0)
	now := time.Now()
	l.now = func() time.Time { return now } // freeze: no refill during the burst

	for i := 0; i < 3; i++ {
		if !l.allow("k") {
			t.Fatalf("request %d within the burst of 3 was throttled", i+1)
		}
	}
	if l.allow("k") {
		t.Fatal("request past the burst was admitted — the bucket is not bounding anything")
	}
	// Tokens accrue at 1/s, so a second later exactly one more gets through.
	now = now.Add(1100 * time.Millisecond)
	if !l.allow("k") {
		t.Fatal("bucket did not refill after 1.1s at 1 token/sec")
	}
	if l.allow("k") {
		t.Fatal("bucket refilled by more than the elapsed time allows")
	}
}

// TestRateLimiter_KeysAreIndependent pins that one noisy tenant cannot throttle
// another — the limiter is per key, not global.
func TestRateLimiter_KeysAreIndependent(t *testing.T) {
	l := newRateLimiter(1.0, 2.0)
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		l.allow("noisy")
	}
	if l.allow("noisy") {
		t.Fatal("noisy key was not throttled")
	}
	if !l.allow("quiet") {
		t.Fatal("a different key was throttled by the noisy key's usage")
	}
}

// TestInFlightLimiter_PerKeyAndGlobalCeilings covers the semaphore: a rate limit
// alone does not bound CONCURRENT long-lived streams, which is what pins
// goroutines and upstream connections.
func TestInFlightLimiter_PerKeyAndGlobalCeilings(t *testing.T) {
	l := newInFlightLimiter(2, 3)

	r1, ok1 := l.acquire("a")
	_, ok2 := l.acquire("a")
	if !ok1 || !ok2 {
		t.Fatal("the first two slots for one key should be admitted")
	}
	if _, ok := l.acquire("a"); ok {
		t.Fatal("third concurrent request for one key exceeded maxPerKey but was admitted")
	}
	if _, ok := l.acquire("b"); !ok {
		t.Fatal("a different key was blocked by another key's in-flight usage")
	}
	if _, ok := l.acquire("c"); ok {
		t.Fatal("global in-flight ceiling was exceeded")
	}

	// Releasing frees exactly one slot, and is idempotent.
	r1()
	r1()
	if _, ok := l.acquire("c"); !ok {
		t.Fatal("releasing a slot did not free capacity")
	}
}

// TestHandleChatCompletions_ThrottlesAFloodOnOneKey is the end-to-end regression
// for the measured finding: 40 simultaneous requests on one key produced 40
// concurrent upstream calls with zero throttling. Now the excess must be refused
// with 429 and must never reach OpenRouter.
func TestHandleChatCompletions_ThrottlesAFloodOnOneKey(t *testing.T) {
	const keyID = "99999999-9999-9999-9999-999999999999"
	store := &fakeUsageStore{tokenLimit: 100_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, sseUsageBody(5))
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)

	const n = 60 // deliberately past keyBurst (20)
	var throttled, served atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			p.handleChatCompletions(rec, newAuthorizedRequest(
				`{"model":"deepseek/deepseek-v4-flash","messages":[],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`))
			switch rec.Code {
			case http.StatusTooManyRequests:
				throttled.Add(1)
			case http.StatusOK:
				served.Add(1)
			}
		}()
	}
	wg.Wait()

	if throttled.Load() == 0 {
		t.Fatalf("a %d-request flood on ONE key was not throttled at all (served=%d) — "+
			"the proxy is still unbounded", n, served.Load())
	}
	if upstreamCalls.Load() > served.Load() {
		t.Errorf("more upstream calls (%d) than served responses (%d): throttled requests "+
			"must never reach OpenRouter (they cost money)", upstreamCalls.Load(), served.Load())
	}
	if served.Load() == 0 {
		t.Error("the limiter refused everything; legitimate traffic must still pass")
	}
	t.Logf("flood of %d on one key: served=%d throttled=%d upstream=%d",
		n, served.Load(), throttled.Load(), upstreamCalls.Load())
}

// TestPreAuthLimit_BoundsSupabaseAmplification pins that unauthenticated floods
// are bounded BEFORE the Supabase lookup, so bad keys cannot amplify into a
// third-party dependency.
func TestPreAuthLimit_BoundsSupabaseAmplification(t *testing.T) {
	const keyID = "77777777-7777-7777-7777-777777777777"
	store := &fakeUsageStore{tokenLimit: 100000}

	var lookups atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`)) // never a valid key: every attempt is a failed auth
	})
	supabase := httptest.NewServer(mux)
	defer supabase.Close()
	_ = store

	p := newTestProxy(supabase.URL, "http://unused.invalid")

	const n = 300 // well past preAuthBurstPerSource (20)
	throttled := 0
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"x","messages":[]}`))
		req.Header.Set("Authorization", "Bearer wrong_key")
		p.handleChatCompletions(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatalf("%d failed auth attempts were never throttled — each is a Supabase round trip", n)
	}
	if lookups.Load() >= int64(n) {
		t.Errorf("Supabase was hit %d times for %d attempts; the pre-auth limit must cut "+
			"amplification before the lookup", lookups.Load(), n)
	}
	t.Logf("%d bad-auth attempts: %d throttled, only %d reached Supabase",
		n, throttled, lookups.Load())
}

// TestStripSSEAccountMetadata covers the streaming-path scrub (endpoint audit
// class 5 / M9): stripAccountMetadata was wired only to the non-SSE branch while
// the daemon always sets stream:true, so account-identifying fields reached every
// caller on the only path real traffic uses. Proven live before the fix.
func TestStripSSEAccountMetadata(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			"strips user_id, keeps the rest",
			`data: {"user_id":"org-SECRET","choices":[{"delta":{"content":"hi"}}]}`,
			`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		},
		{
			// The common case must be forwarded byte-for-byte: no re-marshal, so
			// key order and spacing are preserved exactly as upstream sent them.
			"ordinary chunk passes through unchanged",
			`data: {"choices":[{"delta":{"content":"hi"}}],"provider":"DeepInfra"}`,
			`data: {"choices":[{"delta":{"content":"hi"}}],"provider":"DeepInfra"}`,
		},
		{"[DONE] untouched", `data: [DONE]`, `data: [DONE]`},
		{"non-data line untouched", `event: ping`, `event: ping`},
		{"malformed JSON passed through, not mangled", `data: {not json`, `data: {not json`},
		{"empty data untouched", `data: `, `data: `},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripSSEAccountMetadata(c.line); got != c.want {
				t.Errorf("stripSSEAccountMetadata(%q)\n got = %q\nwant = %q", c.line, got, c.want)
			}
		})
	}
}

// TestStreamSSE_StripsAccountMetadataEndToEnd drives the real streamSSE relay and
// asserts the account field never reaches the client, while the surrounding
// stream (content + usage + [DONE]) is delivered intact.
func TestStreamSSE_StripsAccountMetadataEndToEnd(t *testing.T) {
	const keyID = "88888888-8888-8888-8888-888888888888"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"user_id":"org-SECRET-ACCOUNT","choices":[{"delta":{"content":"x"}}]}` + "\n\n" +
		`data: {"usage":{"total_tokens":7}}` + "\n\n" +
		`data: [DONE]` + "\n\n"

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	p.streamSSE(rec, strings.NewReader(upstream), keyID, defaultReservationTokens, 1)

	got := rec.Body.String()
	if strings.Contains(got, "org-SECRET-ACCOUNT") || strings.Contains(got, "user_id") {
		t.Errorf("account metadata reached the client on the streaming path:\n%s", got)
	}
	for _, must := range []string{`"content":"hi"`, `"total_tokens":7`, "[DONE]"} {
		if !strings.Contains(got, must) {
			t.Errorf("stream lost %q; relay must only remove account fields:\n%s", must, got)
		}
	}
}

// abortingResponseWriter simulates a client that disconnects mid-stream: it
// relays writes normally until it sees a line containing failOn (the trailing
// usage chunk), then returns an error on that write the way a real broken
// connection would. This models the exploit -- a client that reads the full
// answer, then drops just before the usage line -- which streamSSE's write
// error path handles.
type abortingResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	failOn     string
	statusCode int
}

func (w *abortingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *abortingResponseWriter) WriteHeader(code int) { w.statusCode = code }

func (w *abortingResponseWriter) Write(b []byte) (int, error) {
	if w.failOn != "" && bytes.Contains(b, []byte(w.failOn)) {
		return 0, errors.New("simulated client disconnect")
	}
	return w.body.Write(b)
}

// Flush is a no-op; streamSSE type-asserts http.Flusher and flushes each line.
func (w *abortingResponseWriter) Flush() {}

// TestHandleChatCompletions_ClientAbortsBeforeUsageChunk_DoesNotRefund is the
// regression for the C3 abort-refund bug. A client reads the full answer, then
// disconnects just before the terminal usage chunk -- upstream already produced
// (and billed) the completion, so the reservation must be KEPT, not refunded.
// A full refund here is free, repeatable, exploitable inference on the paid
// tier. Fail-when-neutered: revert finalizeUsage's producedOutput branch to a
// refund and this observes the -reserved delta instead of 0.
//
// Since the outbox landed (§5(e)) this branch also has to CLOSE its
// pending_corrections row with a zero-delta correction rather than returning
// early the way it used to. It was the one branch that legitimately had no
// accounting work to do, and skipping the call left a live row behind on a
// perfectly healthy request -- which the sweep would then report ~20 minutes
// later as an abandoned reservation that never happened. That is asserted here
// too, because this path (clients disconnecting mid-stream) is by far the most
// frequent way to hit it, so the false alarm would have been the common case.
func TestHandleChatCompletions_ClientAbortsBeforeUsageChunk_DoesNotRefund(t *testing.T) {
	const keyID = "55555555-5555-5555-5555-555555555555"
	const actualTokens = 900 // upstream WOULD report this, but the client aborts first

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := newFakeUpstream(actualTokens)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
	// Fail the relay write that carries the usage chunk.
	rec := &abortingResponseWriter{failOn: "usage"}

	p.handleChatCompletions(rec, req)

	// Exactly one correction, and it must be a no-op numerically: the
	// reservation stays as the charge.
	waitForCorrections(t, store, 1)
	if n := store.correctionCount(); n != 1 {
		t.Fatalf("correction count = %d, want exactly 1 (the zero-delta outbox close); deltas=%v", n, store.corrections)
	}
	if got := store.corrections[0]; got != 0 {
		t.Errorf("delta = %d, want 0 (aborted-but-produced request must keep its reservation, not refund)", got)
	}
	store.mu.Lock()
	used := store.tokensUsed
	store.mu.Unlock()
	if used != int64(defaultReservationTokens) {
		t.Errorf("tokens_used = %d, want %d (reservation kept, nothing refunded)", used, defaultReservationTokens)
	}
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("open outbox rows = %d, want 0 -- this branch must close its own row or the sweep will later report a healthy request as an abandoned reservation", n)
	}
}

// TestFinalizeUsage_ProducedNoOutput_FullRefund pins that a request which
// produced no billable output (upstream never reached, or a non-billable error
// response) is still fully refunded -- the correct behavior for the two error
// call sites in handleChatCompletions.
func TestFinalizeUsage_ProducedNoOutput_FullRefund(t *testing.T) {
	const keyID = "66666666-6666-6666-6666-666666666666"
	const reserved = 4096

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	p := newTestProxy(supabase.URL, "http://unused.invalid")

	// Open a real outbox row first, so the refund has something to close and
	// this test also covers the row actually being closed.
	_, _, pendingID, _ := store.reserve(keyID, reserved)

	p.finalizeUsage(keyID, reserved, 0, false, pendingID)

	waitForCorrections(t, store, 1)
	if got, want := store.corrections[0], int64(-reserved); got != want {
		t.Errorf("delta = %d, want %d (full refund when no output was produced)", got, want)
	}
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("open outbox rows = %d, want 0 (the refund must close its row)", n)
	}
}

// TestFinalizeUsage_TrueUpUnchanged pins that a request with a real usage
// figure still trues up to it exactly (including a positive top-up when actual
// exceeds the reservation) -- the fix must not disturb the normal path.
func TestFinalizeUsage_TrueUpUnchanged(t *testing.T) {
	const keyID = "77777777-7777-7777-7777-777777777777"
	const reserved = 4096
	const actual = 5000 // > reserved: a positive top-up

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	p := newTestProxy(supabase.URL, "http://unused.invalid")

	_, _, pendingID, _ := store.reserve(keyID, reserved)

	p.finalizeUsage(keyID, reserved, actual, true, pendingID)

	waitForCorrections(t, store, 1)
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("open outbox rows = %d, want 0 (the true-up must close its row)", n)
	}
	if got, want := store.corrections[0], int64(actual-reserved); got != want {
		t.Errorf("delta = %d, want %d (true-up to the real usage figure)", got, want)
	}
}

// ---------------------------------------------------------------------------
// QUOTA_RESERVATION_DESIGN.md §5(e): the pending_corrections outbox and the
// reconciliation sweep. §5(d)'s retry covers a correction whose CALL failed;
// none of it can cover a correction whose PROCESS died, because the goroutine
// that would have retried died too. These cover the durable record that is left
// behind instead, and what finds it.
// ---------------------------------------------------------------------------

// TestReserveQuota_ReturnsPendingID pins the reservation half of the outbox: a
// granted reservation must hand back the id of the row that records a
// correction is owed, and a refused one must hand back nothing while making no
// extra round trip.
//
// The round-trip count is the real assertion in the refusal case. A separate
// INSERT after the UPDATE would reintroduce, inside the outbox, exactly the
// crash-in-the-gap hole the outbox exists to close -- so "one call" is the
// property, not an optimization.
func TestReserveQuota_ReturnsPendingID(t *testing.T) {
	t.Run("granted returns a nonzero pending id", func(t *testing.T) {
		const keyID = "aaaa1111-0000-0000-0000-000000000001"
		store := &fakeUsageStore{tokenLimit: 100000}
		supabase, calls := newFakeSupabase(store, keyID)
		defer supabase.Close()
		p := newTestProxy(supabase.URL, "http://unused.invalid")

		pendingID, ok := p.reserveQuota(context.Background(), keyID, defaultReservationTokens)
		if !ok {
			t.Fatal("reserveQuota refused a reservation that fits well under the limit")
		}
		if pendingID == 0 {
			t.Error("pendingID = 0 on a granted reservation -- nothing durable records that a correction is owed, so a crash here is invisible to the sweep")
		}
		if n := store.openPendingCount(); n != 1 {
			t.Errorf("open outbox rows = %d, want 1 (the reservation must open exactly one)", n)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("supabase calls = %d, want 1 -- the outbox row must be opened by the reservation statement itself, not a second round trip a crash could land between", n)
		}
	})

	t.Run("refused returns zero and opens no row", func(t *testing.T) {
		const keyID = "aaaa1111-0000-0000-0000-000000000002"
		// Only 1000 headroom; defaultReservationTokens (4096) cannot fit.
		store := &fakeUsageStore{tokensUsed: 99000, tokenLimit: 100000}
		supabase, calls := newFakeSupabase(store, keyID)
		defer supabase.Close()
		p := newTestProxy(supabase.URL, "http://unused.invalid")

		pendingID, ok := p.reserveQuota(context.Background(), keyID, defaultReservationTokens)
		if ok {
			t.Fatal("reserveQuota granted a reservation that exceeds the limit")
		}
		if pendingID != 0 {
			t.Errorf("pendingID = %d, want 0 on a refusal", pendingID)
		}
		if n := store.openPendingCount(); n != 0 {
			t.Errorf("open outbox rows = %d, want 0 -- a refusal reserved nothing, so it owes no correction and must leave no row for the sweep to alarm on", n)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("supabase calls = %d, want 1 (the single refused reserve_usage call, with no separate outbox write)", n)
		}
	})
}

// TestCorrectUsage_PostsApplyCorrectionWithPendingID pins the wire contract of
// the correction itself: the RPC it targets and the field that closes the
// outbox row.
//
// Fail-when-neutered, both directions: point correctUsage back at
// increment_usage and the path assertion fails (and, against the shared fake,
// the call 404s so no correction lands at all); drop p_pending_id from the
// payload and the field assertion fails while the row stays open forever.
func TestCorrectUsage_PostsApplyCorrectionWithPendingID(t *testing.T) {
	const keyID = "bbbb2222-0000-0000-0000-000000000001"
	const wantPendingID = int64(4242)
	const wantDelta = -1234

	type captured struct {
		path      string
		keyID     string
		tokens    int
		pendingID *int64 // pointer so "absent" is distinguishable from 0
	}
	got := make(chan captured, 1)

	supabase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			KeyID     string `json:"p_key_id"`
			Tokens    int    `json:"p_tokens"`
			PendingID *int64 `json:"p_pending_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		got <- captured{path: r.URL.Path, keyID: body.KeyID, tokens: body.Tokens, pendingID: body.PendingID}
		w.WriteHeader(http.StatusOK)
	}))
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	p.correctUsage(keyID, wantDelta, wantPendingID)

	select {
	case c := <-got:
		if c.path != "/rest/v1/rpc/apply_correction" {
			t.Errorf("path = %q, want /rest/v1/rpc/apply_correction -- increment_usage cannot close the outbox row, so a correction sent there leaves the reservation looking abandoned", c.path)
		}
		if c.keyID != keyID {
			t.Errorf("p_key_id = %q, want %q", c.keyID, keyID)
		}
		if c.tokens != wantDelta {
			t.Errorf("p_tokens = %d, want %d", c.tokens, wantDelta)
		}
		if c.pendingID == nil {
			t.Fatal("p_pending_id absent from the payload -- the correction would apply but never close its outbox row, so the sweep would report a handled request as abandoned")
		}
		if *c.pendingID != wantPendingID {
			t.Errorf("p_pending_id = %d, want %d", *c.pendingID, wantPendingID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the correction RPC call")
	}
}

// TestSweepPendingCorrections_ReportsAbandonedReservations is the sweep half.
// It asserts the loud line per claimed row (the actual deliverable -- an
// operator seeing it knows which key was over-charged by how much and when),
// that the claim does NOT refund, and that a clean sweep is silent.
func TestSweepPendingCorrections_ReportsAbandonedReservations(t *testing.T) {
	t.Run("claims and loudly reports each abandoned row", func(t *testing.T) {
		const keyID = "cccc3333-0000-0000-0000-000000000001"
		store := &fakeUsageStore{tokenLimit: 1000000}
		supabase, _ := newFakeSupabase(store, keyID)
		defer supabase.Close()

		// Three reservations whose corrections never arrived, aged past the
		// stale window -- i.e. three crashed requests.
		const abandoned = 3
		for i := 0; i < abandoned; i++ {
			if _, _, _, ok := store.reserve(keyID, defaultReservationTokens); !ok {
				t.Fatalf("setup: reservation %d refused", i)
			}
		}
		// One more that is fresh: an in-flight request must never be swept out
		// from under itself.
		if _, _, _, ok := store.reserve(keyID, defaultReservationTokens); !ok {
			t.Fatal("setup: in-flight reservation refused")
		}
		store.backdatePending(time.Duration(pendingCorrectionStaleAfterMinutes+5) * time.Minute)
		// Re-open the fresh one AFTER backdating so only it is recent.
		if _, _, _, ok := store.reserve(keyID, defaultReservationTokens); !ok {
			t.Fatal("setup: fresh reservation refused")
		}

		store.mu.Lock()
		usedBefore := store.tokensUsed
		store.mu.Unlock()

		var logs bytes.Buffer
		p := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(&logs, "", 0), nil)
		p.sweepPendingCorrections()

		out := logs.String()
		if n := strings.Count(out, "ABANDONED RESERVATION swept"); n != abandoned+1 {
			t.Errorf("ABANDONED RESERVATION lines = %d, want %d; log:\n%s", n, abandoned+1, out)
		}
		// Every claimed row must carry the fields an operator needs to act.
		for _, want := range []string{
			"pending_id=",
			"key_id=" + keyID,
			fmt.Sprintf("reserved=%d", defaultReservationTokens),
			"reserved_at=",
			"stays CHARGED",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("sweep log missing %q; log:\n%s", want, out)
			}
		}

		// The fresh row survives: sweeping a live request's reservation would
		// double-charge it when its real correction lands.
		if n := store.openPendingCount(); n != 1 {
			t.Errorf("open outbox rows after sweep = %d, want 1 (the in-flight reservation must not be claimed)", n)
		}

		// And nothing was refunded. This is the safe-direction decision, not an
		// oversight: the crashed request's true usage is unrecoverable, and
		// refunding hands back quota for inference OpenRouter really billed.
		store.mu.Lock()
		usedAfter := store.tokensUsed
		store.mu.Unlock()
		if usedAfter != usedBefore {
			t.Errorf("tokens_used = %d after sweep, want %d unchanged -- a swept reservation stays charged, never refunded on missing usage data", usedAfter, usedBefore)
		}
	})

	t.Run("silent when there is nothing to sweep", func(t *testing.T) {
		const keyID = "cccc3333-0000-0000-0000-000000000002"
		store := &fakeUsageStore{tokenLimit: 100000}
		supabase, _ := newFakeSupabase(store, keyID)
		defer supabase.Close()

		var logs bytes.Buffer
		p := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(&logs, "", 0), nil)
		p.sweepPendingCorrections()

		// A healthy proxy runs this every 5 minutes forever. Any routine output
		// here trains operators to ignore the one prefix that must stay
		// attention-worthy.
		if out := logs.String(); out != "" {
			t.Errorf("sweep logged on the empty common case, want silence:\n%s", out)
		}
	})
}

// TestReservationSurvivesCrash_SweptNotRefunded is the §5(e) scenario end to
// end, at the only level a unit test can reach it: a request reserves, and the
// correction never runs -- exactly what a SIGKILL between the two produces,
// since finalizeUsage's goroutine dies with the process. The durable row is
// what must be left behind, and the sweep in the NEXT process lifetime is what
// must find it.
//
// The crash is modeled by simply not calling finalizeUsage. That is faithful to
// the failure being closed: from the database's point of view a crashed proxy
// and a proxy that never got to the correction are indistinguishable, and the
// database's point of view is the only one that survives the crash.
func TestReservationSurvivesCrash_SweptNotRefunded(t *testing.T) {
	const keyID = "dddd4444-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// Lifetime 1: reserve, then "crash" -- no correction is ever sent.
	crashed := newTestProxy(supabase.URL, "http://unused.invalid")
	pendingID, ok := crashed.reserveQuota(context.Background(), keyID, defaultReservationTokens)
	if !ok || pendingID == 0 {
		t.Fatalf("setup: reserveQuota returned (%d, %v)", pendingID, ok)
	}
	store.mu.Lock()
	usedAtCrash := store.tokensUsed
	store.mu.Unlock()
	if usedAtCrash != int64(defaultReservationTokens) {
		t.Fatalf("tokens_used at crash = %d, want %d", usedAtCrash, defaultReservationTokens)
	}

	// The reservation record outlives the process that made it. Without this,
	// nothing anywhere knows a correction was ever owed -- the whole of §5(e).
	if n := store.openPendingCount(); n != 1 {
		t.Fatalf("open outbox rows after crash = %d, want 1 (the reservation must outlive the process)", n)
	}

	// Lifetime 2: a restarted proxy (or a sibling replica) sweeps.
	store.backdatePending(time.Duration(pendingCorrectionStaleAfterMinutes+1) * time.Minute)
	var logs bytes.Buffer
	restarted := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(&logs, "", 0), nil)
	restarted.sweepPendingCorrections()

	if !strings.Contains(logs.String(), "ABANDONED RESERVATION swept") {
		t.Errorf("restarted proxy did not report the stranded reservation; log:\n%s", logs.String())
	}
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("open outbox rows after sweep = %d, want 0 (claimed exactly once)", n)
	}
	store.mu.Lock()
	usedAfter := store.tokensUsed
	store.mu.Unlock()
	if usedAfter != usedAtCrash {
		t.Errorf("tokens_used = %d after sweep, want %d unchanged from its post-reservation value -- the sweep reports, it must never refund on usage data that no longer exists", usedAfter, usedAtCrash)
	}
}

// ---------------------------------------------------------------------------
// F1: proxy-side ZDR enforcement (Reject). The proxy is the authority that a
// request carries the required zero-data-retention routing flags, rather than
// trusting the client to have set them. Fail CLOSED, body never mutated (the
// accept path forwards byte-for-byte). See proxy/F1_ENFORCEMENT_DESIGN.md.
// ---------------------------------------------------------------------------

// bodyCapturingUpstream records the exact bytes it received, so a test can prove
// the accept path forwards the caller's body byte-for-byte (the property that
// makes Reject, not Stamp, the chosen design).
func bodyCapturingUpstream(t *testing.T, totalTokens int) (*httptest.Server, *[]byte, *atomic.Bool) {
	t.Helper()
	var mu sync.Mutex
	var got []byte
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = b
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, sseUsageBody(totalTokens))
	}))
	return srv, &got, &called
}

// TestZDRRoutingEnforced_Predicate is the unit-level table for the fail-closed
// predicate: every shape short of an explicit, correct routing object must be
// refused, while extra routing fields (the D4 ignore/only lists) must NOT cause
// a refusal.
func TestZDRRoutingEnforced_Predicate(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"correct flags", `{"model":"x","provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`, true},
		{"correct flags, no allow_fallbacks", `{"provider":{"zdr":true,"data_collection":"deny"}}`, true},
		{"correct flags plus D4 ignore list is still accepted", `{"provider":{"zdr":true,"data_collection":"deny","ignore":["DeepInfra"]}}`, true},
		{"correct flags plus an only list is still accepted", `{"provider":{"zdr":true,"data_collection":"deny","only":["Morph"]}}`, true},
		{"provider object entirely absent -> reject", `{"model":"x","messages":[]}`, false},
		{"zdr false -> reject", `{"provider":{"zdr":false,"data_collection":"deny"}}`, false},
		{"zdr missing -> reject (defaults false)", `{"provider":{"data_collection":"deny"}}`, false},
		{"data_collection allow -> reject", `{"provider":{"zdr":true,"data_collection":"allow"}}`, false},
		{"data_collection missing -> reject", `{"provider":{"zdr":true}}`, false},
		{"provider is not an object -> reject", `{"provider":"zdr"}`, false},
		{"unparseable body -> reject", `{not json`, false},
		{"empty body -> reject", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := zdrRoutingEnforced([]byte(c.body)); got != c.want {
				t.Errorf("zdrRoutingEnforced(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

// TestHandleChatCompletions_RejectsStrippedZDRFlags: a request with the ZDR
// flags stripped is refused with 403, upstream is never called, and no quota is
// reserved or spent -- the fail-closed contract at the forward seam.
func TestHandleChatCompletions_RejectsStrippedZDRFlags(t *testing.T) {
	const keyID = "f1000000-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream, _, called := bodyCapturingUpstream(t, 5)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)

	// A body with no provider routing object at all.
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(
		`{"model":"x","messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a request missing ZDR flags; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "zdr_required") {
		t.Errorf("response body = %q, want it to name zdr_required", rec.Body.String())
	}
	if called.Load() {
		t.Error("a request without ZDR flags reached OpenRouter -- enforcement must refuse BEFORE forwarding")
	}
	if store.correctionCount() != 0 || store.tokensUsed != 0 {
		t.Error("a refused request must not reserve or spend quota")
	}
}

// TestHandleChatCompletions_WeakenedZDRFlagsRejected covers the weakened (not
// merely absent) variants: each must be refused and never forwarded.
func TestHandleChatCompletions_WeakenedZDRFlagsRejected(t *testing.T) {
	const keyID = "f1000000-0000-0000-0000-000000000002"
	weakened := []string{
		`{"model":"x","provider":{"zdr":false,"data_collection":"deny","allow_fallbacks":true}}`,
		`{"model":"x","provider":{"zdr":true,"data_collection":"allow","allow_fallbacks":true}}`,
		`{"model":"x","provider":{"allow_fallbacks":true}}`,
	}
	for _, body := range weakened {
		t.Run(body, func(t *testing.T) {
			store := &fakeUsageStore{tokenLimit: 100000}
			supabase, _ := newFakeSupabase(store, keyID)
			defer supabase.Close()
			upstream, _, called := bodyCapturingUpstream(t, 5)
			defer upstream.Close()

			p := newTestProxy(supabase.URL, upstream.URL)
			rec := httptest.NewRecorder()
			p.handleChatCompletions(rec, newAuthorizedRequest(body))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if called.Load() {
				t.Error("a weakened-ZDR request reached OpenRouter")
			}
		})
	}
}

// TestHandleChatCompletions_ForwardsBodyByteForByte is the accept-path property
// that made Reject the right choice over Stamp: a conforming request passes, and
// the bytes upstream receives are IDENTICAL to the bytes the client sent -- the
// enforcement peek mutated nothing.
func TestHandleChatCompletions_ForwardsBodyByteForByte(t *testing.T) {
	const keyID = "f1000000-0000-0000-0000-000000000003"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream, gotBody, called := bodyCapturingUpstream(t, 42)
	defer upstream.Close()

	// A realistic daemon body: model, messages with content, stream, and the
	// full provider routing object -- deliberately with specific key order and
	// spacing that a re-marshal would perturb.
	const sent = `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hello world"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true},"stream_options":{"include_usage":true}}`

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(sent))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a conforming request; body=%s", rec.Code, rec.Body.String())
	}
	if !called.Load() {
		t.Fatal("conforming request never reached upstream")
	}
	if got := string(*gotBody); got != sent {
		t.Errorf("forwarded body was not byte-for-byte identical\n sent = %s\n  got = %s", sent, got)
	}
}

// TestHandleChatCompletions_AcceptsZDRFlagsWithIgnoreList is the F1/D4 seam: a
// request carrying BOTH the ZDR flags AND a D4 ignore:["DeepInfra"] list must
// pass the reject gate and forward unchanged. F1 must judge only the ZDR flags
// and ignore the extra routing field.
func TestHandleChatCompletions_AcceptsZDRFlagsWithIgnoreList(t *testing.T) {
	const keyID = "f1000000-0000-0000-0000-000000000004"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream, gotBody, called := bodyCapturingUpstream(t, 7)
	defer upstream.Close()

	const sent = `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true,"ignore":["DeepInfra"]}}`

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(sent))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a request with an ignore list must still pass the ZDR gate; body=%s", rec.Code, rec.Body.String())
	}
	if !called.Load() {
		t.Fatal("request carrying both ZDR flags and an ignore list never reached upstream")
	}
	if got := string(*gotBody); got != sent {
		t.Errorf("forwarded body (with ignore list) was not byte-for-byte identical\n sent = %s\n  got = %s", sent, got)
	}
	if !strings.Contains(string(*gotBody), `"ignore":["DeepInfra"]`) {
		t.Error("the ignore list did not survive to upstream")
	}
}

// TestZDRRoutingEnforced_FailWhenNeutered proves the enforcement predicate is
// what does the rejecting -- not some other layer. If the gate is "neutered"
// (made to always return true, i.e. enforcement removed), a stripped-flags
// request would be accepted; the assertion here is that the predicate itself
// returns false for that request, so a neutered predicate is directly
// observable. The handler-level tests above (upstream never called) are what
// catch a neutered gate in the request path; this pins the predicate.
func TestZDRRoutingEnforced_FailWhenNeutered(t *testing.T) {
	stripped := `{"model":"x","messages":[{"role":"user","content":"hi"}]}`
	if zdrRoutingEnforced([]byte(stripped)) {
		t.Fatal("zdrRoutingEnforced accepted a request with no provider routing object -- " +
			"if you are seeing this after replacing the body with `return true`, that is the " +
			"neutered gate this test exists to catch: a stripped-flags request would be forwarded")
	}
}
