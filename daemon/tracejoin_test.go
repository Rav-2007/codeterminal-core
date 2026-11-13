//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE JOIN FIELD, THROUGH THE REAL DAEMON (protocol.PromptRequest.Trace).
//
// A client names its turn with eight hex digits so its own trace and the
// daemon's can be joined. This file holds the half that is true in every build:
// a daemon that is not tracing (a default build, or a traced build with
// MOCHIII_TRACE unset) does nothing with the id, writes nothing anywhere and says
// nothing about it. The traced half, where the id is used and where a bad one is
// replaced, is tracejoin_traced_test.go.

// saysHi is a model that answers every request with "hi" and no tool call.
func saysHi(int, e2eChat) e2eReply {
	return e2eReply{lines: withUsage(textSSE("hi"), 10, 2, 0.0001)}
}

// joinTraceDir is where a traced daemon would write: <socket dir>/trace.
func joinTraceDir(t *testing.T) string {
	t.Helper()
	dir, err := protocol.SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "trace")
}

// joinCanary is an id that has the right shape, so a daemon that did look at it
// could not reject it; it is how the tests tell "ignored" from "invalid".
const joinCanary = "c0ffee42"

// rawPrompt sends body as one request line, after the handshake, and reads to
// Done. body is JSON the caller wrote by hand, so it can be malformed in ways a
// protocol.PromptRequest cannot be.
func (d *e2eDaemon) rawPrompt(t *testing.T, body string) (stream string, done protocol.TokenResponse) {
	t.Helper()
	conn, _, dec := d.dial(t)
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, body); err != nil {
		t.Fatalf("send: %v", err)
	}
	var seen strings.Builder
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatalf("reading the stream before Done: %v\nso far: %s", err, seen.String())
		}
		seen.Write(raw)
		var tok protocol.TokenResponse
		if err := json.Unmarshal(raw, &tok); err != nil {
			t.Fatalf("a response that is not a TokenResponse: %s", raw)
		}
		if tok.Done {
			return seen.String(), tok
		}
	}
}

// joinBody is a prompt request body with extra fields spliced in as JSON text.
func (d *e2eDaemon) joinBody(extra string) string {
	ws, _ := json.Marshal(d.workspace)
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"protocol_version":%d,"prompt":"say hi","workspace":%s%s}`, protocol.ProtocolVersion, ws, extra)
}

func TestTheJoinIDIsIgnoredWithNoTracingAndSaysNothing(t *testing.T) {
	model := newE2EModel(t, saysHi)
	d := startE2E(t, model, map[string]string{"greet.go": e2eGreet}, nil)

	turn := d.request(t, protocol.PromptRequest{Prompt: "say hi", Trace: joinCanary}, e2eApproveAll)
	if turn.done.Error != "" {
		t.Fatalf("a request that names its turn failed: %s", turn.done.Error)
	}
	if !strings.Contains(turn.text.String(), "hi") {
		t.Errorf("the answer is %q", turn.text.String())
	}
	if _, err := os.Stat(joinTraceDir(t)); err == nil {
		t.Errorf("%s exists although this daemon is not tracing", joinTraceDir(t))
	}
	var found []string
	_ = filepath.WalkDir(d.runtimeDir, func(p string, _ os.DirEntry, _ error) error {
		if strings.HasSuffix(p, ".jsonl") || strings.Contains(p, "trace") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("files that look like a trace: %v", found)
	}
	// The id is nowhere the daemon writes: its log, its answer, or the files it
	// keeps under the workspace and the runtime directory.
	var all bytes.Buffer
	all.WriteString(d.log.String())
	all.WriteString(turn.text.String())
	for _, root := range []string{d.runtimeDir, d.workspace} {
		_ = filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				if b, rerr := os.ReadFile(p); rerr == nil {
					all.Write(b)
				}
			}
			return nil
		})
	}
	if bytes.Contains(all.Bytes(), []byte(joinCanary)) {
		t.Errorf("the id the client sent is in what the daemon wrote or answered")
	}
}

// Each of these is a request a hostile or broken client could send. None may
// change the answer, and none may leave a trace of itself.
func TestAMalformedJoinFieldDoesNotChangeTheAnswerOrLeakIntoTheLog(t *testing.T) {
	model := newE2EModel(t, saysHi)
	d := startE2E(t, model, map[string]string{"greet.go": e2eGreet}, nil)
	const canary = "plainzebra-joinfieldcanary-8c41d9"
	for name, extra := range map[string]string{
		"valid":          `"trace":"` + joinCanary + `"`,
		"number":         `"trace":12345678`,
		"array":          `"trace":["` + canary + `"]`,
		"object":         `"trace":{"` + canary + `":1}`,
		"null":           `"trace":null`,
		"text":           `"trace":"` + canary + `"`,
		"duplicate":      `"trace":"` + joinCanary + `","trace":"` + canary + `"`,
		"upper-case key": `"TRACE":"` + canary + `"`,
		"escaped":        `"trace":"\u0063\u0030ffee42"`,
		"huge":           `"trace":"` + strings.Repeat(canary, 4000) + `"`,
	} {
		stream, done := d.rawPrompt(t, d.joinBody(extra))
		if done.Error != "" {
			t.Errorf("%s: the request failed: %s", name, done.Error)
		}
		if !strings.Contains(stream, "hi") {
			t.Errorf("%s: the answer changed: %s", name, stream)
		}
		for _, v := range []string{canary, joinCanary} {
			if strings.Contains(stream, v) || strings.Contains(d.log.String(), v) {
				t.Errorf("%s: %q was echoed or logged", name, v)
			}
		}
	}
}
