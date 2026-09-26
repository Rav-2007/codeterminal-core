package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A provider that goes silent must not hold a turn for requestTimeout. See
// streamStallTimeout. MEASURED live before this existed: a stream stopped
// mid-turn and the daemon waited the full five minutes with nothing on screen.

// shortStall shrinks the watchdog for one test.
func shortStall(t *testing.T, d time.Duration) {
	t.Helper()
	prev := streamStallTimeout
	streamStallTimeout = d
	t.Cleanup(func() { streamStallTimeout = prev })
}

// stallServer serves requests with the given behaviours, in order; the last
// repeats. Each behaviour writes what it likes and then returns or hangs.
func stallServer(t *testing.T, behaviours ...func(w http.ResponseWriter, r *http.Request)) (string, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(behaviours) {
			i = len(behaviours) - 1
		}
		w.Header().Set("Content-Type", "text/event-stream")
		behaviours[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func sseContent(w http.ResponseWriter, text string) {
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
	w.(http.Flusher).Flush()
}

func sseDone(w http.ResponseWriter) {
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

// hang holds the response open, silent, until the client gives up.
func hang(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func runStream(t *testing.T, base string) (string, error, time.Duration) {
	t.Helper()
	var got strings.Builder
	started := time.Now()
	_, err := streamWithRetry(context.Background(), base, "k", "m",
		[]chatMessage{{Role: "user", Content: "hi"}}, nil, providerRouting{},
		func(tok string) error { got.WriteString(tok); return nil },
		func(string) {}, func(string) {}, func(string) {}, nil)
	return got.String(), err, time.Since(started)
}

// Silent before any output: abandoned at the stall timeout and retried,
// even though the stall alone outlasts the ordinary retry budget.
func TestASilentProviderIsRetriedNotWaitedOn(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	base, requests := stallServer(t,
		hang,
		func(w http.ResponseWriter, _ *http.Request) { sseContent(w, "recovered"); sseDone(w) },
	)
	got, err, took := runStream(t, base)
	if err != nil {
		t.Fatalf("a stall followed by a healthy attempt failed: %v", err)
	}
	if got != "recovered" || requests.Load() != 2 {
		t.Errorf("got %q after %d request(s), want %q after 2", got, requests.Load(), "recovered")
	}
	if took > 10*time.Second {
		t.Errorf("took %s; the stall was not cut at the watchdog", took)
	}
}

// Silent MID-ANSWER: the attempt ends promptly with a stall error, and is not
// retried -- a retry would repeat text the user has already read.
func TestAProviderThatGoesSilentMidAnswerEndsPromptly(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	base, requests := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseContent(w, "partial ")
		<-r.Context().Done()
	})
	got, err, took := runStream(t, base)
	if err == nil {
		t.Fatal("a stalled stream was reported as a success")
	}
	if !asModelError(err).stalled || !strings.Contains(asModelError(err).Detail(), "sent nothing") {
		t.Errorf("error = %v (%s), want a stall", err, asModelError(err).Detail())
	}
	if got != "partial " || requests.Load() != 1 {
		t.Errorf("got %q after %d request(s), want the partial answer and no retry", got, requests.Load())
	}
	if took > 10*time.Second {
		t.Errorf("took %s to notice a silent provider", took)
	}
}

// A slow model that is still talking is never cut: any byte, keep-alive
// comments included, re-arms the watchdog.
func TestASlowButLiveStreamIsNotCut(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	base, requests := stallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 8; i++ { // 8 x 100ms = well past the 300ms watchdog
			fmt.Fprint(w, ": OPENROUTER PROCESSING\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		sseContent(w, "slow but fine")
		sseDone(w)
	})
	got, err, _ := runStream(t, base)
	if err != nil || got != "slow but fine" || requests.Load() != 1 {
		t.Errorf("got %q, err %v, %d request(s): a live stream was cut", got, err, requests.Load())
	}
}

// Stalls in a row end the turn -- never a loop. With the production 60s
// watchdog the first stall alone spends totalRetryBudget, so only the one
// stall retry happens; with this test's short watchdog the ordinary budget
// still has room, so the bound that holds either way is maxStreamAttempts.
func TestRepeatedStallsEndTheTurnInBoundedTime(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	base, requests := stallServer(t, hang)
	_, err, took := runStream(t, base)
	if err == nil || !asModelError(err).stalled {
		t.Fatalf("err = %v, want a stall error", err)
	}
	if n := requests.Load(); n < 2 || n > maxStreamAttempts {
		t.Errorf("%d attempts, want between 2 and %d", n, maxStreamAttempts)
	}
	if took > 20*time.Second {
		t.Errorf("took %s", took)
	}
}
