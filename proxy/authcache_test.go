package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// revocableSupabase is a fake api_keys endpoint whose answer can be changed
// mid-test, and which counts how many times it was asked.
//
// The count is the whole instrument here: "the cache works" is not an assertion
// about latency, it is an assertion that the SECOND request did not produce a
// second lookup. Timing would be a proxy for that; counting is the thing itself.
type revocableSupabase struct {
	lookups atomic.Int64
	revoked atomic.Bool
	keyID   string
}

func (f *revocableSupabase) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		f.lookups.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if f.revoked.Load() {
			// Exactly the shape a revoked/inactive key produces: 200 with an
			// empty array, which the row-count gate turns into a refusal.
			w.Write([]byte(`[]`))
			return
		}
		json.NewEncoder(w).Encode([]map[string]string{{"id": f.keyID}})
	})
	mux.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"tokens_used": 1, "token_limit": 1_000_000, "pending_id": 1},
		})
	})
	mux.HandleFunc("/rest/v1/rpc/apply_correction", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	return httptest.NewServer(mux)
}

func authTestUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"usage\":{\"total_tokens\":7}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
}

const authTestBody = `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,` +
	`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`

// TestAuthCacheWarmKeySkipsTheLookup is the performance claim, asserted as a
// round-trip COUNT rather than as a duration.
func TestAuthCacheWarmKeySkipsTheLookup(t *testing.T) {
	fake := &revocableSupabase{keyID: "aaaa0000-0000-0000-0000-000000000001"}
	sb := fake.server()
	defer sb.Close()
	up := authTestUpstream()
	defer up.Close()

	p := newProxy("sk-test", up.URL, sb.URL, "sb_secret_test", log.New(io.Discard, "", 0), nil)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	if got := fake.lookups.Load(); got != 1 {
		t.Errorf("5 requests with one key produced %d Supabase lookups; want 1", got)
	}
	if h := p.metrics.authCacheHits.Value(); h != 4 {
		t.Errorf("auth_cache_hits_total = %d, want 4", h)
	}
	if m := p.metrics.authCacheMisses.Value(); m != 1 {
		t.Errorf("auth_cache_misses_total = %d, want 1", m)
	}
}

// TestAuthCacheRevocationTakesEffectAfterTTL is the security claim, and it is
// the test this whole feature has to earn.
//
// It asserts BOTH halves, because only asserting one is how a cache like this
// goes wrong: a revoked key must keep working inside the TTL (that is the cost
// we knowingly accepted, and if it did not happen the cache would not be doing
// anything), and it must STOP working once the TTL passes (that is the bound
// that made the cost acceptable).
//
// Neuter-check: delete the expiry comparison in authCache.get -- i.e. return the
// entry without checking c.clock().Before(e.expires) -- and the second half goes
// red, because the revoked key authorizes forever.
func TestAuthCacheRevocationTakesEffectAfterTTL(t *testing.T) {
	fake := &revocableSupabase{keyID: "aaaa0000-0000-0000-0000-000000000002"}
	sb := fake.server()
	defer sb.Close()
	up := authTestUpstream()
	defer up.Close()

	p := newProxy("sk-test", up.URL, sb.URL, "sb_secret_test", log.New(io.Discard, "", 0), nil)

	// Controlled clock, so the TTL is exercised without a 30-second sleep.
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	p.authCache.now = func() time.Time { return time.Unix(0, now.Load()) }

	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("warming request: want 200, got %d", rec.Code)
	}

	// The key is revoked in the database. Nothing tells the proxy.
	fake.revoked.Store(true)

	// Inside the TTL: still authorized. This is the accepted cost, stated.
	now.Add(int64(authCacheTTL / 2))
	rec = httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
	if rec.Code != http.StatusOK {
		t.Errorf("inside the TTL a revoked key should still authorize (that is the "+
			"accepted 30s window); got %d", rec.Code)
	}

	// Past the TTL: refused. This is the bound that made the cost acceptable.
	now.Add(int64(authCacheTTL))
	rec = httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("past the TTL a revoked key MUST be refused; got %d. The cache is "+
			"serving a revoked credential indefinitely.", rec.Code)
	}
}

// TestAuthCacheNeverCachesFailures pins the property that keeps this cache from
// being an attack amplifier: a bad key must cost a full lookup EVERY time, so
// the cache can never be primed with, or shortcut, a rejection.
//
// Neuter-check: move the put() above the row-count gate in authorize and this
// goes red.
func TestAuthCacheNeverCachesFailures(t *testing.T) {
	fake := &revocableSupabase{keyID: "aaaa0000-0000-0000-0000-000000000003"}
	fake.revoked.Store(true) // every lookup returns []
	sb := fake.server()
	defer sb.Close()

	p := newProxy("sk-test", "http://unused.invalid", sb.URL, "sb_secret_test",
		log.New(io.Discard, "", 0), nil)

	const attempts = 4
	for i := 0; i < attempts; i++ {
		rec := httptest.NewRecorder()
		p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, rec.Code)
		}
	}

	if got := fake.lookups.Load(); got != attempts {
		t.Errorf("%d rejected attempts produced %d lookups; want %d. A cached "+
			"failure would let an invalid key skip the database.", attempts, got, attempts)
	}
	if p.authCache.len() != 0 {
		t.Errorf("cache holds %d entries after only failed lookups; want 0", p.authCache.len())
	}
}

// TestAuthCacheFlushIsImmediate covers the incident lever: after a flush, the
// next request for a revoked key must go back to the database and be refused,
// without waiting out the TTL.
func TestAuthCacheFlushIsImmediate(t *testing.T) {
	fake := &revocableSupabase{keyID: "aaaa0000-0000-0000-0000-000000000004"}
	sb := fake.server()
	defer sb.Close()
	up := authTestUpstream()
	defer up.Close()

	p := newProxy("sk-test", up.URL, sb.URL, "sb_secret_test", log.New(io.Discard, "", 0), nil)

	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("warming request: want 200, got %d", rec.Code)
	}
	fake.revoked.Store(true)

	if n := p.authCache.flush(); n != 1 {
		t.Errorf("flush dropped %d entries, want 1", n)
	}

	rec = httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("after a flush a revoked key must be refused immediately; got %d", rec.Code)
	}
}

// TestAuthCacheFlushEndpointRequiresAuthAndPOST covers the admin surface itself.
// It is on a public listener, so "it only flushes a cache" is not a reason to
// leave it open -- an unauthenticated flush is a free way to force every request
// back onto a full Supabase round trip.
func TestAuthCacheFlushEndpointRequiresAuthAndPOST(t *testing.T) {
	const token = "an-admin-token-long-enough-to-pass"
	p := newTestProxy("http://unused.invalid", "http://unused.invalid")
	h := p.adminAuthCacheFlushHandler(token)

	cases := []struct {
		name   string
		method string
		bearer string
		want   int
	}{
		{"no token", http.MethodPost, "", http.StatusUnauthorized},
		{"wrong token", http.MethodPost, "not-the-token", http.StatusUnauthorized},
		{"right token but GET", http.MethodGet, token, http.StatusMethodNotAllowed},
		{"right token, POST", http.MethodPost, token, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, adminAuthCacheFlushPath, nil)
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestAuthCacheFlushRouteAbsentWithoutToken mirrors the counters route's rule:
// unconfigured means the route does not exist, not that it exists and refuses.
func TestAuthCacheFlushRouteAbsentWithoutToken(t *testing.T) {
	p := newTestProxy("http://unused.invalid", "http://unused.invalid")

	withToken := newMux(p, "commit", "an-admin-token-long-enough-to-pass")
	rec := httptest.NewRecorder()
	withToken.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, adminAuthCacheFlushPath, nil))
	if rec.Code == http.StatusNotFound {
		t.Error("route should exist when a token is configured")
	}

	without := newMux(p, "commit", "")
	rec = httptest.NewRecorder()
	without.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, adminAuthCacheFlushPath, nil))
	if rec.Code == http.StatusOK {
		t.Error("flush route answered 200 with no admin token configured")
	}
}

// TestAuthCacheEvictsAtCapacity proves the bound is a bound. Without it a caller
// cycling distinct valid keys grows the map without limit.
func TestAuthCacheEvictsAtCapacity(t *testing.T) {
	const max = 8
	c := newAuthCache(time.Minute, max)
	for i := 0; i < max*3; i++ {
		c.put(fmt.Sprintf("hash-%03d", i), fmt.Sprintf("key-%03d", i))
	}
	if got := c.len(); got != max {
		t.Errorf("cache holds %d entries, want the %d cap", got, max)
	}
	// The oldest must be the one gone, not an arbitrary victim.
	if _, ok := c.get("hash-000"); ok {
		t.Error("the least-recently-used entry survived eviction")
	}
	if _, ok := c.get(fmt.Sprintf("hash-%03d", max*3-1)); !ok {
		t.Error("the most-recently-used entry was evicted")
	}
}

// TestAuthCacheRecencyOrder pins LRU rather than insertion order: an entry that
// keeps being used must not be evicted just because it was added early.
func TestAuthCacheRecencyOrder(t *testing.T) {
	const max = 4
	c := newAuthCache(time.Minute, max)
	for i := 0; i < max; i++ {
		c.put(fmt.Sprintf("h%d", i), fmt.Sprintf("k%d", i))
	}
	// Touch the oldest so it becomes the newest.
	if _, ok := c.get("h0"); !ok {
		t.Fatal("h0 should still be present before eviction pressure")
	}
	c.put("h-new", "k-new") // forces one eviction

	if _, ok := c.get("h0"); !ok {
		t.Error("h0 was evicted despite being the most recently used")
	}
	if _, ok := c.get("h1"); ok {
		t.Error("h1 should have been evicted as the least recently used")
	}
}

// TestAuthCacheExpiredEntryIsRemovedNotJustHidden keeps the TTL from becoming a
// memory leak: an expired entry must leave the map when it is next looked at.
func TestAuthCacheExpiredEntryIsRemovedNotJustHidden(t *testing.T) {
	c := newAuthCache(time.Minute, 100)
	base := time.Now()
	cur := base
	c.now = func() time.Time { return cur }

	c.put("h", "k")
	if c.len() != 1 {
		t.Fatalf("len = %d, want 1", c.len())
	}
	cur = base.Add(2 * time.Minute)
	if _, ok := c.get("h"); ok {
		t.Error("expired entry was served")
	}
	if c.len() != 0 {
		t.Errorf("expired entry still occupies the map (len = %d)", c.len())
	}
}

// TestAuthCacheConcurrent exists for -race, which CI runs.
func TestAuthCacheConcurrent(t *testing.T) {
	c := newAuthCache(time.Minute, 64)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := fmt.Sprintf("h%d", i%10)
			c.put(h, fmt.Sprintf("k%d", i%10))
			c.get(h)
			c.len()
			if i%17 == 0 {
				c.flush()
			}
		}(i)
	}
	wg.Wait()
}

// TestAuthCacheNilIsSafe covers the zero value, so a proxy built without a cache
// (any test using a struct literal) does not panic on the hot path.
func TestAuthCacheNilIsSafe(t *testing.T) {
	var c *authCache
	c.put("h", "k")
	if _, ok := c.get("h"); ok {
		t.Error("nil cache returned a hit")
	}
	if n := c.flush(); n != 0 {
		t.Errorf("nil flush returned %d", n)
	}
	if n := c.len(); n != 0 {
		t.Errorf("nil len returned %d", n)
	}
}
