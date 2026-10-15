package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// THE 2026-10-15 REPORT, end to end in the daemon: "write a simple login page
// using html in the new_file", ~/Desktop/new_file an empty folder.

// fakeUserHome points $HOME at a fresh folder and returns it resolved.
func fakeUserHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	home, err := editapply.RealHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

// propose_edit at the folder fails with a category, a next step, no absolute
// path and no proposal; the file inside it is then proposed, not written.
func TestProposeEditAtAFolderSaysWhatToDoAndProposesNothing(t *testing.T) {
	home := fakeUserHome(t)
	folder := filepath.Join(home, "Desktop", "new_file")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	s := builtinTestServer(t)
	sink := &proposalSink{}

	res, err := s.builtinProposeEdit(context.Background(),
		json.RawMessage(`{"path":"~/Desktop/new_file","search":"","replace":"<html></html>\n"}`), sink)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("an edit to a folder succeeded: %s", res.Content)
	}
	for _, want := range []string{editFailIsDirectory, "Nothing was proposed or written", "~/Desktop/new_file/index.html"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result %q does not say %q", res.Content, want)
		}
	}
	if strings.Contains(res.Content, home) {
		t.Errorf("result %q names the absolute home path", res.Content)
	}
	// The chat row shows the first line: the reason, not the daemon's note.
	if row := toolFailureReason(res.Content); !strings.Contains(row, "is a directory") || strings.Contains(row, "[Mochiii") {
		t.Errorf("the chat row would read %q", row)
	}
	if sink.editCount() != 0 {
		t.Errorf("a failed edit was filed as a proposal: %d", sink.editCount())
	}

	res, err = s.builtinProposeEdit(context.Background(),
		json.RawMessage(`{"path":"~/Desktop/new_file/index.html","search":"","replace":"<html></html>\n"}`), sink)
	if err != nil || res.IsError {
		t.Fatalf("the file inside the folder was refused: %v %s", err, res.Content)
	}
	if !strings.Contains(res.Content, "creates") || sink.editCount() != 1 {
		t.Errorf("result %q, %d proposal(s); want one create", res.Content, sink.editCount())
	}
	if _, err := os.Stat(filepath.Join(folder, "index.html")); !os.IsNotExist(err) {
		t.Errorf("propose_edit wrote the file before review (stat: %v)", err)
	}
}

// The category follows the refusal, never the wording.
func TestEditFailureCategories(t *testing.T) {
	home := fakeUserHome(t)
	s := builtinTestServer(t)
	for path, want := range map[string]string{
		"~/.bashrc": editFailPathRefused,
		filepath.Join(filepath.Dir(home), "x.txt"): editFailPathRefused,
		"missing.go": editFailRejected,
	} {
		raw, _ := json.Marshal(map[string]string{"path": path, "search": "x", "replace": "y"})
		res, _ := s.builtinProposeEdit(context.Background(), raw, &proposalSink{})
		if !res.IsError || !strings.Contains(res.Content, "FAILED ("+want+")") {
			t.Errorf("%s: %q, want category %s", path, res.Content, want)
		}
	}
}

// In the working copy too: the same refusal, the same category.
func TestProposeEditAtAFolderInTheWorkingCopyIsCategorised(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.go": "package a\n"})
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	sink := stagedSink(t, s)
	out, isErr := propose(t, s, sink, "adir", "", "x\n")
	if !isErr || !strings.Contains(out, editFailIsDirectory) || !strings.Contains(out, "adir/<name>") {
		t.Errorf("working copy: %q", out)
	}
	// Nothing changed and nothing ran: no "not checked" report under it.
	if _, info, _ := sink.finish(); info != nil {
		t.Errorf("a turn that changed nothing reported %+v", info)
	}
}

// errorOnlySSE is a stream that carries an error and nothing else, the shape
// OpenRouter sends when the upstream fails after the stream has begun.
func errorOnlySSE(code int, message string) []string {
	obj, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": message},
		"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "error"}}})
	return []string{"data: " + string(obj), "data: [DONE]"}
}

// AN ERROR IN AN OTHERWISE EMPTY STREAM IS A FAILED CALL, classified, with the
// provider's words in the log detail only.
//
// Neuter check: drop failedEmpty's return in streamCompletion and each case
// comes back as a nil error -- the "empty reply" the user saw.
func TestAnErrorOnlyStreamIsAClassifiedFailure(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		class ModelErrorClass
	}{
		{"rate limit", errorOnlySSE(429, "Rate limit exceeded: free-models-per-min"), ClassRateLimited},
		{"upstream 502", errorOnlySSE(502, "Provider returned error"), ClassUpstreamUnavailable},
		{"bare string", []string{`data: {"error":"overloaded"}`, `data: [DONE]`}, ClassUpstreamUnavailable},
		{"finish_reason only", []string{`data: {"choices":[{"delta":{},"finish_reason":"error"}]}`, `data: [DONE]`}, ClassUpstreamUnavailable},
		{"no [DONE]", errorOnlySSE(502, "x")[:1], ClassUpstreamUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := rawSSEServerFunc(t, func([]byte) []string { return tc.lines })
			_, err := streamCompletion(context.Background(), base, "k", "m", buildChatMessages("s", nil, "hi"), nil,
				ZDRConfig{}.resolvedProviderRouting(), func(string) error { return nil }, nil, nil, nil)
			var me *ModelError
			if !errors.As(err, &me) {
				t.Fatalf("err = %v, want a ModelError", err)
			}
			if me.Class != tc.class || !me.Retryable() {
				t.Errorf("class %s (retryable %v), want retryable %s", me.Class, me.Retryable(), tc.class)
			}
			if strings.Contains(me.Error(), "Rate limit exceeded") || strings.Contains(me.Error(), "Provider returned") {
				t.Errorf("the provider's own words reached the client message: %q", me.Error())
			}
		})
	}
}

// A null error is not an error.
func TestANullErrorFieldIsIgnored(t *testing.T) {
	base := rawSSEServerFunc(t, func([]byte) []string {
		return []string{`data: {"error":null,"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `data: [DONE]`}
	})
	if _, err := streamCompletion(context.Background(), base, "k", "m", buildChatMessages("s", nil, "hi"), nil,
		ZDRConfig{}.resolvedProviderRouting(), func(string) error { return nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// In the loop, an error-only stream is retried as a provider failure -- not
// answered with the note telling the model it was silent.
func TestAnErrorOnlyStreamIsRetriedNotTreatedAsEmpty(t *testing.T) {
	base, requests, bodies := agentUpstream(t, errorOnlySSE(429, "slow down"), textSSE("the answer"))
	s := loopServer(t, base, MCPConfig{Enabled: true})
	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != "the answer" || res.Incomplete != nil || requests.Load() != 2 {
		t.Fatalf("text %q, incomplete %+v, %d request(s)", res.FinalText, res.Incomplete, requests.Load())
	}
	retry := sentMessages(t, (*bodies)[1])
	if last := retry[len(retry)-1]; last.Content == emptyReplyNote {
		t.Error("the retry told the model its reply was empty; the provider failed, not the model")
	}
}

// Retries are bounded: an error on every attempt ends the turn after
// maxStreamAttempts calls, as a provider failure.
func TestAnErrorOnEveryAttemptEndsTheTurnBounded(t *testing.T) {
	var lines [][]string
	for i := 0; i < 6; i++ {
		lines = append(lines, errorOnlySSE(http.StatusBadGateway, "down"))
	}
	base, requests, _ := agentUpstream(t, lines...)
	s := loopServer(t, base, MCPConfig{Enabled: true})
	_, _, err := runLoop(t, s)
	var me *ModelError
	if !errors.As(err, &me) || me.Class != ClassUpstreamUnavailable {
		t.Fatalf("err = %v, want an upstream_unavailable ModelError", err)
	}
	if requests.Load() != maxStreamAttempts {
		t.Errorf("%d request(s), want %d", requests.Load(), maxStreamAttempts)
	}
}

// A tool-call-only reply (no text) is not empty, and empty replies after it
// never run the tool again: recovery repeats model calls, not tools.
func TestRecoveryFromEmptyRepliesNeverRepeatsATool(t *testing.T) {
	base, requests, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`),
		emptySSE(), emptySSE(), emptySSE())
	s := loopServer(t, base, MCPConfig{Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": PolicyAllow}}})
	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseSucceeded || a.Phase == protocol.ToolPhaseFailed {
			ran++
		}
	}
	if ran != 1 || requests.Load() != 4 {
		t.Errorf("the tool finished %d time(s) over %d request(s); want once over 4", ran, requests.Load())
	}
	if after := sentMessages(t, (*bodies)[1]); after[len(after)-1].Role != "tool" {
		t.Errorf("the tool-call-only reply was treated as empty: the next request ended with %+v", after[len(after)-1])
	}
	if res.Incomplete == nil || !strings.Contains(res.Incomplete.Detail, "empty answer") {
		t.Errorf("incomplete = %+v", res.Incomplete)
	}
}

// A turn that goes quiet after a failed tool says which tool failed and why,
// and that it changed nothing.
func TestEmptyRepliesAfterAFailedToolNameTheFailure(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__propose_edit", `{"path":"adir","search":"","replace":"x\n"}`),
		emptySSE(), emptySSE(), emptySSE())
	s := loopServer(t, base, MCPConfig{Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"propose_edit": PolicyAllow}}})
	if err := os.Mkdir(filepath.Join(s.workspace, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Incomplete == nil {
		t.Fatal("the turn was reported complete")
	}
	for _, want := range []string{"propose_edit: ", "is a directory", "nothing was changed"} {
		if !strings.Contains(res.Incomplete.Detail, want) {
			t.Errorf("detail %q does not say %q", res.Incomplete.Detail, want)
		}
	}
	// The chat row and the notice say the same reason.
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseFailed && !strings.Contains(res.Incomplete.Detail, a.Detail) {
			t.Errorf("row reason %q is not the notice's", a.Detail)
		}
	}
}
