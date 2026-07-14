package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

func newTestModel() chatModel {
	return newTestModelWithRoot("/workspace")
}

// newTestModelWithRoot is for edit-review tests, which need PrepareEdit to
// resolve against a real directory. Non-review tests use the placeholder
// path from newTestModel since PrepareEdit is never invoked unless a
// streamed answer actually contains edit-block markup.
func newTestModelWithRoot(root string) chatModel {
	m := newChatModel("test-client", root, root, nil)
	// Drive past the splash and give the model a size, exactly as the real
	// runtime would via its initial WindowSizeMsg, so viewport/input are
	// usable in assertions below.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(chatModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // dismiss splash
	m = updated.(chatModel)
	return m
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return real
}

func writeTempFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return full
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(data)
}

// editBlockText renders one SEARCH/REPLACE block exactly as the model is
// instructed to (daemon/prompts/system.txt), for use as synthetic streamed
// answer text in tests.
func editBlockText(path, search, replace string) string {
	return strings.Join([]string{
		"path: " + path,
		"<<<<<<< SEARCH",
		search,
		"=======",
		replace,
		">>>>>>> REPLACE",
	}, "\n")
}

func typeText(m chatModel, s string) chatModel {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(chatModel)
	}
	return m
}

func pressEnter(m chatModel) (chatModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(chatModel), cmd
}

func TestChat_SplashDismissedByAnyKey(t *testing.T) {
	m := newChatModel("test-client", "/workspace", "/workspace", nil)
	if m.state != stateSplash {
		t.Fatalf("state = %v, want stateSplash before any key", m.state)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = updated.(chatModel)
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle after a keypress dismisses the splash", m.state)
	}
}

func TestChat_EnterOnEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel()
	before := len(m.turns)

	m, _ = pressEnter(m)

	if len(m.turns) != before {
		t.Errorf("turns = %d, want unchanged %d after Enter on empty input", len(m.turns), before)
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle to remain unchanged", m.state)
	}
}

func TestChat_EnterWithTextStartsSending(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hello")

	m, cmd := pressEnter(m)

	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending", m.state)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (spinner tick + stream start) after Enter")
	}
	if len(m.turns) != 1 || m.turns[0].role != roleUser || m.turns[0].text != "hello" {
		t.Errorf("turns = %+v, want a single user turn with text %q", m.turns, "hello")
	}
	if m.input.Value() != "" {
		t.Errorf("input value = %q, want cleared after send", m.input.Value())
	}
	if m.input.Focused() {
		t.Error("input should be blurred (disabled) while a request is in flight")
	}
	if m.streamCancel == nil {
		t.Error("streamCancel should be set once a stream starts")
	}
}

func TestChat_TokenMsgAppendsToTranscriptAndStartsStreaming(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, cmd := m.Update(tokenMsg("Hel"))
	m = updated.(chatModel)
	if m.state != stateStreaming {
		t.Fatalf("state = %v, want stateStreaming after the first token", m.state)
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a token")
	}
	if len(m.turns) != 2 || m.turns[1].role != roleAssistant || m.turns[1].text != "Hel" {
		t.Fatalf("turns = %+v, want a second assistant turn with text %q", m.turns, "Hel")
	}

	updated, _ = m.Update(tokenMsg("lo"))
	m = updated.(chatModel)
	if m.turns[1].text != "Hello" {
		t.Errorf("assistant turn text = %q, want accumulated %q", m.turns[1].text, "Hello")
	}
}

func TestChat_GroundingMsgSetsLastGroundingAndKeepsWaiting(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	info := &protocol.GroundingInfo{Grounded: true, Chunks: 3, Workspace: "/workspace"}
	updated, cmd := m.Update(groundingMsg{info})
	m = updated.(chatModel)

	if m.lastGrounding != info {
		t.Errorf("lastGrounding = %+v, want %+v", m.lastGrounding, info)
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a groundingMsg (it's not terminal)")
	}
	if m.state != stateSending {
		t.Errorf("state = %v, want unchanged (groundingMsg arrives before any token)", m.state)
	}
}

func TestChat_GroundingLabelReflectsUngroundedReason(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: false, Reason: "no index found"}})
	m = updated.(chatModel)

	label := m.groundingLabel()
	if !strings.Contains(label, "ungrounded") || !strings.Contains(label, "no index found") {
		t.Errorf("groundingLabel = %q, want it to mention ungrounded and the reason", label)
	}
}

func TestChat_GroundingLabelFlagsWorkspaceMismatch(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, WorkspaceMismatch: true, Workspace: "/other/repo"}})
	m = updated.(chatModel)

	label := m.groundingLabel()
	if !strings.Contains(label, "/other/repo") {
		t.Errorf("groundingLabel = %q, want it to name the daemon's actual workspace on a mismatch", label)
	}
}

func TestChat_GroundingClearsOnNewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, Chunks: 2}})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.lastGrounding == nil {
		t.Fatal("precondition failed: expected lastGrounding to be set after the first turn")
	}

	m = typeText(m, "second")
	m, _ = pressEnter(m)

	if m.lastGrounding != nil {
		t.Error("lastGrounding should be cleared at the start of a new turn, not carried over from the previous one")
	}
}

func TestChat_NoGroundingLabelBeforeFirstReport(t *testing.T) {
	m := newTestModel()
	if label := m.groundingLabel(); label != "" {
		t.Errorf("groundingLabel = %q, want empty before any grounding info has arrived", label)
	}
}

func TestChat_RedactionsMsgSetsLastRedactionsAndKeepsWaiting(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	kinds := []string{"openai_key"}
	updated, cmd := m.Update(redactionsMsg{kinds})
	m = updated.(chatModel)

	if len(m.lastRedactions) != 1 || m.lastRedactions[0] != "openai_key" {
		t.Errorf("lastRedactions = %v, want %v", m.lastRedactions, kinds)
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a redactionsMsg (it's not terminal)")
	}
	if m.state != stateSending {
		t.Errorf("state = %v, want unchanged (redactionsMsg arrives before any token)", m.state)
	}
}

func TestChat_RedactionsLabelListsKinds(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(redactionsMsg{[]string{"openai_key", "aws_access_key"}})
	m = updated.(chatModel)

	label := m.redactionsLabel()
	if !strings.Contains(label, "openai_key") || !strings.Contains(label, "aws_access_key") {
		t.Errorf("redactionsLabel = %q, want it to mention both kinds", label)
	}
	if !strings.Contains(label, "2") {
		t.Errorf("redactionsLabel = %q, want it to mention the count (2)", label)
	}
}

func TestChat_RedactionsClearsOnNewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(redactionsMsg{[]string{"openai_key"}})
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.lastRedactions == nil {
		t.Fatal("precondition failed: expected lastRedactions to be set after the first turn")
	}

	m = typeText(m, "second")
	m, _ = pressEnter(m)

	if m.lastRedactions != nil {
		t.Error("lastRedactions should be cleared at the start of a new turn, not carried over from the previous one")
	}
}

func TestChat_NoRedactionsLabelBeforeFirstReport(t *testing.T) {
	m := newTestModel()
	if label := m.redactionsLabel(); label != "" {
		t.Errorf("redactionsLabel = %q, want empty before any redactions message has arrived", label)
	}
}

// --- regression: grounding + redactions concurrently active (bug found in
// live testing after commit 36e4c53 -- a workspace-mismatch warning and a
// redaction notice used to be joined onto ONE header line via
// strings.Join, silently overflowing the terminal width and desyncing the
// hardcoded 1-row header assumption the viewport's height was computed
// from. The redaction notice would disappear entirely with no visible
// sign anything was wrong.) ---------------------------------------------

func TestChat_HeaderShowsBothGroundingAndRedactionsOnSeparateLines(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "here is my key: sk-FAKETESTKEY1234567890abcdef")
	m, _ = pressEnter(m)

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, WorkspaceMismatch: true, Workspace: "/other/repo"}})
	m = updated.(chatModel)
	updated, _ = m.Update(redactionsMsg{[]string{"openai_key"}})
	m = updated.(chatModel)

	header := m.renderHeader()
	lines := strings.Split(header, "\n")
	if len(lines) != 3 {
		t.Fatalf("renderHeader produced %d line(s): %q, want 3 (brand/state line + grounding + redactions, each on its own line)", len(lines), header)
	}
	if !strings.Contains(lines[1], "/other/repo") {
		t.Errorf("line 1 = %q, want the grounding/workspace-mismatch notice", lines[1])
	}
	if !strings.Contains(lines[2], "openai_key") {
		t.Errorf("line 2 = %q, want the redactions notice", lines[2])
	}
	// The actual bug: both notices must be genuinely present in the
	// rendered output at once, not just structurally separate strings that
	// never both get returned.
	if !strings.Contains(header, "/other/repo") || !strings.Contains(header, "openai_key") {
		t.Fatalf("renderHeader = %q, want BOTH the grounding and redactions notices present simultaneously", header)
	}
}

func TestChat_HeaderLineCountTracksActiveNotices(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	if got := m.headerLineCount(); got != 1 {
		t.Errorf("headerLineCount = %d, want 1 with no notices active", got)
	}

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, WorkspaceMismatch: true, Workspace: "/other/repo"}})
	m = updated.(chatModel)
	if got := m.headerLineCount(); got != 2 {
		t.Errorf("headerLineCount = %d, want 2 with only grounding active", got)
	}

	updated, _ = m.Update(redactionsMsg{[]string{"openai_key"}})
	m = updated.(chatModel)
	if got := m.headerLineCount(); got != 3 {
		t.Errorf("headerLineCount = %d, want 3 with both grounding and redactions active", got)
	}
}

// TestChat_ViewportShrinksWhenNoticeLinesGrow is the other half of the
// regression: headerLineCount growing is only useful if resizeViewport
// actually consumes it. Before the fix, the viewport's height was computed
// once from a hardcoded headerLines=1 and never revisited, so it never
// shrank to make room for extra notice lines -- which is exactly what let
// a second notice line silently overdraw/get overdrawn.
func TestChat_ViewportShrinksWhenNoticeLinesGrow(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	baseline := m.viewport.Height

	updated, _ := m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, WorkspaceMismatch: true, Workspace: "/other/repo"}})
	m = updated.(chatModel)
	updated, _ = m.Update(redactionsMsg{[]string{"openai_key"}})
	m = updated.(chatModel)

	if m.viewport.Height != baseline-2 {
		t.Errorf("viewport.Height = %d, want %d (baseline %d minus 2 new notice lines)", m.viewport.Height, baseline-2, baseline)
	}
}

// TestChat_NoticeLineTruncatesRatherThanDisappearing covers the case where
// even a single notice line is too long for a narrow terminal: it must be
// visibly truncated (with a "…" tail), never silently dropped, and must
// never cause renderHeader to produce more lines than headerLineCount
// expects (which would desync resizeViewport all over again).
func TestChat_NoticeLineTruncatesRatherThanDisappearing(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	m = updated.(chatModel)
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ = m.Update(groundingMsg{&protocol.GroundingInfo{Grounded: true, WorkspaceMismatch: true, Workspace: "/a/very/long/workspace/path/that/will/not/fit"}})
	m = updated.(chatModel)

	lines := m.noticeLines()
	if len(lines) != 1 {
		t.Fatalf("noticeLines = %v, want exactly 1 line", lines)
	}
	if !strings.Contains(lines[0], "…") {
		t.Errorf("noticeLines[0] = %q, want it truncated with a %q tail at width 20", lines[0], "…")
	}
	if lines[0] == "" {
		t.Error("noticeLines[0] is empty, want the notice still visibly present (truncated), not silently dropped")
	}
}

func TestChat_StreamDoneReenablesInput(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("hi there"))
	m = updated.(chatModel)

	updated, cmd := m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle after streamDoneMsg", m.state)
	}
	if m.streamCancel != nil {
		t.Error("streamCancel should be cleared once the stream is done")
	}
	if m.streamCh != nil {
		t.Error("streamCh should be cleared once the stream is done")
	}
	if cmd == nil {
		t.Error("expected a Cmd to re-focus the input (Focus() returns a blink Cmd)")
	}
}

func TestChat_StreamErrShowsErrorStateAndReenablesInput(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(streamErrMsg{errors.New("boom")})
	m = updated.(chatModel)

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if m.statusErr != "boom" {
		t.Errorf("statusErr = %q, want %q", m.statusErr, "boom")
	}
	if m.streamCancel != nil {
		t.Error("streamCancel should be cleared after an error")
	}
}

func TestChat_EnterAfterErrorStartsANewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamErrMsg{errors.New("boom")})
	m = updated.(chatModel)

	m = typeText(m, "second")
	m, cmd := pressEnter(m)

	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending; Enter after an error should be able to start a new turn", m.state)
	}
	if cmd == nil {
		t.Fatal("expected a Cmd starting the new stream")
	}
	if len(m.turns) != 2 || m.turns[1].text != "second" {
		t.Errorf("turns = %+v, want the second prompt appended", m.turns)
	}
}

func TestChat_EnterWhileSendingOrStreamingIsANoOp(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	if m.state != stateSending {
		t.Fatalf("precondition failed: state = %v, want stateSending", m.state)
	}

	before := len(m.turns)
	m = typeText(m, "second") // typing while sending should also be ignored (input is blurred)
	m, cmd := pressEnter(m)

	if len(m.turns) != before {
		t.Errorf("turns = %d, want unchanged %d — Enter while busy must be a no-op", len(m.turns), before)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd for a no-op Enter while busy")
	}
}

// TestChat_CtrlCCancelsAndQuits is the mid-stream-quit safety net: it
// confirms Ctrl-C actually invokes streamCancel (not just that it returns
// tea.Quit), which is the mechanism that unblocks streamPrompt's blocked
// read (see TestStreamPrompt_ContextCancelUnblocksBlockedRead in
// stream_test.go for the full end-to-end proof).
func TestChat_CtrlCCancelsAndQuits(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(chatModel)

	if ctx.Err() == nil {
		t.Error("expected streamCancel to have been called (context should be Done)")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (tea.Quit)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected the returned Cmd to produce a tea.QuitMsg")
	}
}

func TestChat_TokenAfterStreamAbandonedIsIgnored(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamDoneMsg{}) // stream ends; streamCh cleared
	m = updated.(chatModel)

	before := len(m.turns)
	updated, cmd := m.Update(tokenMsg("stray"))
	m = updated.(chatModel)

	if len(m.turns) != before {
		t.Errorf("turns = %+v, want unchanged after a stray token from an abandoned stream", m.turns)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd for a stray token (no waitForNext re-issued)")
	}
}

func lastSystemText(turns []turn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == roleSystem {
			return turns[i].text
		}
	}
	return ""
}

func TestChat_ValidEditBlockEntersReviewState(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "please fix it")
	m, _ = pressEnter(m)

	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func new_() {}")))
	m = updated.(chatModel)
	updated, cmd := m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateEditReview {
		t.Fatalf("state = %v, want stateEditReview", m.state)
	}
	if m.reviewPrepared == nil || m.reviewPrepared.Block.FilePath != "foo.go" {
		t.Fatalf("reviewPrepared = %+v, want a prepared edit for foo.go", m.reviewPrepared)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd while awaiting a review confirm keypress (not a blocking read)")
	}
}

func TestChat_PlainAnswerWithNoEditBlocksStaysIdle(t *testing.T) {
	m := newTestModelWithRoot("/workspace")
	m = typeText(m, "what does this do")
	m, _ = pressEnter(m)

	updated, _ := m.Update(tokenMsg("It's a normal answer with no edit blocks."))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle unchanged when the answer has no edit blocks", m.state)
	}
}

func TestChat_ReviewApplyWritesFileAndCLICompatibleBackup(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func new_() {}")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)
	if m.state != stateEditReview {
		t.Fatalf("precondition failed: state = %v", m.state)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after the only block is applied", m.state)
	}
	got := readFileString(t, filepath.Join(root, "foo.go"))
	if !strings.Contains(got, "func new_() {}") {
		t.Errorf("foo.go = %q, want the edit applied", got)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "1 applied, 0 skipped, 0 refused") {
		t.Errorf("summary = %q, want 1 applied", summary)
	}

	// Backup compatibility: the same .codeterminal/backups/<session>/{before,after}
	// layout the CLI's applyEditBlocks writes, so `edits undo` can restore it.
	entries, err := os.ReadDir(filepath.Join(root, ".codeterminal", "backups"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one backup session dir, got %v (err=%v)", entries, err)
	}
	sessionDir := filepath.Join(root, ".codeterminal", "backups", entries[0].Name())
	before := readFileString(t, filepath.Join(sessionDir, "before", "foo.go"))
	if before != original {
		t.Errorf("backup before/foo.go = %q, want the pre-edit original %q", before, original)
	}
	after := readFileString(t, filepath.Join(sessionDir, "after", "foo.go"))
	if !strings.Contains(after, "func new_() {}") {
		t.Errorf("backup after/foo.go = %q, want it to contain the applied edit", after)
	}
}

func TestChat_ReviewSkipDoesNotApply(t *testing.T) {
	root := realTempDir(t)
	original := "package main\n\nfunc old() {}\n"
	writeTempFile(t, root, "foo.go", original)
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func new_() {}")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle", m.state)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != original {
		t.Errorf("foo.go was modified despite skipping: %q", got)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "0 applied, 1 skipped, 0 refused") {
		t.Errorf("summary = %q, want 0 applied, 1 skipped", summary)
	}
}

func TestChat_ReviewCancelRemainingSkipsAllPendingBlocks(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "a.go", "package main\n\nfunc a() {}\n")
	writeTempFile(t, root, "b.go", "package main\n\nfunc b() {}\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix both")
	m, _ = pressEnter(m)

	text := editBlockText("a.go", "func a() {}", "func a2() {}") + "\n" + editBlockText("b.go", "func b() {}", "func b2() {}")
	updated, _ := m.Update(tokenMsg(text))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)
	if m.state != stateEditReview || m.reviewPrepared.Block.FilePath != "a.go" {
		t.Fatalf("precondition failed: state=%v prepared=%+v", m.state, m.reviewPrepared)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after cancelling remaining edits", m.state)
	}
	if got := readFileString(t, filepath.Join(root, "a.go")); !strings.Contains(got, "func a() {}") {
		t.Errorf("a.go = %q, want unchanged (cancelled)", got)
	}
	if got := readFileString(t, filepath.Join(root, "b.go")); !strings.Contains(got, "func b() {}") {
		t.Errorf("b.go = %q, want unchanged (cancelled)", got)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "0 applied, 2 skipped, 0 refused") {
		t.Errorf("summary = %q, want both blocks counted as skipped", summary)
	}
}

func TestChat_ReviewSummaryReflectsMixedOutcomes(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "a.go", "package main\n\nfunc a() {}\n")
	writeTempFile(t, root, "b.go", "package main\n\nfunc b() {}\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)

	text := strings.Join([]string{
		editBlockText("a.go", "func a() {}", "func a2() {}"), // will apply
		editBlockText("b.go", "func b() {}", "func b2() {}"), // will skip
		editBlockText("a.go", "func missing() {}", "x"),      // will auto-refuse (not found)
	}, "\n")
	updated, _ := m.Update(tokenMsg(text))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}) // apply a.go's first block
	m = updated.(chatModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}) // skip b.go
	m = updated.(chatModel)
	// The third block refuses automatically inside advanceReview — no keypress needed.

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after all 3 blocks are processed", m.state)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "1 applied, 1 skipped, 1 refused") {
		t.Errorf("summary = %q, want 1 applied, 1 skipped, 1 refused", summary)
	}
	if got := readFileString(t, filepath.Join(root, "a.go")); !strings.Contains(got, "func a2() {}") {
		t.Errorf("a.go = %q, want the accepted edit applied", got)
	}
	if got := readFileString(t, filepath.Join(root, "b.go")); !strings.Contains(got, "func b() {}") {
		t.Errorf("b.go = %q, want the skipped edit left alone", got)
	}
}

// The following tests assert that refusals in the TUI's review flow come
// from calling the exact same editapply.PrepareEdit the CLI calls — not a
// second, weaker check reimplemented for the TUI. Each mirrors one of the
// CLI's own refusal tests (daemon/apply_cmd_test.go /
// editapply/apply_test.go) and, critically, never shows a confirm prompt
// for a refused block (it's auto-skipped, exactly like applyEditBlocks).

func TestChat_ReviewRefusesAmbiguousSearchIdenticallyToCLI(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "x := 1\nx := 1\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "x := 1", "x := 2")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle — an ambiguous block auto-refuses with no confirm prompt", m.state)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "0 applied, 0 skipped, 1 refused") || !strings.Contains(summary, "ambiguous") {
		t.Errorf("summary = %q, want 1 refused mentioning ambiguity", summary)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); got != "x := 1\nx := 1\n" {
		t.Errorf("foo.go was modified despite the ambiguous refusal: %q", got)
	}
}

func TestChat_ReviewRefusesSecretFileIdenticallyToCLI(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, ".env", "SECRET=1\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText(".env", "SECRET=1", "SECRET=2")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle", m.state)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "1 refused") || !strings.Contains(summary, "secret") {
		t.Errorf("summary = %q, want a refusal mentioning the secret-file rule", summary)
	}
	if got := readFileString(t, filepath.Join(root, ".env")); got != "SECRET=1\n" {
		t.Errorf(".env was modified despite the secret-file refusal: %q", got)
	}
}

func TestChat_ReviewRefusesOutsideWorkspaceIdenticallyToCLI(t *testing.T) {
	root := realTempDir(t)
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("../outside.txt", "a", "b")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle", m.state)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "1 refused") {
		t.Errorf("summary = %q, want 1 refused", summary)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside.txt")); err == nil {
		t.Error("outside.txt was created outside the workspace root")
	}
}

func TestChat_ReviewRefusesSyntaxBreakingGoEditIdenticallyToCLI(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func broken( {")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle", m.state)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "unparseable") {
		t.Errorf("summary = %q, want it to mention unparseable Go", summary)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); !strings.Contains(got, "func old() {}") {
		t.Errorf("foo.go was modified despite the syntax-breaking refusal: %q", got)
	}
}

// --- buildHistory ------------------------------------------------------

func TestBuildHistory_EmptyTranscriptIsEmpty(t *testing.T) {
	if h := buildHistory(nil); len(h) != 0 {
		t.Errorf("buildHistory(nil) = %+v, want empty", h)
	}
	if h := buildHistory([]turn{}); len(h) != 0 {
		t.Errorf("buildHistory([]turn{}) = %+v, want empty", h)
	}
}

func TestBuildHistory_OrdersUserAndAssistantOldestFirst(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "q1"},
		{role: roleAssistant, text: "a1"},
		{role: roleUser, text: "q2"},
		{role: roleAssistant, text: "a2"},
	}
	got := buildHistory(turns)
	want := []protocol.Turn{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("turn %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestBuildHistory_SkipsSystemTurns proves edit-review summaries and parse-
// error notices (roleSystem) are TUI-only chrome, not sent as conversation
// history — the protocol only has "user"/"assistant" roles, and the daemon
// would drop anything else anyway (daemon/history.go).
func TestBuildHistory_SkipsSystemTurns(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "q1"},
		{role: roleAssistant, text: "a1"},
		{role: roleSystem, text: "edits: 1 applied, 0 skipped, 0 refused"},
		{role: roleUser, text: "q2"},
	}
	got := buildHistory(turns)
	if len(got) != 3 {
		t.Fatalf("got %d turns, want 3 (system turn dropped): %+v", len(got), got)
	}
	for _, h := range got {
		if h.Role != "user" && h.Role != "assistant" {
			t.Errorf("unexpected role in history: %+v", h)
		}
	}
}

// TestBuildHistory_ExcludesNotYetAppendedCurrentPrompt mirrors exactly how
// startTurn calls buildHistory: on the transcript as it stands BEFORE the
// new prompt is appended. This is what guarantees a not-yet-answered prompt
// never ends up inside its own History.
func TestBuildHistory_ExcludesNotYetAppendedCurrentPrompt(t *testing.T) {
	priorTurns := []turn{
		{role: roleUser, text: "first question"},
		{role: roleAssistant, text: "first answer"},
	}
	got := buildHistory(priorTurns)
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2: %+v", len(got), got)
	}
	for _, h := range got {
		if h.Content == "second question" {
			t.Fatal("the not-yet-appended current prompt must never appear in History")
		}
	}
}

// TestBuildHistory_LongTranscriptIsNotCappedClientSide proves the TUI does
// no capping of its own: Layer 1 (daemon/history.go's maxHistoryTurns)
// already owns that policy server-side, so a long session just sends its
// whole transcript.
func TestBuildHistory_LongTranscriptIsNotCappedClientSide(t *testing.T) {
	const n = 50
	turns := make([]turn, n)
	for i := range turns {
		role := roleUser
		if i%2 == 1 {
			role = roleAssistant
		}
		turns[i] = turn{role: role, text: fmt.Sprintf("turn-%d", i)}
	}
	got := buildHistory(turns)
	if len(got) != n {
		t.Errorf("got %d turns, want all %d (no client-side cap)", len(got), n)
	}
}

// --- ctrl+n: clear conversation -----------------------------------------

func TestChat_CtrlNClearsTranscript(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hello")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("hi there"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if len(m.turns) == 0 {
		t.Fatal("expected turns to be populated after a completed exchange")
	}
	m.lastGrounding = &protocol.GroundingInfo{Grounded: true, Chunks: 3}
	m.lastRedactions = []string{"openai_key"}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(chatModel)

	if len(m.turns) != 0 {
		t.Errorf("turns = %+v, want empty after ctrl+n", m.turns)
	}
	if m.lastGrounding != nil {
		t.Error("lastGrounding should be cleared after ctrl+n")
	}
	if m.lastRedactions != nil {
		t.Error("lastRedactions should be cleared after ctrl+n")
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle after ctrl+n", m.state)
	}
	if len(buildHistory(m.turns)) != 0 {
		t.Error("buildHistory(m.turns) should be empty right after clearing — next prompt sends no History")
	}
}

func TestChat_CtrlNIsNoOpWhileSendingOrStreaming(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hello")
	m, _ = pressEnter(m) // state = stateSending
	before := len(m.turns)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(chatModel)
	if len(m.turns) != before {
		t.Errorf("turns = %+v, want unchanged (ctrl+n is a no-op while sending)", m.turns)
	}
	if m.state != stateSending {
		t.Errorf("state = %v, want stateSending unchanged", m.state)
	}

	updated, _ = m.Update(tokenMsg("partial")) // now stateStreaming
	m = updated.(chatModel)
	before = len(m.turns)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(chatModel)
	if len(m.turns) != before {
		t.Errorf("turns = %+v, want unchanged (ctrl+n is a no-op while streaming)", m.turns)
	}
}

// --- cross-session hydration (newChatModel's initialHistory) ------------

func TestNewChatModel_HydratesTurnsFromInitialHistory(t *testing.T) {
	initial := []protocol.Turn{
		{Role: "user", Content: "what is a goroutine?"},
		{Role: "assistant", Content: "a lightweight thread"},
	}
	m := newChatModel("test-client", "/workspace", "/workspace", initial)

	if len(m.turns) != 2 {
		t.Fatalf("got %d turns, want 2 hydrated from initialHistory: %+v", len(m.turns), m.turns)
	}
	if m.turns[0].role != roleUser || m.turns[0].text != "what is a goroutine?" {
		t.Errorf("turn 0 = %+v, want the hydrated user turn", m.turns[0])
	}
	if m.turns[1].role != roleAssistant || m.turns[1].text != "a lightweight thread" {
		t.Errorf("turn 1 = %+v, want the hydrated assistant turn", m.turns[1])
	}
	if got := m.historyLabel(); got == "" {
		t.Error("historyLabel() empty, want it to reflect the hydrated turns immediately, before any prompt is sent")
	}
}

// TestNewChatModel_DropsInvalidRolesInInitialHistory is defensive-in-depth:
// the daemon has already re-validated persisted turns (daemon/memory.go's
// LoadRecentTurns), but turnsFromProtocol still only ever maps exactly
// "user"/"assistant" rather than trusting the daemon blindly a second time.
func TestNewChatModel_DropsInvalidRolesInInitialHistory(t *testing.T) {
	initial := []protocol.Turn{
		{Role: "user", Content: "real"},
		{Role: "system", Content: "should never survive hydration"},
	}
	m := newChatModel("test-client", "/workspace", "/workspace", initial)
	if len(m.turns) != 1 {
		t.Fatalf("got %d turns, want 1 (system-role entry dropped): %+v", len(m.turns), m.turns)
	}
	if m.turns[0].role != roleUser {
		t.Errorf("turn 0 = %+v, want the real user turn", m.turns[0])
	}
}

// --- ctrl+n's daemon-side (async) half -----------------------------------

func TestChat_ResetErrMsgAppendsSystemNoteWithoutChangingState(t *testing.T) {
	m := newTestModel()
	before := m.state

	updated, cmd := m.Update(resetErrMsg{err: errors.New("daemon unreachable")})
	m = updated.(chatModel)

	if cmd != nil {
		t.Error("expected no further Cmd after a resetErrMsg")
	}
	if m.state != before {
		t.Errorf("state = %v, want unchanged %v", m.state, before)
	}
	note := lastSystemText(m.turns)
	if !strings.Contains(note, "daemon unreachable") {
		t.Errorf("system note = %q, want it to mention the failure", note)
	}
}

func TestChat_CtrlNReturnsNonNilCmdForBackgroundReset(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hello")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("hi"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = updated.(chatModel)
	if len(m.turns) != 0 {
		t.Fatalf("turns = %+v, want cleared immediately", m.turns)
	}
	if cmd == nil {
		t.Error("expected a non-nil Cmd to fire the background daemon-side reset")
	}
}

// TestChat_ThreeQuestionsInOneSessionMemReachesSixNotMore pins the
// invariant that a live session's own turn accumulation is never inflated
// by re-hydration (see protocol.HandshakeResponse.PersistedHistory's doc
// comment: only runChat's one-time preflight connection may hydrate from
// it; the streaming path must never touch it). After 3 Q&A exchanges in one
// continuous session, mem:N (len(buildHistory(m.turns))) must be exactly 6
// (3 user + 3 assistant), never more.
func TestChat_ThreeQuestionsInOneSessionMemReachesSixNotMore(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 3; i++ {
		m = typeText(m, fmt.Sprintf("question %d", i))
		m, _ = pressEnter(m)
		updated, _ := m.Update(tokenMsg(fmt.Sprintf("answer %d", i)))
		m = updated.(chatModel)
		updated, _ = m.Update(streamDoneMsg{})
		m = updated.(chatModel)
	}

	if got := len(buildHistory(m.turns)); got != 6 {
		t.Fatalf("mem count = %d, want exactly 6 after 3 exchanges (3 user + 3 assistant)", got)
	}
}
