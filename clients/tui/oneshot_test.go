package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// oneShotApprovalDaemon stands up a fake daemon that asks for approval of one
// tool call, then finishes. It reports back the answer it received, so a test
// can assert what actually went on the wire rather than what was printed.
func oneShotApprovalDaemon(t *testing.T, req protocol.ToolApprovalRequest) <-chan protocol.ToolApprovalResponse {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath := filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setLockPathForTest(t, lockPath))

	answers := make(chan protocol.ToolApprovalResponse, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)

		var hs protocol.HandshakeRequest
		if err := dec.Decode(&hs); err != nil {
			return
		}
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
		var prompt protocol.PromptRequest
		if err := dec.Decode(&prompt); err != nil {
			return
		}

		ask := req
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, ToolApproval: &ask})
		var answer protocol.ToolApprovalResponse
		if err := dec.Decode(&answer); err != nil {
			return
		}
		answers <- answer
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: "done"})
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
	}()
	return answers
}

func oneShotRequest() protocol.ToolApprovalRequest {
	args := `{"path":"/etc/passwd"}`
	return protocol.ToolApprovalRequest{
		CallID:          "c1",
		Server:          "somebodys-server",
		Tool:            "read_anything",
		Arguments:       args,
		ArgumentsSHA256: "deadbeef",
		Lane:            protocol.LaneThirdParty,
		Iteration:       1,
		MaxIterations:   8,
	}
}

// ONLY A LITERAL y OR a APPROVES -- the same strict default-deny confirm as the
// CLI's `[y/N]` for edits and the chat UI's modal.
//
// "yes", "Y" and "" are the three answers a person most plausibly gives at a
// [y/N] prompt expecting them to work. Each of them denies, on purpose: the
// alternative is a prompt where a habit approves an unsandboxed subprocess.
func TestOneShotOnlyApprovesOnALiteralYesOrAllow(t *testing.T) {
	for _, tc := range []struct {
		typed        string
		wantDecision string
		wantApproval bool
	}{
		{"y\n", protocol.ApprovalApprove, true},
		{"a\n", protocol.ApprovalApproveForTurn, true},
		{"n\n", protocol.ApprovalDeny, false},
		{"q\n", protocol.ApprovalCancelTurn, false},
		{"yes\n", protocol.ApprovalDeny, false},
		{"Y\n", protocol.ApprovalDeny, false},
		{"\n", protocol.ApprovalDeny, false},
		{" \n", protocol.ApprovalDeny, false},
		{"", protocol.ApprovalDeny, false}, // EOF: stdin has nothing left to give
	} {
		t.Run(strings.TrimSpace(tc.typed)+"|", func(t *testing.T) {
			req := oneShotRequest()
			answers := oneShotApprovalDaemon(t, req)

			var out, errOut bytes.Buffer
			runOneShotPrompt("test", "hi", oneShotIO{
				in: strings.NewReader(tc.typed), out: &out, err: &errOut, interactive: true,
			})

			select {
			case got := <-answers:
				if got.Decision != tc.wantDecision {
					t.Errorf("typing %q sent decision %q, want %q", tc.typed, got.Decision, tc.wantDecision)
				}
				if got.Approval != tc.wantApproval {
					t.Errorf("typing %q sent approval=%t, want %t", tc.typed, got.Approval, tc.wantApproval)
				}
				// The echo is what binds the answer to the question. A client
				// that recomputed either field would be attesting to its own
				// rendering rather than to the daemon's bytes.
				if got.CallID != req.CallID || got.ArgumentsSHA256 != req.ArgumentsSHA256 {
					t.Errorf("the answer does not echo the question verbatim: %+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no answer reached the daemon")
			}
		})
	}
}

// STDOUT IS THE ANSWER. Scripts pipe it. Approval prompts, activity narration
// and incomplete notices all go to stderr, so a caller that pipes stdout gets
// exactly what it got before agent mode existed.
func TestOneShotKeepsStdoutClean(t *testing.T) {
	req := oneShotRequest()
	oneShotApprovalDaemon(t, req)

	var out, errOut bytes.Buffer
	runOneShotPrompt("test", "hi", oneShotIO{
		in: strings.NewReader("y\n"), out: &out, err: &errOut, interactive: true,
	})

	if got := out.String(); got != "done\n" {
		t.Errorf("stdout carried more than the answer: %q", got)
	}
	prompt := errOut.String()
	for _, want := range []string{req.Arguments, "read_anything", "NOT SANDBOXED", "[y]es"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt on stderr is missing %q:\n%s", want, prompt)
		}
	}
}

// The panel must show what will run, in full, and must not soften the lane --
// and must not cry wolf on a confined built-in either.
func TestOneShotPromptIsHonestAboutTheLane(t *testing.T) {
	var buf bytes.Buffer
	askOneShotApproval(oneShotRequest(), bufio.NewReader(strings.NewReader("n\n")), &buf)
	if !strings.Contains(buf.String(), "NOT SANDBOXED") {
		t.Errorf("an unconfined third-party tool was not described as unconfined:\n%s", buf.String())
	}

	confined := oneShotRequest()
	confined.Server, confined.Lane, confined.Confined = "builtin", protocol.LaneFirstParty, true
	buf.Reset()
	askOneShotApproval(confined, bufio.NewReader(strings.NewReader("n\n")), &buf)
	if strings.Contains(buf.String(), "NOT SANDBOXED") {
		t.Errorf("a confined built-in was labelled unsandboxed:\n%s", buf.String())
	}
}

func TestOneShotActivityLine(t *testing.T) {
	act := protocol.ToolActivity{
		Server: "srv", Tool: "tool", Phase: protocol.ToolPhaseRunning,
	}
	if line := oneShotActivityLine(act); !strings.Contains(line, "running srv__tool") {
		t.Errorf("unexpected running line: %s", line)
	}

	act.Phase = protocol.ToolPhaseSucceeded
	act.ResultBytes = 100
	act.DurationMS = 50
	if line := oneShotActivityLine(act); !strings.Contains(line, "100 bytes") {
		t.Errorf("unexpected succeeded line: %s", line)
	}

	act.Phase = protocol.ToolPhaseFailed
	act.Detail = "bad input"
	if line := oneShotActivityLine(act); !strings.Contains(line, "failed: bad input") {
		t.Errorf("unexpected failed line: %s", line)
	}

	act.Phase = protocol.ToolPhaseDenied
	if line := oneShotActivityLine(act); !strings.Contains(line, "not run: bad input") {
		t.Errorf("unexpected denied line: %s", line)
	}
}
