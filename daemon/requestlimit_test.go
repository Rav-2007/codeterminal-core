package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A REQUEST THAT REACHES THE DAEMON'S OWN TIME LIMIT WHILE THE MODEL IS STILL
// SENDING. See requestTimeout.
//
// MEASURED 2026-09-29, live: a model reasoned for five minutes -- 39,000
// characters of it -- and was cut at exactly 300 s. Bytes were arriving the
// whole time, so the stall watchdog had nothing to say; the attempt had spent
// the whole retry budget by itself, so it was not retried; and the user was
// told "the model provider is unreachable", about a provider that had been
// talking to them for five minutes.

// shortRequestLimit shrinks the request limit and the retry budget for one
// test, keeping the limit LONGER than the budget -- which is the real
// arrangement (5 minutes against 45 seconds) and the whole reason the first
// attempt used to be the last.
func shortRequestLimit(t *testing.T, limit time.Duration) {
	t.Helper()
	prevLimit, prevBudget := requestTimeout, totalRetryBudget
	requestTimeout, totalRetryBudget = limit, limit/3
	t.Cleanup(func() { requestTimeout, totalRetryBudget = prevLimit, prevBudget })
	shortStall(t, 10*limit) // never the watchdog: these servers do not go silent
}

// reasonForever streams reasoning, and no answer, until the client gives up.
func reasonForever(w http.ResponseWriter, r *http.Request) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"still thinking \"}}]}\n\n")
			w.(http.Flusher).Flush()
		}
	}
}

// ONE MORE TRY, even though the first has outlasted the retry budget: a model
// that ran away once often does not the second time, and nothing has been shown
// that a second attempt would repeat.
//
// Neuter check: drop `|| modelErr.ranOut` from streamWithRetry.
func TestARequestThatRanOutOfTimeIsTriedOnceMore(t *testing.T) {
	shortRequestLimit(t, 300*time.Millisecond)
	base, requests := stallServer(t,
		reasonForever,
		func(w http.ResponseWriter, _ *http.Request) { sseContent(w, "recovered"); sseDone(w) },
	)
	got, _, err := runStream(t, base)
	if err != nil {
		t.Fatalf("the second attempt answered, but the turn failed: %v", err)
	}
	if got != "recovered" || requests.Load() != 2 {
		t.Errorf("got %q after %d request(s), want the second attempt's answer", got, requests.Load())
	}
}

// AND ONLY ONE. Two in a row end the turn, in bounded time, with a sentence
// that says what happened -- and does not blame the connection.
func TestARequestThatRunsOutTwiceSaysSoAndStops(t *testing.T) {
	shortRequestLimit(t, 300*time.Millisecond)
	base, requests := stallServer(t, reasonForever)

	_, took, err := runStream(t, base)
	if err == nil {
		t.Fatalf("a model that never finishes was reported as a success")
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d request(s), want exactly 2: the attempt and its one retry", n)
	}
	if took > 3*time.Second {
		t.Errorf("took %s; two attempts of 300ms each must end the turn promptly", took)
	}
	var me *ModelError
	if !errors.As(err, &me) || !me.ranOut {
		t.Fatalf("the failure is not marked as having run out of time: %v", err)
	}
	said := err.Error()
	if strings.Contains(said, "unreachable") {
		t.Errorf("a provider that was sending the whole time is called unreachable: %q", said)
	}
	for _, want := range []string{"had not finished", "300ms"} {
		if !strings.Contains(said, want) {
			t.Errorf("the message does not say %q: %q", want, said)
		}
	}
}

// NOT AFTER PART OF THE ANSWER WAS SHOWN. A second attempt would write it
// again under the first -- the rule every retry here already follows.
func TestARequestThatRanOutAfterAnsweringIsNotRetried(t *testing.T) {
	shortRequestLimit(t, 300*time.Millisecond)
	base, requests := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseContent(w, "the first half")
		reasonForever(w, r)
	})
	got, _, err := runStream(t, base)
	if err == nil || requests.Load() != 1 {
		t.Fatalf("err=%v after %d request(s); want a failure and no second attempt", err, requests.Load())
	}
	if got != "the first half" {
		t.Errorf("the client was shown %q, want what arrived before the limit", got)
	}
}

// THE CALLER'S OWN DEADLINE IS NOT THIS LIMIT. A turn that ran out of ITS time
// is not described as a request that reached the five-minute limit -- which
// would name a limit that was never reached, to a user whose turn was cut by a
// different one.
//
// The answer has begun, so the failure comes back as the stream reported it
// (nothing is retried after output), and what it says is checked directly.
func TestTheCallersDeadlineIsNotMistakenForTheRequestLimit(t *testing.T) {
	shortRequestLimit(t, 10*time.Second)
	base, requests := stallServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseContent(w, "the first half")
		reasonForever(w, r)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := streamWithRetry(ctx, base, "k", "m",
		[]chatMessage{{Role: "user", Content: "hi"}}, nil, providerRouting{},
		func(string) error { return nil }, func(string) {}, func(string) {}, func(string) {}, nil)
	if err == nil {
		t.Fatalf("a turn past its deadline was reported as a success")
	}
	var me *ModelError
	if !errors.As(err, &me) {
		t.Fatalf("the failure is not a classified one, so this test checks nothing: %v", err)
	}
	if me.ranOut || strings.Contains(err.Error(), "the limit for one request") {
		t.Errorf("the caller's deadline was reported as the request limit: %v", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d request(s) after the caller's deadline passed, want 1", n)
	}
}
