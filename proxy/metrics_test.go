package main

import (
	"bytes"
	"encoding/json"
	"expvar"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAdminToken = "an-admin-token-of-sufficient-length"

// TestRefusalsAreCountedByGate is the counters' reason for existing: the log
// explains one request, the counters are how an operator notices a gate is firing
// four hundred times. Both must name the gate with the SAME string, or the two
// views cannot be joined.
func TestRefusalsAreCountedByGate(t *testing.T) {
	keyID := "key-id-for-gate-counts"
	store := &fakeUsageStore{tokenLimit: 1_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var logs bytes.Buffer
	p := newProxy("k", "http://unused.invalid", supabase.URL, "sr",
		log.New(&logs, "", 0), parseAllowedModels("good/model"))
	h := wrapMiddleware(log.New(&logs, "", 0), p.metrics, http.HandlerFunc(p.handleChatCompletions))

	cases := []struct {
		name     string
		auth     bool
		body     string
		wantGate string
	}{
		{"no bearer", false, `{}`, gateNoBearer},
		{"model not allowed", true,
			`{"model":"expensive/model","stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`,
			gateCostSurface},
		{"zdr flags missing", true, `{"model":"good/model","stream":true}`, gateZDRRequired},
		{"not streamed", true,
			`{"model":"good/model","provider":{"zdr":true,"data_collection":"deny"}}`,
			gateStreamRequired},
		{"max_tokens too large", true,
			`{"model":"good/model","stream":true,"max_tokens":9999999,"provider":{"zdr":true,"data_collection":"deny"}}`,
			gateMaxTokens},
		{"duplicate JSON key", true,
			`{"model":"good/model","model":"x","stream":true,"provider":{"zdr":true,"data_collection":"deny"}}`,
			gateDuplicateJSONKey},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := gateCount(p.metrics, tc.wantGate)

			req := httptest.NewRequest(http.MethodPost, chatCompletionsPath, strings.NewReader(tc.body))
			if tc.auth {
				req.Header.Set("Authorization", "Bearer mochi_test_key")
			}
			logs.Reset()
			h.ServeHTTP(httptest.NewRecorder(), req)

			if got := gateCount(p.metrics, tc.wantGate); got != before+1 {
				t.Errorf("refusals_by_gate[%s] = %d, want %d", tc.wantGate, got, before+1)
			}
			// The join: the same label must appear in the log line for this request.
			if !strings.Contains(logs.String(), "gate="+tc.wantGate) {
				t.Errorf("the log for this refusal does not carry gate=%s, so the counter "+
					"and the log cannot be joined.\nLog: %s", tc.wantGate, logs.String())
			}
		})
	}
}

// gateCount reads one gate's tally. expvar.Map stores *expvar.Int, so the
// assertion goes through the counter itself rather than through its rendering.
func gateCount(m *metricSet, gate string) int64 {
	v := m.refusalsByGate.Get(gate)
	if v == nil {
		return 0
	}
	iv, ok := v.(*expvar.Int)
	if !ok {
		return -1
	}
	return iv.Value()
}

// TestResponseClassesAreCounted covers the one counter that cannot be derived from
// the others: what the caller actually received.
func TestResponseClassesAreCounted(t *testing.T) {
	m := newMetrics()
	h := accessLog(log.New(io.Discard, "", 0), m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			t.Fatalf("bad test path %q", r.URL.Path)
		}
		w.WriteHeader(code)
	}))

	for _, path := range []string{"/200", "/204", "/401", "/429", "/500", "/502"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	if got := m.requests.Value(); got != 6 {
		t.Errorf("requests_total = %d, want 6", got)
	}
	if got := m.responses2xx.Value(); got != 2 {
		t.Errorf("responses_2xx = %d, want 2", got)
	}
	if got := m.responses4xx.Value(); got != 2 {
		t.Errorf("responses_4xx = %d, want 2", got)
	}
	if got := m.responses5xx.Value(); got != 2 {
		t.Errorf("responses_5xx = %d, want 2", got)
	}
}

// TestSweepLivenessCounters is the counter this phase exists for as much as the
// request ids are.
//
// The sweep logs only when it finds something -- deliberately, so its prefix stays
// attention-worthy -- which makes a sweep that died twenty minutes ago
// indistinguishable from a healthy one. These counters are the only way to tell.
func TestSweepLivenessCounters(t *testing.T) {
	t.Run("a clean sweep with nothing to do still records that it ran", func(t *testing.T) {
		store := &fakeUsageStore{tokenLimit: 1000}
		supabase, _ := newFakeSupabase(store, "k")
		defer supabase.Close()

		p := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(io.Discard, "", 0), nil)
		swept, failed := p.sweepPendingCorrections()

		if swept != 0 || failed {
			t.Errorf("swept=%d failed=%t, want 0/false", swept, failed)
		}
		if got := p.metrics.sweepRuns.Value(); got != 1 {
			t.Errorf("sweep_runs_total = %d, want 1", got)
		}
		if got := p.metrics.sweepLastRunUnix.Value(); got == 0 {
			t.Error("sweep_last_run_unix is 0 after a sweep: 'is reconciliation alive?' " +
				"is still unanswerable, which is the whole point of this counter")
		}
		if got := p.metrics.sweepLastErrUnix.Value(); got != 0 {
			t.Errorf("sweep_last_error_unix = %d after a CLEAN sweep, want 0", got)
		}
	})

	t.Run("a failing sweep is distinguishable from one that never ran", func(t *testing.T) {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer broken.Close()

		p := newProxy("k", "http://unused.invalid", broken.URL, "sr", log.New(io.Discard, "", 0), nil)
		if _, failed := p.sweepPendingCorrections(); !failed {
			t.Error("a 500 from the sweep RPC was not reported as a failure")
		}
		if run, err := p.metrics.sweepLastRunUnix.Value(), p.metrics.sweepLastErrUnix.Value(); run == 0 || err == 0 {
			t.Errorf("last_run=%d last_error=%d; a sweep that RAN and FAILED must set both, "+
				"otherwise it reads as a sweep that never happened", run, err)
		}
	})

	t.Run("a stranded reservation is counted, not only logged", func(t *testing.T) {
		store := &fakeUsageStore{tokenLimit: 1_000_000}
		supabase, _ := newFakeSupabase(store, "k")
		defer supabase.Close()

		// Open two reservations and never correct them, then age them past the
		// staleness window so the sweep claims both.
		store.reserve("key-a", 4096)
		store.reserve("key-b", 4096)
		store.backdatePending(time.Duration(pendingCorrectionStaleAfterMinutes+1) * time.Minute)

		var logs bytes.Buffer
		p := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(&logs, "", 0), nil)
		swept, failed := p.sweepPendingCorrections()

		if swept != 2 || failed {
			t.Fatalf("swept=%d failed=%t, want 2/false", swept, failed)
		}
		if got := p.metrics.reservationsStranded.Value(); got != 2 {
			t.Errorf("reservations_stranded_total = %d, want 2", got)
		}
		if !strings.Contains(logs.String(), "ABANDONED RESERVATION swept") {
			t.Errorf("the sweep counted rows without logging them; the log line is the "+
				"deliverable, the counter is the alarm.\nLog: %s", logs.String())
		}
	})
}

// TestReservationsOpenedAndFinalizedTrack pins the money-path invariant P1.1 made
// structural, in the form an operator can actually watch: the two counters must
// stay equal once no request is in flight. A gap means a reservation was leaked.
func TestReservationsOpenedAndFinalizedTrack(t *testing.T) {
	keyID := "key-id-for-reservation-counts"
	store := &fakeUsageStore{tokenLimit: 1_000_000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()
	upstream := newFakeUpstream(137)
	defer upstream.Close()

	p := newProxy("k", upstream.URL, supabase.URL, "sr", log.New(io.Discard, "", 0), nil)
	h := wrapMiddleware(log.New(io.Discard, "", 0), p.metrics, http.HandlerFunc(p.handleChatCompletions))

	for i := 0; i < 3; i++ {
		req := newAuthorizedRequest(`{"model":"x","messages":[],"stream":true,` +
			`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	opened := p.metrics.reservationsOpened.Value()
	finalized := p.metrics.reservationsFinalized.Value()
	if opened != 3 {
		t.Errorf("reservations_opened_total = %d, want 3", opened)
	}
	if finalized != opened {
		t.Errorf("opened=%d but finalized=%d: with nothing in flight a gap means a "+
			"reservation was leaked, which is exactly what P1.1 made impossible", opened, finalized)
	}
}

func TestAdminMetricsRoute(t *testing.T) {
	newServed := func(token string) (http.Handler, *proxy) {
		p := newProxy("k", "http://unused.invalid", "", "", log.New(io.Discard, "", 0), nil)
		return newHandler(p, log.New(io.Discard, "", 0), "commit", token), p
	}

	t.Run("no token configured: the route does not exist", func(t *testing.T) {
		h, _ := newServed("")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, adminMetricsPath, nil))

		// 404 from the throttled catch-all, not 401: an unconfigured operational
		// endpoint must not exist at all, and must not advertise that it could.
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (the route must not be registered without a token)", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "requests_total") {
			t.Fatalf("counters were served with no token configured: %s", rec.Body.String())
		}
	})

	t.Run("no credential: 401 and nothing disclosed", func(t *testing.T) {
		h, p := newServed(testAdminToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, adminMetricsPath, nil))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "requests_total") {
			t.Fatalf("counters leaked to an unauthenticated caller: %s", rec.Body.String())
		}
		if got := p.metrics.adminAuthFailures.Value(); got != 1 {
			t.Errorf("admin_auth_failures_total = %d, want 1", got)
		}
	})

	t.Run("wrong credential: 401", func(t *testing.T) {
		h, _ := newServed(testAdminToken)
		req := httptest.NewRequest(http.MethodGet, adminMetricsPath, nil)
		req.Header.Set("Authorization", "Bearer "+testAdminToken+"-not-quite")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("correct credential: the counters", func(t *testing.T) {
		h, _ := newServed(testAdminToken)

		req := httptest.NewRequest(http.MethodGet, adminMetricsPath, nil)
		req.Header.Set("Authorization", "Bearer "+testAdminToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
		var set map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
			t.Fatalf("counters are not JSON (%v): %s", err, rec.Body.String())
		}
		for _, want := range []string{
			"requests_total", "refusals_by_gate", "rate_limited_by_scope",
			"reservations_opened_total", "sweep_last_run_unix", "draining",
			"uptime_seconds", "goroutines", "heap_alloc_bytes",
		} {
			if _, ok := set[want]; !ok {
				t.Errorf("scrape is missing %s; body %s", want, rec.Body.String())
			}
		}
		// The live scrape of the real binary is what caught this: serving
		// expvar.Handler() rendered the DEFAULT registry, disclosing the binary's
		// full path in cmdline and burying the counters under all of memstats.
		for _, unwanted := range []string{"cmdline", "memstats"} {
			if _, found := set[unwanted]; found {
				t.Errorf("the scrape discloses %s, which comes from expvar's default "+
					"registry and has no business on a public listener", unwanted)
			}
		}
	})
}

// TestDrainingIsExposedLive pins the reason draining is an expvar.Func rather than
// a mirrored Int: it must be correct without anyone remembering to update it.
func TestDrainingIsExposedLive(t *testing.T) {
	var flag atomic.Bool
	m := newMetrics()
	m.bindDraining(&flag)

	if got := scrapeDraining(t, m); got != 0 {
		t.Errorf("draining = %d before the flag is set, want 0", got)
	}
	flag.Store(true)
	if got := scrapeDraining(t, m); got != 1 {
		t.Errorf("draining = %d after the flag is set, want 1 (the value is stale, so a "+
			"scrape cannot tell whether this replica is leaving)", got)
	}
}

// TestNewProxyBindsDraining is the wiring half: bindDraining is only useful if
// newProxy actually calls it, and a scrape of a proxy built the normal way must
// report the flag that proxy really uses.
func TestNewProxyBindsDraining(t *testing.T) {
	p := newProxy("k", "u", "", "", log.New(io.Discard, "", 0), nil)
	if got := scrapeDraining(t, p.metrics); got != 0 {
		t.Fatalf("draining = %d on a fresh proxy, want 0", got)
	}
	p.draining.Store(true)
	if got := scrapeDraining(t, p.metrics); got != 1 {
		t.Error("a proxy's own draining flag is not what its counters report; " +
			"newProxy is not binding it")
	}
}

// scrapeDraining reads draining the way an operator would: out of the rendered
// scrape, not off the struct field.
func scrapeDraining(t *testing.T, m *metricSet) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	m.writeTo(rec)

	var set map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("scrape is not JSON (%v): %s", err, rec.Body.String())
	}
	got, ok := set["draining"].(float64)
	if !ok {
		t.Fatalf("draining missing from the scrape: %s", rec.Body.String())
	}
	return int64(got)
}

func TestAdminTokenLengthIsEnforced(t *testing.T) {
	t.Run("unset means no route", func(t *testing.T) {
		t.Setenv(adminTokenEnv, "")
		if got := adminTokenFromEnv(log.New(io.Discard, "", 0)); got != "" {
			t.Errorf("token = %q, want empty", got)
		}
	})

	t.Run("a long enough token is accepted and trimmed", func(t *testing.T) {
		t.Setenv(adminTokenEnv, "  "+testAdminToken+"  ")
		if got := adminTokenFromEnv(log.New(io.Discard, "", 0)); got != testAdminToken {
			t.Errorf("token = %q, want %q", got, testAdminToken)
		}
	})

	// The short-token case is fatal by design (adminTokenFromEnv calls
	// logger.Fatalf), so it cannot be asserted in-process without killing the test
	// binary. The boundary is asserted on the predicate instead, and the fatal
	// behaviour is covered by the live drill in the phase notes.
	t.Run("the boundary is where it claims to be", func(t *testing.T) {
		if len(testAdminToken) < minAdminTokenLen {
			t.Fatalf("the test token is shorter than the minimum it is meant to satisfy")
		}
		if minAdminTokenLen < 16 {
			t.Errorf("minAdminTokenLen = %d, which is not a credential", minAdminTokenLen)
		}
	})
}
