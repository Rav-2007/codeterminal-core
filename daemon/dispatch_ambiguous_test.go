package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// A body naming two request types is ambiguous, and ambiguity must not be
// resolved by which sniffer serveConn happens to call first.
//
// requestfields.go stated this rule before it enforced it for this shape:
// "an ambiguous request has no single correct interpretation and must not be
// guessed at, least of all into a destructive handler". It held for duplicate
// keys and for case-folded keys. It did not hold here, and the handler that
// won was apply-edit, which writes to the user's files.
//
// These tests assert the OUTCOME ON DISK, not the sniffer's return value. A
// sniffer-level assertion would keep passing if serveConn ever grew its own
// dispatch shortcut; the file's contents cannot.

// ambiguousDispatchWorkspace builds a workspace whose one file the hostile
// bodies below all try to rewrite.
func ambiguousDispatchWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "target.txt"), []byte("ORIGINAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws
}

// driveOneRequest runs the real handshake-then-dispatch path over a pipe and
// returns everything the daemon wrote back, plus its log.
func driveOneRequest(t *testing.T, srv *Server, logbuf *bytes.Buffer, body string) []string {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		serverConn.Close()
		close(done)
	}()
	defer func() {
		clientConn.Close()
		<-done
	}()

	hs, err := json.Marshal(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion, ClientName: "ambiguity-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := clientConn.Write(append(hs, '\n')); err != nil {
		t.Fatalf("writing handshake: %v", err)
	}
	dec := json.NewDecoder(clientConn)
	clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hsResp protocol.HandshakeResponse
	if err := dec.Decode(&hsResp); err != nil {
		t.Fatalf("reading handshake response: %v", err)
	}
	if !hsResp.Ok {
		t.Fatalf("handshake rejected: %+v", hsResp)
	}

	clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := clientConn.Write([]byte(body + "\n")); err != nil {
		t.Fatalf("writing body: %v", err)
	}

	var replies []string
	for {
		clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var m json.RawMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		replies = append(replies, string(m))
	}
	return replies
}

func TestDispatch_TwoDiscriminatorsNeverReachTheDestructiveHandler(t *testing.T) {
	const edit = `"edit":{"file_path":"target.txt","search":"ORIGINAL","replace":"EDITED"}`

	// Every row named a second request type alongside the edit, and every row
	// used to come back {"applied":true} with the file rewritten. The order
	// within each body is varied deliberately: the defect was decided by
	// serveConn's dispatch order, and a fix that only reordered the block would
	// pass some of these and fail others.
	for _, tc := range []struct{ name, body string }{
		{"edit then undo", edit + `,"undo":true`},
		{"undo then edit", `"undo":true,` + edit},
		{"edit then status", edit + `,"status":true`},
		{"status then edit", `"status":true,` + edit},
		{"edit then search", edit + `,"search":true`},
		{"edit then approval", edit + `,"approval":true`},
		{"every discriminator at once", `"status":true,"search":true,"undo":true,` + edit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := ambiguousDispatchWorkspace(t)
			var logbuf bytes.Buffer
			srv := &Server{
				apiBase:   "http://127.0.0.1:1", // nothing listens here
				logger:    log.New(&logbuf, "", 0),
				workspace: ws,
			}
			body := fmt.Sprintf(`{"protocol_version":%d,%s}`, protocol.ProtocolVersion, tc.body)
			replies := driveOneRequest(t, srv, &logbuf, body)

			after, err := os.ReadFile(filepath.Join(ws, "target.txt"))
			if err != nil {
				t.Fatalf("reading target: %v", err)
			}
			if string(after) != "ORIGINAL\n" {
				t.Errorf("an ambiguous body rewrote the file: content is now %q, want %q\nbody:    %s\nreplies: %v\nlog:     %s",
					string(after), "ORIGINAL\n", body, replies, strings.TrimSpace(logbuf.String()))
			}
			if joined := strings.Join(replies, " "); strings.Contains(joined, `"applied":true`) {
				t.Errorf("an ambiguous body was answered as an applied edit: %s", joined)
			}
			// The vacuity floor: the daemon must have READ the body and said
			// something. A serveConn that returned before dispatch would leave
			// the file untouched too, and prove nothing.
			if len(replies) == 0 {
				t.Fatalf("daemon sent no response at all; this test cannot have checked dispatch (log: %s)",
					strings.TrimSpace(logbuf.String()))
			}
		})
	}
}

// The other direction. Widening the refusal must not start refusing the
// messages the shipped clients actually send -- each of which carries exactly
// one discriminator, and one of which (ApplyEditRequest) carries a nested
// "search" key inside its edit block that must not be counted as a second one.
func TestDispatch_OneDiscriminatorStillRoutes(t *testing.T) {
	const editBody = `{"file_path":"a.go","search":"x","replace":"y"}`
	for _, tc := range []struct {
		name  string
		raw   string
		sniff func(json.RawMessage) bool
	}{
		{"apply-edit, whose block has its own nested search key", `{"edit":` + editBody + `}`, isApplyEditRequest},
		{"undo", `{"undo":true,"workspace":"/ws"}`, isUndoRequest},
		{"status", `{"status":true}`, isStatusRequest},
		{"search", `{"search":true,"query":"goroutines","limit":5}`, isSearchRequest},
		{"tool approval", `{"approval":true,"call_id":"c1"}`, isToolApprovalResponse},
		// A null discriminator does not select a handler (hasBoolKey), so it
		// must not count as a second selector and refuse an otherwise
		// unambiguous body.
		{"apply-edit beside a null undo", `{"edit":` + editBody + `,"undo":null}`, isApplyEditRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.sniff(json.RawMessage(tc.raw)) {
				t.Errorf("the ambiguity check refused a legitimate single-discriminator body: %s", tc.raw)
			}
		})
	}
}

// countDiscriminators is the whole rule, so it gets its own table -- including
// the shapes that decide whether "present" or "would sniff" is being counted.
func TestCountDiscriminators(t *testing.T) {
	const editBody = `{"file_path":"a.go","search":"x","replace":"y"}`
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"prompt carries none", `{"prompt":"hello","reset":true}`, 0},
		{"edit alone", `{"edit":` + editBody + `}`, 1},
		{"undo alone", `{"undo":true}`, 1},
		{"undo false still selects", `{"undo":false}`, 1},
		{"undo null selects nothing", `{"undo":null}`, 0},
		{"edit null selects nothing", `{"edit":null}`, 0},
		{"edit not an object selects nothing", `{"edit":"a.go"}`, 0},
		{"undo and edit", `{"undo":true,"edit":` + editBody + `}`, 2},
		{"all five", `{"edit":` + editBody + `,"undo":true,"status":true,"search":true,"approval":true}`, 5},
		{"nested search inside edit is not top level", `{"edit":` + editBody + `}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.raw), &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := countDiscriminators(fields); got != tc.want {
				t.Errorf("countDiscriminators(%s) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
