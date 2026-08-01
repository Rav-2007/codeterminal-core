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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
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

	// upstreamBodies is the byte size of every request body the fake OpenRouter
	// received, in order. THE GROWTH CURVE ACROSS ONE TURN IS THE POINT: an
	// agent loop re-sends the whole message list on every iteration, so it
	// carries the system prompt, the retrieved context, and every tool result so
	// far. Whether that grows linearly or worse is the difference between "an
	// agent turn costs N x a normal turn" and "it costs N^2", and no number
	// anywhere else in this repo answers it.
	upstreamBodies []int
	// upstreamTools records whether each request carried a "tools" field, so a
	// turn's shape is readable without re-parsing the bodies.
	upstreamTools []bool
}

func (l *ledger) recordUpstream(bodyBytes int, hadTools bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.upstreamBodies = append(l.upstreamBodies, bodyBytes)
	l.upstreamTools = append(l.upstreamTools, hadTools)
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
		supabaseDelay = flag.Duration("supabase-delay", 0,
			"artificial per-call latency on every fake Supabase endpoint. 0 measures the "+
				"proxy's OWN overhead on a loopback; a production-like value (the 2026-07-17 "+
				"note measured ~90ms for the second sequential call) is what makes a "+
				"round-trip-count change -- an auth cache, or collapsing authorize+reserve -- "+
				"show up as the number a user would actually feel.")

		// AGENT MODE. Without these the fake upstream always answers with text,
		// so the daemon's agent loop runs exactly one iteration and every
		// question about what a LOOP costs the proxy is unanswerable without
		// spending real money on a real model.
		//
		// The cycle is deterministic on purpose: responses 1..N of each cycle
		// request a tool, response N+1 is plain text, and the counter wraps. One
		// turn is therefore exactly N+1 upstream requests, every time, which is
		// what makes "reservations per turn" a number rather than an average.
		toolCalls = flag.Int("tool-calls", 0,
			"emit this many tool_call responses before answering with text, then repeat. "+
				"0 (the default) is the original text-only behaviour. Set it to the daemon's "+
				"mcp.budget.max_iterations minus one to drive a worst-case agent turn.")
		toolName = flag.String("tool-name", "builtin__list_directory",
			"qualified tool name the fake model asks for. The default needs no index and no "+
				"approval channel when its policy is \"allow\".")
		toolArgs = flag.String("tool-args", `{"path":"."}`,
			"argument JSON the fake model sends with each tool call")

		// reserve_usage returns NO ROW when a key is over its limit -- that is the
		// real over-limit signal (proxy/main.go's len(rows) != 1 gate), and the
		// only way to make the proxy answer 429 quota_exceeded. Without this the
		// fake always succeeds, so "what does a user lose when quota runs out
		// PART-WAY THROUGH an agent turn?" is unaskable.
		failAfter = flag.Int("reserve-fail-after", 0,
			"after this many successful reservations, return no row from reserve_usage "+
				"(the over-limit signal). 0 never fails. Set it below the iteration count "+
				"to kill a turn mid-way.")
	)
	flag.Parse()

	l := &ledger{nextPending: 1000, openPending: map[int64]bool{}}

	// ---- fake Supabase: the four PostgREST calls the proxy actually makes ----
	sb := http.NewServeMux()

	var keyRotation, reserveSeq atomic.Int64
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

		if *failAfter > 0 && int(reserveSeq.Add(1)) > *failAfter {
			l.record("reserve_usage(OVER LIMIT)", r.RemoteAddr)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
			return
		}

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
			"calls":           l.calls,
			"reserved":        l.reserved,
			"corrections":     l.corrected,
			"open_pending":    len(l.openPending),
			"distinct_conns":  len(l.conns),
			"upstream_bodies": l.upstreamBodies,
			"upstream_tools":  l.upstreamTools,
		})
	})

	// The delay wraps the whole mux rather than each handler, so it covers
	// /__ledger too -- deliberately. The ledger read-out is the harness's own
	// endpoint and is never on a measured path, and a wrapper that quietly
	// exempted some routes would be one more thing to remember when reading a
	// number off this harness later.
	var sbHandler http.Handler = sb
	if *supabaseDelay > 0 {
		inner := sbHandler
		sbHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(*supabaseDelay)
			inner.ServeHTTP(w, r)
		})
	}

	go func() {
		log.Printf("fake supabase on %s (per-call delay %s)", *supabaseAddr, *supabaseDelay)
		if err := http.ListenAndServe(*supabaseAddr, sbHandler); err != nil {
			log.Fatalf("fake supabase: %v", err)
		}
	}()

	// ---- fake OpenRouter -----------------------------------------------------
	up := http.NewServeMux()
	var upstreamSeq atomic.Int64
	up.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// Read the body before answering: its SIZE is the measurement, and the
		// presence of a "tools" field distinguishes an agent-mode request from
		// the byte-identical-when-disabled one. The content is never parsed
		// beyond that -- this harness has no more business reading a prompt than
		// the proxy does.
		body, _ := io.ReadAll(r.Body)
		l.recordUpstream(len(body), bytes.Contains(body, []byte(`"tools":`)))

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		// AGENT MODE: ask for a tool on the first N responses of each cycle,
		// answer with text on the (N+1)th. Deterministic, so one turn is always
		// exactly N+1 upstream requests.
		if *toolCalls > 0 {
			n := upstreamSeq.Add(1) - 1
			if n%int64(*toolCalls+1) < int64(*toolCalls) {
				args, _ := json.Marshal(*toolArgs)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n",
					n, *toolName, string(args))
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"total_tokens\":"+strconv.Itoa(*totalTokens)+"}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				if fl != nil {
					fl.Flush()
				}
				return
			}
		}

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
