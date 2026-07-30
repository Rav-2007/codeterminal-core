// Command testharness serves a fake Supabase and a fake OpenRouter so the REAL
// proxy binary can be driven through faults out-of-process.
//
// WHY THIS EXISTS AS A BINARY, when proxy/integration_test.go already covers the
// same faults in process: some properties are only observable against a real
// process. A genuine SIGTERM (not a synthetic channel send), FD and thread
// counts from /proc, RSS growth over a 30-minute soak, and the behaviour of a
// real TCP client that gets its stream cut -- none of those exist inside
// `go test`.
//
// PROVENANCE: this is the Phase-0 baseline harness, which produced every number
// in docs/ROBUSTNESS_BASELINE.md -- the mid-stream SIGTERM finding (3,959 tokens
// stranded permanently, on every deploy), the connection-pooling measurements
// that REFUTED the ~80ms TTFT hypothesis, and the runtime plateau figures the
// soak test compares against. It lived in a session scratchpad under /tmp, where
// it was one sweep away from being lost along with the ability to reproduce any
// of it. Promoted here, tracked, and made configurable.
//
// Deliberately stdlib-only, matching the proxy's own zero-dependency property.
//
// Usage:
//
//	go run ./testharness -supabase 127.0.0.1:9101 -upstream 127.0.0.1:9102
//
// Then point the proxy at it:
//
//	SUPABASE_URL=http://127.0.0.1:9101 OPENROUTER_BASE=http://127.0.0.1:9102/... ./proxy
//
// The ledger is readable at any time:
//
//	curl -s http://127.0.0.1:9101/__ledger | jq
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ledger records what the proxy did to the money path, in order.
//
// The ORDER is the point. "authorize -> reserve_usage -> apply_correction" is a
// balanced request; "authorize -> reserve_usage" and nothing else is a stranded
// reservation, and the difference between those two strings is the entire
// Phase-0 finding.
type ledger struct {
	mu    sync.Mutex
	calls []string

	reserved  int
	corrected int

	// conns counts DISTINCT client connections seen on the Supabase side. This
	// is how connection reuse is measured: with pooling working, N sequential
	// requests share ~1 connection, and the un-drained-body hypothesis predicted
	// N. It measured 1 either way, which is what refuted it.
	conns map[string]bool

	// openPending tracks reservations opened but never corrected -- the rows a
	// real deployment would leave for the reconciliation sweep.
	openPending map[int64]bool

	nextPending int64
}

func (l *ledger) record(name, remote string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, name)
	if l.conns == nil {
		l.conns = map[string]bool{}
	}
	l.conns[remote] = true
}

func main() {
	var (
		supabaseAddr = flag.String("supabase", "127.0.0.1:9101", "listen address for the fake Supabase")
		upstreamAddr = flag.String("upstream", "127.0.0.1:9102", "listen address for the fake OpenRouter")
		chunkDelay   = flag.Duration("chunk-delay", 250*time.Millisecond,
			"delay between SSE chunks; 250ms lands a SIGTERM mid-stream reliably, a small value makes a load baseline quick")
		chunkCount  = flag.Int("chunks", 40, "SSE chunks before the usage frame")
		totalTokens = flag.Int("total-tokens", 137, "total_tokens reported in the final usage frame")
		tokenLimit  = flag.Int64("token-limit", 10_000_000, "token_limit reserve_usage reports")
		withPending = flag.Bool("pending-id", true,
			"return a pending_id from reserve_usage; false simulates a database without migration 0002")
		distinctKeys = flag.Int("distinct-keys", 0,
			"if > 0, hand out this many DIFFERENT api_keys.id values in rotation. The rate "+
				"limiters hold one bucket per key, so this is what makes bucket growth -- and "+
				"the sweep that reclaims it -- observable during a soak. 0 means one fixed key.")
	)
	flag.Parse()

	l := &ledger{nextPending: 1000, openPending: map[int64]bool{}}

	// ---- fake Supabase: the four PostgREST calls the proxy actually makes ----
	sb := http.NewServeMux()

	var keyRotation atomic.Int64
	sb.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		l.record("authorize", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")

		id := "key-under-test"
		if *distinctKeys > 0 {
			id = fmt.Sprintf("key-%08d", keyRotation.Add(1)%int64(*distinctKeys))
		}
		// Exactly one row, or the proxy fails closed on the row-count gate.
		json.NewEncoder(w).Encode([]map[string]any{{"id": id}})
	})

	sb.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			KeyID    string `json:"p_key_id"`
			Reserved int    `json:"p_reserved"`
		}
		json.NewDecoder(r.Body).Decode(&in)

		l.mu.Lock()
		l.nextPending++
		pending := l.nextPending
		l.reserved += in.Reserved
		l.openPending[pending] = true
		l.mu.Unlock()

		l.record("reserve_usage", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")

		row := map[string]any{
			"tokens_used": in.Reserved,
			"token_limit": *tokenLimit,
		}
		if *withPending {
			row["pending_id"] = pending
		}
		json.NewEncoder(w).Encode([]map[string]any{row})
	})

	sb.HandleFunc("/rest/v1/rpc/apply_correction", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			KeyID     string `json:"p_key_id"`
			Tokens    int    `json:"p_tokens"`
			PendingID int64  `json:"p_pending_id"`
		}
		json.NewDecoder(r.Body).Decode(&in)

		l.mu.Lock()
		l.corrected++
		delete(l.openPending, in.PendingID)
		l.mu.Unlock()

		l.record(fmt.Sprintf("apply_correction(tokens=%d,pending=%d)", in.Tokens, in.PendingID), r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	sb.HandleFunc("/rest/v1/rpc/sweep_pending_corrections", func(w http.ResponseWriter, r *http.Request) {
		l.record("sweep", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})

	// /__ledger is the harness's own read-out, not part of the Supabase API.
	// open_pending > 0 after a run is the stranded-reservation signal.
	sb.HandleFunc("/__ledger", func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"calls":          l.calls,
			"reserved":       l.reserved,
			"corrections":    l.corrected,
			"open_pending":   len(l.openPending),
			"distinct_conns": len(l.conns),
		})
	})

	go func() {
		log.Printf("fake supabase on %s", *supabaseAddr)
		if err := http.ListenAndServe(*supabaseAddr, sb); err != nil {
			log.Fatalf("fake supabase: %v", err)
		}
	}()

	// ---- fake OpenRouter -----------------------------------------------------
	up := http.NewServeMux()
	up.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		for i := 0; i < *chunkCount; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i)
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				// The proxy went away mid-stream -- a SIGTERM drill, or a client
				// disconnect propagated upstream. Logged because "did upstream
				// notice?" is a question the drills actually ask.
				log.Printf("upstream: client went away at chunk %d", i)
				return
			case <-time.After(*chunkDelay):
			}
		}

		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":%d}}\n\n", *totalTokens)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	})

	log.Printf("fake openrouter on %s", *upstreamAddr)
	if err := http.ListenAndServe(*upstreamAddr, up); err != nil {
		log.Printf("fake openrouter: %v", err)
		os.Exit(1)
	}
}
