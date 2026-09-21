// agentbench drives real agent turns against a running daemon and reports what
// each one cost, as JSON.
//
// It exists because no shipped client can be scripted into agent mode. The chat
// TUI declares CapToolApproval but needs a terminal; the one-shot CLI declares
// it only with --prompt from a tty, precisely so a piped run cannot promise an
// answer it has nobody to give. Both of those are correct, and both make "run
// 50 agent turns and read the numbers off" impossible without this.
//
// So this is a minimal, honest client: it declares the capability and it KEEPS
// the promise, answering every approval from a policy given on the command line.
// It is not a product surface and never becomes one -- under testdata/, so
// `go build ./...`, `go vet ./...` and the coverage ratchet all ignore it.
//
// WHAT IT MEASURES, and why each field is here rather than "some timings":
//
//	ttft_ms       time to the first token of the WHOLE turn -- what a user feels
//	wall_ms       the whole turn, approval waits included
//	waited_ms     time this client spent "deciding", so wall_ms can be corrected
//	              to machine time. A benchmark that charged its own think time to
//	              the daemon would report a latency nobody experiences.
//	iterations    inferred from tool activity, since the wire carries no step count
//	tool_calls    every ToolActivity in a terminal phase
//	result_bytes  sum of post-scrub bytes fed back to the model -- in agent mode
//	              this is the quantity that leaves the machine, which is the
//	              number a privacy-positioned product should be able to report
//	approvals     how many times the daemon actually stopped and asked
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"sort"
	"time"

	"mochiii/protocol"
)

type turnRecord struct {
	Turn        int    `json:"turn"`
	TTFTMS      int64  `json:"ttft_ms"`
	WallMS      int64  `json:"wall_ms"`
	WaitedMS    int64  `json:"waited_ms"`
	Iterations  int    `json:"iterations"`
	ToolCalls   int    `json:"tool_calls"`
	Approvals   int    `json:"approvals"`
	ResultBytes int    `json:"result_bytes"`
	AnswerBytes int    `json:"answer_bytes"`
	Incomplete  string `json:"incomplete,omitempty"`
	Error       string `json:"error,omitempty"`
}

func main() {
	var (
		turns    = flag.Int("turns", 5, "how many agent turns to run")
		prompt   = flag.String("prompt", "list the files here and summarise them", "prompt to send each turn")
		decision = flag.String("decision", protocol.ApprovalApprove,
			"how to answer every approval: approve | approve_for_turn | deny | cancel_turn")
		thinkMS = flag.Int("think-ms", 0,
			"pause this long before answering each approval, simulating a human. Reported "+
				"separately as waited_ms so it never contaminates the latency figures.")
		workspace = flag.String("workspace", ".", "workspace to send with the prompt")
		pace      = flag.Duration("pace", 0,
			"sleep between turns. The proxy's per-key limiter is 2 req/s with a burst of 20 "+
				"(proxy/ratelimit.go) and ONE agent turn is several requests, so an unpaced run "+
				"measures throttling rather than cost unless that is what you are measuring.")
	)
	flag.Parse()

	records := make([]turnRecord, 0, *turns)
	for i := 1; i <= *turns; i++ {
		if i > 1 && *pace > 0 {
			time.Sleep(*pace)
		}
		records = append(records, runTurn(i, *prompt, *workspace, *decision, time.Duration(*thinkMS)*time.Millisecond))
	}

	out := map[string]any{"turns": records, "summary": summarise(records)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		log.Fatal(err)
	}
}

func runTurn(n int, prompt, workspace, decision string, think time.Duration) turnRecord {
	rec := turnRecord{Turn: n}

	lockData, err := os.ReadFile(protocol.LockPath())
	if err != nil {
		rec.Error = "no daemon lockfile: " + err.Error()
		return rec
	}
	var lock protocol.LockFile
	if err := json.Unmarshal(lockData, &lock); err != nil {
		rec.Error = "corrupt lockfile: " + err.Error()
		return rec
	}

	conn, err := net.Dial("unix", lock.SocketPath)
	if err != nil {
		rec.Error = "dial: " + err.Error()
		return rec
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(bufio.NewReader(conn))

	// The capability is declared because this client genuinely answers. That is
	// the whole contract -- see the file comment.
	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      "agentbench",
		Capabilities:    []string{protocol.CapToolApproval},
	}); err != nil {
		rec.Error = "handshake: " + err.Error()
		return rec
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		rec.Error = "handshake reply: " + err.Error()
		return rec
	}
	if !hs.Ok {
		rec.Error = "handshake refused: " + hs.Error
		return rec
	}

	started := time.Now()
	if err := enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
		Workspace:       workspace,
	}); err != nil {
		rec.Error = "prompt: " + err.Error()
		return rec
	}

	seenCalls := map[string]bool{}
	var firstToken time.Time
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			rec.Error = "stream: " + err.Error()
			break
		}
		if tok.ToolActivity != nil {
			a := tok.ToolActivity
			switch a.Phase {
			case protocol.ToolPhaseSucceeded, protocol.ToolPhaseFailed, protocol.ToolPhaseDenied:
				if !seenCalls[a.CallID] {
					seenCalls[a.CallID] = true
					rec.ToolCalls++
				}
				rec.ResultBytes += a.ResultBytes
			}
		}
		if tok.ToolApproval != nil {
			rec.Approvals++
			if think > 0 {
				time.Sleep(think)
				rec.WaitedMS += think.Milliseconds()
			}
			// Echoed verbatim: recomputing either field would attest to this
			// client's own copy rather than to the bytes the daemon sent, which
			// is exactly what the digest exists to rule out.
			if err := enc.Encode(protocol.ToolApprovalResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Approval: decision == protocol.ApprovalApprove ||
					decision == protocol.ApprovalApproveForTurn,
				CallID:          tok.ToolApproval.CallID,
				ArgumentsSHA256: tok.ToolApproval.ArgumentsSHA256,
				Decision:        decision,
			}); err != nil {
				rec.Error = "approval: " + err.Error()
				break
			}
		}
		if tok.Token != "" {
			if firstToken.IsZero() {
				firstToken = time.Now()
			}
			rec.AnswerBytes += len(tok.Token)
		}
		if tok.Error != "" {
			rec.Error = tok.Error
		}
		if tok.Done {
			if tok.Incomplete != nil {
				rec.Incomplete = tok.Incomplete.Reason
			}
			break
		}
	}

	rec.WallMS = time.Since(started).Milliseconds()
	if !firstToken.IsZero() {
		rec.TTFTMS = firstToken.Sub(started).Milliseconds()
	}
	// One model call per tool call, plus the final one that answered. Inferred
	// rather than reported: the wire deliberately carries no step counter, and
	// inventing a protocol field for a benchmark would be the tail wagging the
	// dog.
	rec.Iterations = rec.ToolCalls + 1
	return rec
}

func summarise(recs []turnRecord) map[string]any {
	if len(recs) == 0 {
		return map[string]any{}
	}
	var wall, ttft, machine []int64
	totalTools, totalBytes, totalApprovals, errs, incomplete := 0, 0, 0, 0, 0
	for _, r := range recs {
		wall = append(wall, r.WallMS)
		ttft = append(ttft, r.TTFTMS)
		machine = append(machine, r.WallMS-r.WaitedMS)
		totalTools += r.ToolCalls
		totalBytes += r.ResultBytes
		totalApprovals += r.Approvals
		if r.Error != "" {
			errs++
		}
		if r.Incomplete != "" {
			incomplete++
		}
	}
	return map[string]any{
		"turns":              len(recs),
		"errors":             errs,
		"incomplete":         incomplete,
		"p50_wall_ms":        pct(wall, 50),
		"p95_wall_ms":        pct(wall, 95),
		"p50_machine_ms":     pct(machine, 50),
		"p50_ttft_ms":        pct(ttft, 50),
		"tool_calls_total":   totalTools,
		"approvals_total":    totalApprovals,
		"result_bytes_total": totalBytes,
	}
}

func pct(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (p * (len(s) - 1)) / 100
	return s[idx]
}
