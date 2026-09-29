package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// ESC STOPS A THINKING MODEL ON THE PLAIN PATH TOO.
//
// A reasoning model thinks before it answers, and every thinking token is
// billed. The plain (no tools) path used to drop the error from its reasoning
// write, so a client that hung up mid-think was noticed only at the first
// ANSWER token -- by which point the whole think had been generated and paid
// for. MEASURED by the 2026-09-29 cost audit: esc stopped an agent turn in 0.6 s
// and did not stop a plain one at all until the answer began.
//
// The upstream here thinks for thinkFor and answers only after that; the
// client hangs up after the first reasoning message. The assertion is on the
// UPSTREAM: its request must be cancelled soon after the hang-up, not run to
// its answer.
//
// Neuter check: make the plain path's reasoning callback ignore tw.write's
// failure without cancelling (enc.Encode instead of tw.write) and the upstream
// thinks to the end.
func TestAClientThatHangsUpWhileTheModelThinksStopsThePlainTurn(t *testing.T) {
	const thinkFor = 5 * time.Second
	type outcome struct {
		cancelled bool
		at        time.Time
	}
	result := make(chan outcome, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		end := time.Now().Add(thinkFor)
		for time.Now().Before(end) {
			if _, err := w.Write([]byte(`data: {"choices":[{"delta":{"reasoning":"thinking... "}}]}` + "\n\n")); err != nil {
				result <- outcome{cancelled: true, at: time.Now()}
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				result <- outcome{cancelled: true, at: time.Now()}
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"the answer"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"))
		result <- outcome{at: time.Now()}
	}))
	t.Cleanup(upstream.Close)

	var mu sync.Mutex
	var lines []string
	sockAddr, _, _ := agentSocketServerLogged(t, upstream.URL, MCPConfig{}, capturingLogger(&mu, &lines))
	// No capabilities: this client cannot answer an approval, so it gets the
	// plain path.
	_, dec, conn := agentClient(t, sockAddr, "why is the sky blue?", nil)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var msg protocol.TokenResponse
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("the turn never streamed any reasoning: %v", err)
		}
		if msg.Done {
			t.Fatalf("the turn finished before it started thinking: %+v", msg)
		}
		if msg.Reasoning != "" {
			break
		}
	}
	hungUp := time.Now()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-result:
		if !got.cancelled {
			t.Fatalf("the model thought for the full %s after the client hung up -- every thinking token "+
				"billed for nobody. A failed reasoning write must cancel the turn (tw.write).", thinkFor)
		}
		if took := got.at.Sub(hungUp); took > 2*time.Second {
			t.Errorf("the upstream call was cancelled %s after the hang-up, want under 2s", took)
		}
	case <-time.After(thinkFor + 5*time.Second):
		t.Fatal("the upstream request neither finished nor was cancelled")
	}

	// The stop is logged as a stop, not as a model API error.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		joined := strings.Join(lines, "")
		mu.Unlock()
		if strings.Contains(joined, "went away mid-answer") {
			if strings.Contains(joined, "model API error") {
				t.Errorf("the stop was also logged as a model API error:\n%s", joined)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never logged the stop; log:\n%s", joined)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A CANCELLED TURN IS NEVER RETRIED, AND ITS ERROR SAYS SO.
//
// When the client's hang-up is found by a failed TOKEN write, the attempt has
// already streamed, and streamWithRetry used to return the write error itself.
// Callers decide "interrupt or failure?" with errors.Is(err, context.Canceled),
// so the agent loop read a user's stop as a provider failure: it sent a Done to
// nobody and saved the stopped turn to memory.
//
// Neuter check: delete the ctx.Err() check at the top of streamWithRetry's
// failure handling and this gets the write error back.
func TestAStoppedTurnComesBackAsCancelled(t *testing.T) {
	base, calls, _ := agentUpstream(t, textSSE("partial answer"), textSSE("second attempt"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writeErr := errors.New("write unix: broken pipe")

	_, err := streamWithRetry(ctx, base, "k", "test-model", []chatMessage{{Role: "user", Content: "hi"}}, nil,
		providerRouting{},
		func(string) error {
			cancel() // what turnWriter.write does when the client is gone
			return writeErr
		},
		nil, nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled: a stop must read as a stop", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("model calls = %d, want 1: a cancelled turn must not be retried", got)
	}
}

// AN AGENT TURN STOPPED WHILE ITS ANSWER STREAMS IS A STOP, NOT A FAILURE.
//
// The loop turns a provider error after some output into an "incomplete"
// result with a nil error, so the work is kept. A cancel came through the same
// branch and was saved as though the provider had failed.
//
// Neuter check: remove the context.Canceled early return in runAgentLoop's
// error branch and the log says "after a provider failure".
func TestAnAgentTurnStoppedMidAnswerIsNotAProviderFailure(t *testing.T) {
	response := []string{
		`data: {"choices":[{"delta":{"content":"here is the first half"}}]}`,
		ssePause,
		`data: {"choices":[{"delta":{"content":" and the second half"}}]}`,
		ssePause,
		`data: {"choices":[{"delta":{"content":" and the end"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	base, _, _ := agentUpstream(t, response)
	var mu sync.Mutex
	var lines []string
	sockAddr, _, _ := agentSocketServerLogged(t, base, MCPConfig{Enabled: true}, capturingLogger(&mu, &lines))
	_, dec, conn := agentClient(t, sockAddr, "explain it", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var msg protocol.TokenResponse
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("the turn never streamed a token: %v", err)
		}
		if msg.Done {
			t.Fatalf("the turn finished before streaming a token: %+v", msg)
		}
		if msg.Token != "" {
			break
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		joined := strings.Join(lines, "")
		mu.Unlock()
		if strings.Contains(joined, "provider failure") {
			t.Fatalf("a stop was treated as a provider failure:\n%s", joined)
		}
		if strings.Contains(joined, "went away mid-turn") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never logged the stop; log:\n%s", joined)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
