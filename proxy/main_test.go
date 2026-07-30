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

// streamSSEAndFinalize drives streamSSE exactly the way handleChatCompletions
// does: relay the stream, then discharge the reservation through the single
// deferred finalizer.
//
// It exists because streamSSE no longer bills. It RECORDS its result into a
// reservationOutcome and the handler's one deferred finalizeReservation call
// bills it, so that "every reservation is finalized exactly once" is a
// structural property of the handler instead of something four call sites have
// to remember. A test that calls streamSSE alone would therefore assert on a
// stream that was never billed -- which is why every billing-sensitive test
// below goes through this helper rather than the raw method.
func streamSSEAndFinalize(p *proxy, w http.ResponseWriter, body io.Reader, keyID string, reserved int, pendingID int64, ceiling int) *reservationOutcome {
	outcome := &reservationOutcome{keyID: keyID, reserved: reserved, pendingID: pendingID}
	p.streamSSE(w, body, outcome, ceiling)
	p.finalizeReservation(outcome)
	return outcome
}

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
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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
	streamSSEAndFinalize(p, rec, strings.NewReader(upstream), keyID, defaultReservationTokens, 1, absoluteMaxRequestTokens)

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
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
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

		res, ok := p.reserveQuota(context.Background(), keyID, defaultReservationTokens)
		pendingID := res.pendingID
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

		res, ok := p.reserveQuota(context.Background(), keyID, defaultReservationTokens)
		pendingID := res.pendingID
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
	crashRes, ok := crashed.reserveQuota(context.Background(), keyID, defaultReservationTokens)
	pendingID := crashRes.pendingID
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

// ---------------------------------------------------------------------------
// Financial-defense hardening. Each test below guards a control that did not
// exist before the DoW audit, and each is written to FAIL if that control is
// removed -- the same discipline as TestZDRRoutingEnforced_FailWhenNeutered.
// ---------------------------------------------------------------------------

// sseChunks builds a stream of n content chunks with NO usage chunk and no
// [DONE] -- i.e. a completion still in progress. This is the shape that used to
// run unbounded: the proxy only ever learned a token count from the terminal
// usage chunk, so a stream that kept producing was never measured until it
// stopped.
func sseChunks(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
	}
	return b.String()
}

// realisticChunk mirrors an ACTUAL OpenRouter SSE chunk: ~294 bytes of JSON
// envelope wrapping a single token of delta content.
//
// The toy fixture above is 47 bytes, and using it for budget tests is exactly how
// a 73x estimator error shipped with a green suite. Any test that asserts
// something about how much a stream "costs" must use this one, because the
// envelope-to-content ratio IS the thing under test.
const realisticChunk = `data: {"id":"gen-1753600000-AbCdEfGhIjKlMnOp","provider":"DeepSeek",` +
	`"model":"deepseek/deepseek-v4-flash","object":"chat.completion.chunk",` +
	`"created":1753600000,"choices":[{"index":0,"delta":{"role":"assistant",` +
	`"content":" the"},"finish_reason":null,"native_finish_reason":null,` +
	`"logprobs":null}]}`

func sseRealisticChunks(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(realisticChunk)
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestBudgetExceeded(t *testing.T) {
	const ceiling = 1000
	tests := []struct {
		name       string
		dataChunks int
		bytes      int
		wantReason string
		wantOver   bool
	}{
		{"nothing streamed", 0, 0, "", false},
		{"well under both bounds", 500, 500 * 294, "", false},
		// The D1 case: 1000 realistic chunks is exactly the ceiling in TOKENS and
		// 294,000 bytes. The old estimator scored that as 73,500 tokens.
		{"at the ceiling with realistic envelopes", 1000, 1000 * 294, "", false},
		{"one chunk over the token ceiling", 1001, 1001 * 294, "token_ceiling", true},
		// Few chunks, enormous payload: the case chunk-counting alone misses.
		{"byte guard catches batched giant chunks", 3, ceiling*maxBytesPerChunkGuard + 1, "byte_guard", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, over := budgetExceeded(tt.dataChunks, tt.bytes, ceiling)
			if over != tt.wantOver {
				t.Fatalf("budgetExceeded(%d, %d, %d) exceeded = %v, want %v",
					tt.dataChunks, tt.bytes, ceiling, over, tt.wantOver)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestRequestTokenCeiling_CappedByAbsoluteMax(t *testing.T) {
	// A key with an enormous quota must still not be drainable by one request.
	if got := requestTokenCeiling(defaultReservationTokens, 10_000_000); got != absoluteMaxRequestTokens {
		t.Errorf("ceiling with huge headroom = %d, want the absolute cap %d -- "+
			"without the cap, one request can spend an entire enterprise quota", got, absoluteMaxRequestTokens)
	}
	// A nearly-exhausted key must get a ceiling near its actual headroom, not the cap.
	if got := requestTokenCeiling(100, 50); got != 150 {
		t.Errorf("ceiling with 50 headroom = %d, want 150 (reserved+headroom)", got)
	}
}

// The load-bearing test. A stream that keeps producing past the ceiling must be
// CUT, not ridden out. Fails when the ceiling check is removed from streamSSE.
func TestStreamSSE_KillsStreamOverBudget(t *testing.T) {
	const keyID = "bbbb1111-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// 500 chunks offered; ceiling of 10 must stop it long before the end.
	const ceiling = 10
	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	streamSSEAndFinalize(p, rec, strings.NewReader(sseChunks(500)), keyID, defaultReservationTokens, 1, ceiling)

	body := rec.Body.String()
	if !strings.Contains(body, "budget_exceeded") {
		t.Error("killed stream carried no budget_exceeded chunk -- the client cannot " +
			"tell throttling from a dropped connection")
	}
	if !strings.Contains(body, "[DONE]") {
		t.Error("killed stream did not terminate with [DONE]")
	}
	// The estimator counts one token per chunk here, so the relay must stop within
	// a chunk or two of the ceiling -- nowhere near all 500.
	if got := strings.Count(body, `"delta"`); got > ceiling+2 {
		t.Errorf("relayed %d content chunks with a ceiling of %d -- the stream was not "+
			"cut. If you are seeing this after removing the ceiling check in streamSSE, "+
			"that is exactly the unbounded-spend regression this test exists to catch", got, ceiling)
	}
}

// A killed stream must be CHARGED, not refunded. Refunding would make an
// over-budget request free, which is worse than not enforcing at all: it would
// hand back quota for inference OpenRouter really did bill.
func TestStreamSSE_KilledStreamIsChargedNotRefunded(t *testing.T) {
	const keyID = "bbbb1111-0000-0000-0000-000000000002"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	streamSSEAndFinalize(p, rec, strings.NewReader(sseChunks(500)), keyID, defaultReservationTokens, 1, 10)

	waitForCorrections(t, store, 1)
	store.mu.Lock()
	delta := store.corrections[0]
	store.mu.Unlock()

	if delta == -int64(defaultReservationTokens) {
		t.Fatal("a killed stream was fully REFUNDED -- over-budget inference would be " +
			"free and infinitely repeatable")
	}
	// No refund of ANY size. This fixture pairs a ceiling of 10 with the 4096
	// reservation, which cannot occur in production (requestTokenCeiling makes
	// ceiling >= reserved), so the kill fires at ~11 chunks. Before chargeForKill
	// the charge was that raw count and the correction was -4085 -- a partial
	// refund this test accepted, and the same defect P0-1 made unbounded on the
	// byte guard. The floor at `reserved` makes it 0.
	if delta < 0 {
		t.Errorf("correction delta = %d: a killed stream was refunded %d tokens. "+
			"Every kill path must charge at least the reservation", delta, -delta)
	}
}

// bigChunkUpstream emits `chunks` SSE data lines of roughly `size` bytes each and
// NO terminal usage chunk -- the shape a provider that batches many tokens into
// one chunk produces (a reasoning model streaming long `reasoning` deltas, say),
// and therefore the shape that trips budgetExceeded's BYTE guard rather than its
// token ceiling. sseChunks/sseRealisticChunks above can only ever trip the token
// ceiling, which is precisely why the byte bound went untested.
func bigChunkUpstream(chunks, size int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		filler := strings.Repeat("A", size)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", filler)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
}

// The P0-1 regression. A stream killed by the BYTE guard must not produce a
// refund -- the defect this test exists to catch charged the CHUNK COUNT, which
// on this bound is tiny by construction (a few huge chunks) while the bytes
// streamed are at the ceiling. finalizeUsage then took its `actual > 0` branch
// with actual far below reserved and issued a large NEGATIVE correction: 4093 of
// 4096 tokens refunded after streaming the maximum the ceiling permits.
//
// It runs the whole handleChatCompletions path rather than streamSSE directly,
// because the defect lives in the agreement between the kill site and
// finalizeUsage and only the real reservation makes `reserved` meaningful.
//
// Neuter check: replace chargeForKill's body with `return max(measured,
// dataChunks)` and this test fails with delta=-4093 while every other budget
// test still passes -- which is exactly how the defect shipped.
func TestStreamSSE_ByteGuardKillNeverRefunds(t *testing.T) {
	const keyID = "bbbb1111-0000-0000-0000-000000000004"

	// Headroom of 100 over the 4096 reservation => ceiling 4196 => the byte guard
	// trips at 4196*512 = 2,148,352 bytes.
	store := &fakeUsageStore{tokensUsed: 0, tokenLimit: defaultReservationTokens + 100}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// 3 x 900_000 = 2.7MB crosses the byte guard at dataChunks == 3, three orders
	// of magnitude below the token ceiling of 4196.
	upstream := bigChunkUpstream(3, 900_000)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`)
	p.handleChatCompletions(httptest.NewRecorder(), req)

	waitForCorrections(t, store, 1)
	store.mu.Lock()
	delta := store.corrections[0]
	store.mu.Unlock()

	if delta < 0 {
		t.Errorf("byte_guard kill refunded %d of %d reserved tokens after streaming the "+
			"maximum its ceiling permits (finalizeUsage saw actual=%d). A stream killed "+
			"for exceeding its budget must never move quota in the caller's favour -- "+
			"this is the C3 abort-refund class through the budget kill path",
			-delta, defaultReservationTokens, delta+int64(defaultReservationTokens))
	}
}

// The byte-guard kill's other half: finalizeUsage charges the same figure to the
// per-key token-RATE bucket, so the defect defeated both new spend controls at
// once. A maximum-spend request must leave the bucket materially charged.
func TestStreamSSE_ByteGuardKillChargesTheTokenRateBucket(t *testing.T) {
	const keyID = "bbbb1111-0000-0000-0000-000000000005"

	store := &fakeUsageStore{tokensUsed: 0, tokenLimit: defaultReservationTokens + 100}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := bigChunkUpstream(3, 900_000)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`)
	p.handleChatCompletions(httptest.NewRecorder(), req)
	waitForCorrections(t, store, 1)

	// Read the balance rather than asking available(), which is a >= 1 boolean
	// against a burst of 120000 and so cannot see the difference between a charge
	// of 3 and a charge of 4096. Measuring the number is the whole point here.
	p.keyTokens.mu.Lock()
	b, charged := p.keyTokens.buckets[keyID]
	var spent float64
	if charged {
		spent = keyTokenBurst - b.tokens
	}
	p.keyTokens.mu.Unlock()

	if !charged {
		t.Fatal("a 2.7MB maximum-spend request left no token-rate bucket at all")
	}
	if spent < float64(defaultReservationTokens) {
		t.Errorf("the token-rate bucket was charged %.0f for a request that streamed the "+
			"maximum its ceiling permits, want at least the reservation %d. Charging the "+
			"chunk count (3) leaves the token-volume limiter unable to bound a byte-guard "+
			"kill, so both of this branch's new spend controls fail on the same defect",
			spent, defaultReservationTokens)
	}
}

// chargeForKill's invariant, unit-level: no input combination may produce a
// figure below the reservation, because that is what makes finalizeUsage emit a
// negative correction.
func TestChargeForKill(t *testing.T) {
	const reserved = 4096
	tests := []struct {
		name       string
		measured   int
		dataChunks int
		want       int
	}{
		// The byte_guard shape: a handful of huge chunks, no usage figure. The
		// defect returned 3 here.
		{"byte guard, no usage figure", 0, 3, reserved},
		// The token_ceiling shape: the count is above the ceiling, which is itself
		// at or above the reservation.
		{"token ceiling, no usage figure", 0, 5000, 5000},
		// A real usage figure already in hand is never revised DOWN, in either
		// direction relative to the reservation.
		{"real usage above the reservation wins", 9000, 5000, 9000},
		{"real usage below the reservation is floored", 200, 3, reserved},
		{"nothing measured at all still charges the reservation", 0, 0, reserved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chargeForKill(tt.measured, tt.dataChunks, reserved); got != tt.want {
				t.Errorf("chargeForKill(%d, %d, %d) = %d, want %d",
					tt.measured, tt.dataChunks, reserved, got, tt.want)
			}
			if got := chargeForKill(tt.measured, tt.dataChunks, reserved); got < reserved {
				t.Errorf("charge %d is below the reservation %d -- finalizeUsage will "+
					"issue a refund for a killed stream", got, reserved)
			}
		})
	}
}

// A stream that stays under its ceiling must be completely untouched -- this is
// the regression guard that the budget check does not interfere with real traffic.
func TestStreamSSE_UnderBudgetStreamIsUnaffected(t *testing.T) {
	const keyID = "bbbb1111-0000-0000-0000-000000000003"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	streamSSEAndFinalize(p, rec, strings.NewReader(sseUsageBody(42)), keyID, defaultReservationTokens, 1, absoluteMaxRequestTokens)

	body := rec.Body.String()
	if strings.Contains(body, "budget_exceeded") {
		t.Error("an under-budget stream was killed -- the ceiling is firing on legitimate traffic")
	}
	if !strings.Contains(body, `"total_tokens":42`) {
		t.Errorf("the real usage chunk did not survive relay:\n%s", body)
	}
}

// Cost authorization beyond the single `model` field. Every entry here was
// forwarded upstream unexamined before the audit.
func TestCostSurface_RefusesBillableSideChannels(t *testing.T) {
	p := newProxy("k", "http://unused.invalid", "http://unused.invalid", "k",
		log.New(io.Discard, "", 0), parseAllowedModels("good/model"))

	const zdr = `"provider":{"zdr":true,"data_collection":"deny"}`
	tests := []struct {
		name string
		body string
		want string
	}{
		{"fallback models array", `{"model":"good/model","models":["expensive/model"],` + zdr + `}`, "cost_surface_not_allowed"},
		{"plugins (billed per use, invisible to token quota)", `{"model":"good/model","plugins":[{"id":"web"}],` + zdr + `}`, "cost_surface_not_allowed"},
		{"transforms", `{"model":"good/model","transforms":["middle-out"],` + zdr + `}`, "cost_surface_not_allowed"},
		{"provider.only (cost steering)", `{"model":"good/model","provider":{"zdr":true,"data_collection":"deny","only":["expensive"]}}`, "cost_surface_not_allowed"},
		{"provider.order (cost steering)", `{"model":"good/model","provider":{"zdr":true,"data_collection":"deny","order":["expensive"]}}`, "cost_surface_not_allowed"},
		{"provider.sort (cost steering)", `{"model":"good/model","provider":{"zdr":true,"data_collection":"deny","sort":"price"}}`, "cost_surface_not_allowed"},
		{"missing model (was a fail-OPEN)", `{` + zdr + `}`, "model_required"},
		{"unlisted model", `{"model":"evil/model",` + zdr + `}`, "model_not_allowed"},
		{"unparseable body", `{not json`, "malformed_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, refused := p.costSurfaceRefusal([]byte(tt.body))
			if !refused {
				t.Fatalf("costSurfaceRefusal allowed %q -- this field reaches OpenRouter "+
					"and is billed. If you are seeing this after dropping the field from the "+
					"predicate, that is the bypass this test exists to catch", tt.name)
			}
			if got != tt.want {
				t.Errorf("refusal = %q, want %q", got, tt.want)
			}
		})
	}
}

// D4 REGRESSION GUARD. provider.ignore is how DeepInfra is excluded from routing
// (commit 0c5bb29); the shipped daemon sends it on every request. Refusing it
// would break all real traffic, so the cost gate must let it through -- a
// deny-list narrows where traffic may go, which is the safe direction.
func TestCostSurface_AllowsProviderIgnore(t *testing.T) {
	p := newProxy("k", "http://unused.invalid", "http://unused.invalid", "k",
		log.New(io.Discard, "", 0), parseAllowedModels("good/model"))

	body := `{"model":"good/model","provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true,"ignore":["DeepInfra"]}}`
	if refusal, refused := p.costSurfaceRefusal([]byte(body)); refused {
		t.Fatalf("costSurfaceRefusal refused the shipped daemon's own D4 routing body with %q -- "+
			"this would 403 every real request", refusal)
	}
}

// An oversized declared max_tokens must be REFUSED, not silently clamped.
// Clamping the reservation while forwarding the caller's larger number was the
// mismatch that let one request overshoot its own admission.
func TestHandleChatCompletions_OversizedMaxTokensRefused(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseUsageBody(1))
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(fmt.Sprintf(
		`{"model":"x","max_tokens":%d,"stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`,
		maxReservationTokens+1))
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for an oversized max_tokens; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max_tokens_too_large") {
		t.Errorf("body = %s, want max_tokens_too_large", rec.Body.String())
	}
	if reached {
		t.Error("OpenRouter was contacted despite the refusal -- the check must run BEFORE the forward")
	}
	if store.openPendingCount() != 0 {
		t.Error("a refused request opened an outbox row -- it reserved nothing and owes no correction")
	}
}

// A max_tokens at or under the cap must still work normally.
func TestHandleChatCompletions_AcceptableMaxTokensStillForwards(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000002"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := newFakeUpstream(50)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	req := newAuthorizedRequest(
		`{"model":"x","max_tokens":100,"stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a legitimate max_tokens; body=%s", rec.Code, rec.Body.String())
	}
}

// Token-volume limiting: the second layer the request-rate limiter cannot see.
func TestTokenRateLimit_DebtBlocksTheNextRequest(t *testing.T) {
	l := newRateLimiter(keyTokenRatePerSecond, keyTokenBurst)
	base := time.Now()
	l.now = func() time.Time { return base }

	if !l.available("k") {
		t.Fatal("a fresh key was refused before spending anything")
	}
	// One outsized completion drives the bucket into debt.
	l.charge("k", keyTokenBurst*2)
	if l.available("k") {
		t.Fatal("a key in token debt was admitted -- without post-hoc charging, token " +
			"volume is unbounded no matter what the request rate is")
	}

	// Debt must be repaid by elapsed time, not held forever.
	l.now = func() time.Time { return base.Add(10 * time.Minute) }
	if !l.available("k") {
		t.Error("a key was still refused after ample refill time -- the debt floor is not bounding the penalty")
	}
}

func TestTokenRateLimit_DebtIsFlooredAtOneBurst(t *testing.T) {
	l := newRateLimiter(keyTokenRatePerSecond, keyTokenBurst)
	base := time.Now()
	l.now = func() time.Time { return base }

	l.charge("k", keyTokenBurst*1000) // one pathological request
	l.mu.Lock()
	b, charged := l.buckets["k"]
	l.mu.Unlock()
	if !charged {
		t.Fatal("charge left no bucket at all -- nothing was spent, so token volume is unmetered")
	}
	got := b.tokens
	if got < -keyTokenBurst {
		t.Errorf("debt = %f, want no worse than -%f -- an unbounded debt would lock a "+
			"paying key out for an arbitrarily long time", got, keyTokenBurst)
	}
}

// Unknown routes were 404ing straight out of the mux without passing through
// admitPreAuth -- an unauthenticated, entirely unthrottled endpoint.
func TestUnknownRoute_IsRateLimited(t *testing.T) {
	p := newTestProxy("http://unused.invalid", "http://unused.invalid")
	handler := p.rateLimitedNotFound()

	got404, got429 := 0, 0
	for i := 0; i < int(preAuthBurstGlobal)+50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/definitely-not-a-route", nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		switch rec.Code {
		case http.StatusNotFound:
			got404++
		case http.StatusTooManyRequests:
			got429++
		}
	}
	if got429 == 0 {
		t.Errorf("404 path served %d requests with zero throttling -- if you are seeing "+
			"this after unregistering the catch-all, that is the unbounded anonymous "+
			"endpoint this test exists to catch", got404)
	}
}

// ---------------------------------------------------------------------------
// D1/D2 regression guards. Both defects below shipped with a fully green suite,
// because the budget tests used a 47-byte toy chunk and no test exercised
// stream:false at all. These use realistic inputs.
// ---------------------------------------------------------------------------

// THE D1 GUARD. A completion of ordinary length, with REAL SSE envelopes, must
// stream to completion untouched.
//
// Against the estimator this replaces (max(chunks, bytes/4)) a 4096-token answer
// scored 4096*294/4 = 301,056 "tokens" against a 65,536 ceiling and was cut at
// roughly chunk 900 -- every substantial answer truncated in production.
func TestStreamSSE_RealisticStreamNotKilledUnderCeiling(t *testing.T) {
	const keyID = "dddd1111-0000-0000-0000-000000000001"
	const chunks = 4096 // an ordinary long answer, well under absoluteMaxRequestTokens

	store := &fakeUsageStore{tokenLimit: 10_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	streamSSEAndFinalize(p, rec, strings.NewReader(sseRealisticChunks(chunks)), keyID, defaultReservationTokens, 1, absoluteMaxRequestTokens)

	body := rec.Body.String()
	if strings.Contains(body, "budget_exceeded") {
		t.Fatalf("a %d-token completion with realistic SSE envelopes was KILLED under a "+
			"%d-token ceiling. If you are seeing this after reintroducing a bytes/4 term "+
			"into the budget check, that is the 73x envelope-vs-content error this test "+
			"exists to catch -- it truncates real traffic, it does not protect it",
			chunks, absoluteMaxRequestTokens)
	}
	if got := strings.Count(body, `"delta"`); got != chunks {
		t.Errorf("relayed %d chunks, want all %d -- the stream was altered", got, chunks)
	}
}

// The byte guard still has to fire on the shape chunk-counting cannot see: a
// provider batching an entire answer into a few enormous chunks.
func TestStreamSSE_ByteGuardCatchesBatchedGiantChunks(t *testing.T) {
	const keyID = "dddd1111-0000-0000-0000-000000000002"
	const ceiling = 100

	store := &fakeUsageStore{tokenLimit: 10_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// Three chunks, each far past the whole byte allowance -- only 3 by chunk
	// count, so the token bound alone would let this through.
	giant := strings.Repeat("x", ceiling*maxBytesPerChunkGuard)
	var b strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `data: {"choices":[{"delta":{"content":"%s"}}]}`+"\n\n", giant)
	}

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	rec := httptest.NewRecorder()
	streamSSEAndFinalize(p, rec, strings.NewReader(b.String()), keyID, defaultReservationTokens, 1, ceiling)

	if !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Error("a stream far past its byte allowance was not killed -- with only 3 chunks " +
			"the token bound cannot see it, which is the whole reason the byte guard exists")
	}
}

// THE D2 GUARD. stream:false took the buffered branch, where the ceiling was
// never applied -- bounded only by maxNonStreamResponseBytes, a 64x escape.
func TestHandleChatCompletions_NonStreamedRequestRefused(t *testing.T) {
	const keyID = "dddd1111-0000-0000-0000-000000000003"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"total_tokens":9}}`)
	}))
	defer upstream.Close()

	for _, tt := range []struct {
		name string
		body string
	}{
		{"stream explicitly false", `{"model":"x","stream":false,"provider":{"zdr":true,"data_collection":"deny"}}`},
		// Omitted is the dangerous one: OpenAI-compatible APIs default stream to
		// false, so treating "absent" as streaming would reopen the bypass.
		{"stream omitted entirely", `{"model":"x","provider":{"zdr":true,"data_collection":"deny"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			p := newTestProxy(supabase.URL, upstream.URL)
			rec := httptest.NewRecorder()
			p.handleChatCompletions(rec, newAuthorizedRequest(tt.body))

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "stream_required") {
				t.Errorf("body = %s, want stream_required", rec.Body.String())
			}
			if reached {
				t.Error("OpenRouter was contacted for a non-streamed request -- this is the " +
					"path where the budget ceiling does not run, so it must never be forwarded")
			}
			if store.openPendingCount() != 0 {
				t.Error("a refused request opened an outbox row")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-1: JSON parser differential. Go's encoding/json matches object keys to
// struct tags IGNORING CASE; OpenRouter matches exactly. Wherever the proxy
// therefore sees a SAFE value under a key OpenRouter will not read, the gate is
// bypassed and OpenRouter falls back to its own (unsafe) default.
// ---------------------------------------------------------------------------

// Demonstrates the underlying language behaviour these tests defend against, so
// the reason for the map-based lookup is legible without a doc lookup.
func TestGoJSONMatchesKeysCaseInsensitively(t *testing.T) {
	var peek struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal([]byte(`{"STREAM":true}`), &peek); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !peek.Stream {
		t.Skip("this Go version matches keys case-sensitively; F-1 does not apply here")
	}
	t.Log("confirmed: struct-tag decoding accepts \"STREAM\" for a `json:\"stream\"` field -- " +
		"any gate built on struct tags reads a key OpenRouter will not")
}

func TestZDRRoutingEnforced_RejectsCaseVariantKeys(t *testing.T) {
	// Each body would pass the proxy's gate while carrying NO routing object that
	// OpenRouter can read -- i.e. it forwards a request believing ZDR is set when
	// the model provider will never see the flags.
	for _, tt := range []struct {
		name string
		body string
	}{
		{"uppercased provider key", `{"model":"x","stream":true,"PROVIDER":{"zdr":true,"data_collection":"deny"}}`},
		{"uppercased flag keys", `{"model":"x","stream":true,"provider":{"ZDR":true,"DATA_COLLECTION":"deny"}}`},
		{"mixed case flag keys", `{"model":"x","stream":true,"provider":{"Zdr":true,"Data_Collection":"deny"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if zdrRoutingEnforced([]byte(tt.body)) {
				t.Error("zdrRoutingEnforced ACCEPTED a body whose routing keys OpenRouter will not " +
					"read. The proxy would forward this believing ZDR is enforced, while the " +
					"provider receives no routing object at all -- F1's entire claim (\"the proxy, " +
					"not the client, is the authority that the flags are correct on the wire\") is " +
					"false for this request. Fix: exact top-level key lookup, not struct-tag decoding")
			}
		})
	}
}

func TestStreamRequested_RejectsCaseVariantKeys(t *testing.T) {
	for _, body := range []string{
		`{"model":"x","STREAM":true,"provider":{"zdr":true,"data_collection":"deny"}}`,
		`{"model":"x","Stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`,
	} {
		if streamRequested([]byte(body)) {
			t.Errorf("streamRequested ACCEPTED %s -- OpenRouter reads no `stream` key here, "+
				"defaults it to false, and returns a buffered body, so the mid-stream budget "+
				"ceiling never runs. This reopens the 64x spend escape", body)
		}
	}
}

func TestPeekMaxTokens_IgnoresCaseVariantKeys(t *testing.T) {
	// A case-variant MAX_TOKENS must not be read as a reservation size: the proxy
	// would reserve the small declared value while OpenRouter, seeing no
	// max_tokens, applies its own far larger default.
	if got, ok := peekMaxTokens([]byte(`{"model":"x","MAX_TOKENS":50}`)); ok {
		t.Errorf("peekMaxTokens read a case-variant key as %d -- the reservation would be sized "+
			"from a field OpenRouter never sees", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage gaps closed after the QA pass. Each of these was a security control
// with zero direct tests.
// ---------------------------------------------------------------------------

// The per-source pre-auth bucket is keyed on X-Forwarded-For, which is
// caller-supplied and therefore SPOOFABLE. Rotating it yields a fresh bucket
// every time. The global bucket is the documented backstop -- this asserts it
// actually holds, which nothing did before.
func TestPreAuthLimit_ForgedXFFCannotEvadeTheGlobalBucket(t *testing.T) {
	p := newTestProxy("http://unused.invalid", "http://unused.invalid")
	handler := p.rateLimitedNotFound()

	throttled := 0
	attempts := int(preAuthBurstGlobal) + 200
	for i := 0; i < attempts; i++ {
		req := httptest.NewRequest(http.MethodGet, "/nope", nil)
		// A different forged source every single request: the per-source bucket
		// never sees the same key twice and so never throttles anything.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d, 10.0.0.1", i%256))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Fatalf("%d requests from %d forged X-Forwarded-For values were ALL admitted -- "+
			"per-source limiting is trivially evaded by spoofing, and the global bucket "+
			"that is documented as the backstop is not bounding anything",
			attempts, attempts)
	}
}

func TestClientSource_PrefersFirstXFFEntryThenPeer(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.7:54321"

	if got := clientSource(req); got != "192.0.2.7" {
		t.Errorf("with no XFF, clientSource = %q, want the peer IP without its port", got)
	}
	// Only the FIRST entry is the client; the rest are hops.
	req.Header.Set("X-Forwarded-For", " 198.51.100.5 , 10.0.0.1, 10.0.0.2")
	if got := clientSource(req); got != "198.51.100.5" {
		t.Errorf("clientSource = %q, want the first XFF entry, trimmed", got)
	}
}

// /health is the ONLY unauthenticated route and had zero tests.
func TestHealth(t *testing.T) {
	t.Run("returns ok and hides the build commit by default", func(t *testing.T) {
		rec := httptest.NewRecorder()
		makeHealthHandler("deadbeefcafe")(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("body is not valid JSON: %v (%s)", err, rec.Body.String())
		}
		if got.Status != "ok" {
			t.Errorf("status field = %q, want ok", got.Status)
		}
		if got.Commit != "" {
			t.Errorf("commit = %q on an anonymous request -- this fingerprints the exact "+
				"running build, and which known-fixed bugs are deployed, for any caller "+
				"on the internet", got.Commit)
		}
	})

	t.Run("discloses the commit only when explicitly opted in", func(t *testing.T) {
		t.Setenv("HEALTH_EXPOSE_COMMIT", "1")
		rec := httptest.NewRecorder()
		makeHealthHandler("deadbeefcafe")(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		var got healthResponse
		json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Commit != "deadbeefcafe" {
			t.Errorf("commit = %q with HEALTH_EXPOSE_COMMIT=1, want it reported -- the "+
				"deploy-verify step depends on this", got.Commit)
		}
	})

	t.Run("is rate limited", func(t *testing.T) {
		p := newTestProxy("http://unused.invalid", "http://unused.invalid")
		handler := p.rateLimitedHealth(makeHealthHandler("x"))

		throttled := 0
		for i := 0; i < int(preAuthBurstGlobal)+100; i++ {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			if rec.Code == http.StatusTooManyRequests {
				throttled++
			}
		}
		if throttled == 0 {
			t.Error("/health served an unbounded flood with zero throttling -- it is the " +
				"only unauthenticated route, so this is a free anonymous endpoint")
		}
	})
}

// The buffered response branch still runs for upstream ERROR bodies, which
// arrive as application/json even when the request asked to stream. Its
// account-metadata scrubber had no test at all.
func TestStripAccountMetadata(t *testing.T) {
	t.Run("removes account fields", func(t *testing.T) {
		got := stripAccountMetadata([]byte(`{"user_id":"org-SECRET","error":{"code":400}}`))
		if strings.Contains(string(got), "user_id") || strings.Contains(string(got), "org-SECRET") {
			t.Errorf("account metadata survived scrubbing: %s", got)
		}
		if !strings.Contains(string(got), `"error"`) {
			t.Errorf("the real error payload was lost: %s", got)
		}
	})
	t.Run("passes through a body it cannot parse", func(t *testing.T) {
		const raw = `not json at all`
		if got := string(stripAccountMetadata([]byte(raw))); got != raw {
			t.Errorf("stripAccountMetadata mangled an unparseable body: %q", got)
		}
	})
}

// End-to-end: an upstream error body must reach the client scrubbed, on the
// buffered branch, for a request that legitimately asked to stream.
func TestHandleChatCompletions_UpstreamErrorBodyIsScrubbed(t *testing.T) {
	const keyID = "eeee1111-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"user_id":"org-LEAKED-ACCOUNT","error":{"message":"bad model"}}`)
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(
		`{"model":"x","stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`))

	if strings.Contains(rec.Body.String(), "org-LEAKED-ACCOUNT") || strings.Contains(rec.Body.String(), "user_id") {
		t.Errorf("OpenRouter account metadata reached the client on the error path:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bad model") {
		t.Errorf("the upstream error message was lost:\n%s", rec.Body.String())
	}
}

// P2-1. Duplicate keys made the gate's verdict and the forwarded bytes disagree:
// every gate resolves last-wins, and the body is forwarded byte-for-byte
// carrying both. Reproduced before the fix -- cost authorization admitted a
// request whose forwarded body still named a banned model first.
//
// Neuter check: remove the hasDuplicateKeys call from handleChatCompletions and
// the first two subtests fail (200 instead of 400, upstream contacted).
func TestDuplicateJSONKeys_Refused(t *testing.T) {
	const keyID = "dddd1111-0000-0000-0000-000000000001"

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			// The confirmed bypass: banned model first, allowed model second.
			name: "duplicate model at the top level",
			body: `{"model":"expensive-model","model":"cheap-model",` +
				`"messages":[{"role":"user","content":"hi"}],"stream":true,` +
				`"provider":{"zdr":true,"data_collection":"deny"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// Nested: the ZDR flags live inside `provider`, so the walk must
			// descend. zdr:false first, zdr:true second reads as compliant to the
			// gate while the forwarded bytes lead with the weakened flag.
			name: "duplicate zdr inside the provider object",
			body: `{"model":"cheap-model","messages":[{"role":"user","content":"hi"}],` +
				`"stream":true,"provider":{"zdr":false,"zdr":true,"data_collection":"deny"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// The shipped daemon's real body shape. It marshals from structs, so it
			// cannot produce a duplicate key -- this must still be admitted, or the
			// check has broken all real traffic.
			name: "the shipped daemon's body shape is unaffected",
			body: `{"model":"cheap-model","messages":[{"role":"system","content":"sys"},` +
				`{"role":"user","content":"hi"}],"stream":true,` +
				`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":false,"ignore":["DeepInfra"]},` +
				`"stream_options":{"include_usage":true}}`,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeUsageStore{tokenLimit: 100000}
			supabase, _ := newFakeSupabase(store, keyID)
			defer supabase.Close()

			var contacted atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contacted.Store(true)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, sseUsageBody(10))
			}))
			defer upstream.Close()

			p := newProxy("k", upstream.URL, supabase.URL, "srk", log.New(io.Discard, "", 0),
				map[string]bool{"cheap-model": true})
			rec := httptest.NewRecorder()
			p.handleChatCompletions(rec, newAuthorizedRequest(tt.body))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusBadRequest {
				if contacted.Load() {
					t.Error("an ambiguous body was forwarded upstream -- the whole point is " +
						"that no gate, and no provider, ever judges a body whose meaning " +
						"depends on which parser reads it")
				}
				if !strings.Contains(rec.Body.String(), "duplicate_json_key") {
					t.Errorf("refusal body = %s, want the duplicate_json_key slug", rec.Body.String())
				}
			}
		})
	}
}

func TestHasDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"clean object", `{"model":"a","stream":true}`, false},
		{"top-level duplicate", `{"model":"a","model":"b"}`, true},
		{"nested duplicate", `{"provider":{"zdr":false,"zdr":true}}`, true},
		{"duplicate inside an array element", `{"messages":[{"role":"a","role":"b"}]}`, true},
		// Same key name at DIFFERENT levels is legitimate and must be allowed --
		// the sets are per-object, not global.
		{"same name in two different objects", `{"a":{"x":1},"b":{"x":2}}`, false},
		{"repeated key across array siblings", `{"m":[{"role":"user"},{"role":"user"}]}`, false},
		{"empty object", `{}`, false},
		// Malformed input is left to the gates, which already refuse it.
		{"malformed", `{"a":`, false},
		{"not an object", `"just a string"`, false},
		// Deep nesting is refused rather than recursed into (fail closed).
		{"excessive nesting", strings.Repeat("[", 200) + strings.Repeat("]", 200), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDuplicateKeys([]byte(tt.body)); got != tt.want {
				t.Errorf("hasDuplicateKeys(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// panickingResponseWriter panics on the write whose payload contains panicOn,
// modelling any runtime fault -- a nil dereference, a bounds bug, a future
// regression -- that unwinds out of the middle of a request AFTER the
// reservation already exists. It mirrors abortingResponseWriter above, which
// does the same for a client disconnect; the difference is that a disconnect is
// an error return the code handles and a panic is not.
type panickingResponseWriter struct {
	header  http.Header
	body    bytes.Buffer
	panicOn string
	code    int

	// panicOnWriteHeader faults at w.WriteHeader instead of at a body write.
	// That distinction is the whole point of the two subtests below: WriteHeader
	// runs in handleChatCompletions AFTER the reservation exists but BEFORE
	// streamSSE is entered, so it is a site that streamSSE's own defer never
	// covered and only the handler-level defer can.
	panicOnWriteHeader bool
}

func (w *panickingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *panickingResponseWriter) WriteHeader(code int) {
	w.code = code
	if w.panicOnWriteHeader {
		panic("synthetic fault at WriteHeader, before the stream begins")
	}
}

func (w *panickingResponseWriter) Write(b []byte) (int, error) {
	if w.panicOn != "" && strings.Contains(string(b), w.panicOn) {
		panic("synthetic fault mid-stream: " + w.panicOn)
	}
	return w.body.Write(b)
}

func (w *panickingResponseWriter) Flush() {}

// TestHandleChatCompletions_PanicStillDischargesReservation is the P1.1
// regression, and the unit-level half of the defect measured live in
// docs/ROBUSTNESS_BASELINE.md §3.
//
// A reservation is a DEBT: reserve_usage has already incremented tokens_used by
// the full reserved amount and opened a pending_corrections row, and something
// must later true that up. If a fault unwinds the request before that happens,
// the caller stays charged the whole reservation and the outbox row stays open
// until the sweep reports it as permanently over-charged -- the sweep never
// refunds, by design.
//
// The two subtests are deliberately NOT equivalent, and the difference is what
// makes this a real regression test rather than a restatement of the fix:
//
//   - "before the stream begins" faults at w.WriteHeader, which runs in
//     handleChatCompletions after the reservation exists and before streamSSE is
//     entered. NOTHING covered this before P1.1. This is the subtest that fails
//     when the fix is neutered.
//   - "mid-stream" faults inside streamSSE's relay write. streamSSE always had
//     its own deferred finalizeUsage, so this case was ALREADY covered before
//     P1.1. It is kept to pin that coverage in place now that the billing moved
//     out of streamSSE and into the handler -- it guards against the refactor
//     having lost something, and it is honestly not evidence that the refactor
//     added something.
//
// Neuter check for the first subtest: replace handleChatCompletions'
// `defer p.finalizeReservation(&outcome)` with a plain call at the end of the
// function. "before the stream begins" then fails with 0 corrections and 1 open
// pending row; "mid-stream" keeps passing, as does every other test in this
// file -- which is precisely the shape that let the gap survive unnoticed.
func TestHandleChatCompletions_PanicStillDischargesReservation(t *testing.T) {
	tests := []struct {
		name string
		// writer decides WHERE the fault lands.
		writer func() *panickingResponseWriter
		// wantRefund is whether the discharge should hand quota back. A fault
		// before any output was relayed is a full refund; a fault after real
		// upstream-billed output must never move quota in the caller's favour
		// (the chargeForKill invariant).
		wantRefund bool
	}{
		{
			name:       "before the stream begins (uncovered before P1.1)",
			writer:     func() *panickingResponseWriter { return &panickingResponseWriter{panicOnWriteHeader: true} },
			wantRefund: true,
		},
		{
			name:       "mid-stream (already covered by streamSSE's own defer)",
			writer:     func() *panickingResponseWriter { return &panickingResponseWriter{panicOn: "BOOM"} },
			wantRefund: false,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keyID := fmt.Sprintf("cccc2222-0000-0000-0000-00000000000%d", i+1)

			store := &fakeUsageStore{tokenLimit: 100000}
			supabase, _ := newFakeSupabase(store, keyID)
			defer supabase.Close()

			// Delivers real content first (so sawData is true on the mid-stream
			// case), then the chunk that triggers the fault, then a usage chunk
			// that must never be reached.
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"BOOM\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"usage\":{\"total_tokens\":99}}\n\n")
			}))
			defer upstream.Close()

			p := newTestProxy(supabase.URL, upstream.URL)
			req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`)

			func() {
				// The panic is expected to propagate out of the handler:
				// CONTAINING it is P1.3's job (the recovery middleware), not
				// P1.1's. What P1.1 guarantees is that the reservation is
				// discharged on the way out regardless of who catches it.
				defer func() {
					if r := recover(); r == nil {
						t.Fatal("expected a synthetic fault to panic; the test's own premise is broken")
					}
				}()
				p.handleChatCompletions(tc.writer(), req)
			}()

			waitForCorrections(t, store, 1)

			if got := store.openPendingCount(); got != 0 {
				t.Errorf("a panic left %d open pending_corrections row(s); the reservation was "+
					"stranded and the sweep would later report this key as permanently "+
					"over-charged. Every reservation must be discharged exactly once, "+
					"including during panic unwinding", got)
			}

			store.mu.Lock()
			delta := store.corrections[0]
			store.mu.Unlock()

			switch {
			case tc.wantRefund && delta != -defaultReservationTokens:
				t.Errorf("fault before any output was relayed corrected by %d, want a full "+
					"refund of %d: upstream generated nothing, so none of the reservation "+
					"was spent", delta, -defaultReservationTokens)
			case !tc.wantRefund && delta < 0:
				t.Errorf("fault after real output was relayed refunded %d tokens; upstream "+
					"billed for that output, so a fault must not move quota in the caller's "+
					"favour (same invariant as chargeForKill)", -delta)
			}
		})
	}
}

// TestFinalizeReservation_IsIdempotent pins the once-ness directly. Nested
// defers and the P1.3 recovery middleware can both reach the finalizer for the
// same request, and a reservation billed twice applies its correction twice --
// which on the common under-run case is a DOUBLE REFUND of quota the account
// really spent.
//
// Neuter check: delete the `o.finalized` guard in finalizeReservation and this
// test fails with 2 corrections.
func TestFinalizeReservation_IsIdempotent(t *testing.T) {
	const keyID = "cccc2222-0000-0000-0000-000000000002"

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	outcome := &reservationOutcome{keyID: keyID, reserved: defaultReservationTokens, pendingID: 1, actual: 50, producedOutput: true}

	p.finalizeReservation(outcome)
	p.finalizeReservation(outcome)
	p.finalizeReservation(outcome)

	waitForCorrections(t, store, 1)
	// Give any extra correction time to land before asserting there wasn't one.
	time.Sleep(150 * time.Millisecond)

	if got := store.correctionCount(); got != 1 {
		t.Errorf("finalizeReservation billed %d times for one reservation; it must bill exactly "+
			"once however many times it is called, or a request whose actual usage undershot "+
			"its reservation gets its refund applied repeatedly", got)
	}
}
