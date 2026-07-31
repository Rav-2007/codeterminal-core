package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowSecondReadReader yields everything up to the split point immediately, then
// blocks for delay before yielding the rest. It exists so a TTFB assertion has
// something to be wrong about: with an instantaneous stream, "time to first
// chunk" and "time to last chunk" are the same number and any TTFB bug passes.
type slowSecondReadReader struct {
	head, tail string
	delay      time.Duration
	sentHead   bool
	sentTail   bool
}

func (r *slowSecondReadReader) Read(p []byte) (int, error) {
	switch {
	case !r.sentHead:
		r.sentHead = true
		return copy(p, r.head), nil
	case !r.sentTail:
		time.Sleep(r.delay)
		r.sentTail = true
		return copy(p, r.tail), nil
	default:
		return 0, io.EOF
	}
}

// TestStageTimerAbsentStageIsNotZero is the load-bearing test for this
// instrument's central design choice.
//
// A 401 never reaches reserveQuota. If attrs() emitted every known stage with a
// zero default, that request's line would say `reserve_ms=0` -- which reads as
// "the reservation was instant" rather than "there was no reservation", and
// would make the instrument actively misleading on exactly the failure paths it
// exists to explain.
//
// Neuter-check: make attrs() emit a fixed set of stages instead of only the
// recorded ones and this goes red on the second assertion.
func TestStageTimerAbsentStageIsNotZero(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000001"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	var logs syncBuffer
	logger := log.New(&logs, "", 0)
	p := newTestProxy(supabase.URL, "http://unused.invalid")
	h := wrapMiddleware(logger, p.metrics, http.HandlerFunc(p.handleChatCompletions))

	// No Authorization header at all: refused before authorize() even looks
	// anything up, and long before any reservation exists.
	req := httptest.NewRequest(http.MethodPost, chatCompletionsPath,
		strings.NewReader(`{"model":"good/model","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 to exercise the short-circuit path, got %d", rec.Code)
	}

	line := logs.String()
	if !strings.Contains(line, "auth_ms=") {
		t.Errorf("auth_ms missing: authorize() ran and must be timed.\n%s", line)
	}
	if strings.Contains(line, "reserve_ms=") {
		t.Errorf("reserve_ms present on a request that never reserved -- an absent "+
			"stage must be ABSENT, not zero.\n%s", line)
	}
	if strings.Contains(line, "ttfb_ms=") {
		t.Errorf("ttfb_ms present on a request that never streamed.\n%s", line)
	}
}

// TestStageTimingsReachTheAccessLog proves the wiring end to end on the happy
// path: every stage that ran is on the one line, under the one req_id.
func TestStageTimingsReachTheAccessLog(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000002"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"usage\":{\"total_tokens\":7}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	var logs syncBuffer
	logger := log.New(&logs, "", 0)
	p := newProxy("sk-test", upstream.URL, supabase.URL, "sb_secret_test",
		logger, nil)
	h := wrapMiddleware(logger, p.metrics, http.HandlerFunc(p.handleChatCompletions))

	// The provider block is required: the F1 gate fails closed on any request
	// without the zero-data-retention routing flags.
	req := newAuthorizedRequest(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,` +
		`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	line := logs.String()
	for _, stage := range []string{stageAuth, stageReserve, stageUpstream, stageTTFB, "latency_ms"} {
		if !strings.Contains(line, stage+"=") {
			t.Errorf("%s missing from the access log line:\n%s", stage, line)
		}
	}
}

// TestStageTimerTTFBIsFirstChunkNotLast pins TTFB to its name.
//
// The stream below delivers its first data chunk immediately and the rest only
// after a delay. TTFB must reflect the first chunk. Neuter-check: delete the
// `if !firstChunkSeen` guard in streamSSE so every chunk re-records, and this
// goes red -- TTFB becomes the last chunk's time, i.e. a stream duration wearing
// a TTFB label.
func TestStageTimerTTFBIsFirstChunkNotLast(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000003"
	const delay = 250 * time.Millisecond
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	body := &slowSecondReadReader{
		head:  "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n",
		tail:  "data: {\"usage\":{\"total_tokens\":7}}\n\ndata: [DONE]\n\n",
		delay: delay,
	}

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	st := &stageTimer{}
	outcome := &reservationOutcome{keyID: keyID, reserved: defaultReservationTokens, pendingID: 1}

	start := time.Now()
	p.streamSSE(httptest.NewRecorder(), body, outcome, absoluteMaxRequestTokens, st)
	total := time.Since(start)

	if total < delay {
		t.Fatalf("the stream did not actually block (%s < %s); the test cannot "+
			"distinguish first from last chunk", total, delay)
	}

	var ttfb time.Duration
	var found bool
	for _, s := range st.stages {
		if s.name == stageTTFB {
			ttfb, found = s.dur, true
		}
	}
	if !found {
		t.Fatal("no ttfb_ms recorded for a stream that delivered data chunks")
	}
	// Generous bound: the point is first-vs-last (0 ms vs 250 ms), not precision.
	if ttfb > delay/2 {
		t.Errorf("ttfb_ms = %s, which is the LAST chunk's time, not the first "+
			"(stream total %s, delay %s)", ttfb, total, delay)
	}
}

// TestStageTimerTTFBRecordedExactlyOnce guards the same property from the other
// side: a multi-chunk stream must produce one ttfb_ms, not one per chunk.
func TestStageTimerTTFBRecordedExactlyOnce(t *testing.T) {
	const keyID = "cccc1111-0000-0000-0000-000000000004"
	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	p := newTestProxy(supabase.URL, "http://unused.invalid")
	st := &stageTimer{}
	outcome := &reservationOutcome{keyID: keyID, reserved: defaultReservationTokens, pendingID: 1}
	p.streamSSE(httptest.NewRecorder(), strings.NewReader(sseChunks(20)), outcome,
		absoluteMaxRequestTokens, st)

	n := 0
	for _, s := range st.stages {
		if s.name == stageTTFB {
			n++
		}
	}
	if n != 1 {
		t.Errorf("ttfb_ms recorded %d times over a 20-chunk stream; want exactly 1", n)
	}
}

// TestStageTimerNilIsSafe covers the documented nil case: callers outside a
// request (the background sweep, a test driving a helper directly) have no
// timer, and must not need a guard at every call site.
func TestStageTimerNilIsSafe(t *testing.T) {
	var st *stageTimer
	st.record(stageAuth, time.Second) // must not panic
	if got := st.attrs(); got != nil {
		t.Errorf("nil timer attrs() = %v, want nil", got)
	}
	if got := stageTimerFrom(nil); got != nil {
		t.Errorf("stageTimerFrom(nil) = %v, want nil", got)
	}
	if got := stageTimerFrom(context.Background()); got != nil {
		t.Errorf("stageTimerFrom(ctx without timer) = %v, want nil", got)
	}
	// timeStage on a timer-less context must still run fn.
	ran := false
	timeStage(context.Background(), stageAuth, func() { ran = true })
	if !ran {
		t.Error("timeStage did not run fn when the context carried no timer")
	}
}

// TestStageTimerConcurrentRecord exists for `go test -race`, which CI runs. The
// streaming path and the deferred finalizer both touch one request's timer.
func TestStageTimerConcurrentRecord(t *testing.T) {
	st := &stageTimer{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.record(stageUpstream, time.Millisecond)
			_ = st.attrs()
		}()
	}
	wg.Wait()
	if len(st.stages) != 50 {
		t.Errorf("recorded %d stages, want 50", len(st.stages))
	}
}

// TestStageTimerPreservesOrder pins the emitted order to the recorded order, so
// two lines for the same path line up column-for-column when read side by side.
func TestStageTimerPreservesOrder(t *testing.T) {
	st := &stageTimer{}
	st.record(stageAuth, 3*time.Millisecond)
	st.record(stageReserve, 5*time.Millisecond)
	st.record(stageUpstream, 7*time.Millisecond)

	got := st.attrs()
	want := []any{stageAuth, int64(3), stageReserve, int64(5), stageUpstream, int64(7)}
	if len(got) != len(want) {
		t.Fatalf("attrs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attrs()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
