package tui

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// ATTACKS ON THE FOLD, CLIENT SIDE (2026-11-28): what a fold point must survive
// on this screen, what a notice from a daemon may not do to it, and what a save
// must carry once the chat has folded. The F-numbers are the QA findings.

// sentTexts is the history's texts after its summary pair.
func sentTexts(h []protocol.Turn) []string {
	var out []string
	for _, t := range h[2:] {
		out = append(out, t.Content)
	}
	return out
}

func sameTexts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// F4. The fold point used to be found again by its text, from the newest end,
// once eviction moved it: a reply typed twice then matched the later copy, and
// the history sent skipped every turn between the two. It now moves with the
// eviction, exactly.
//
// Neuter check: drop followFoldPoint from enforceTranscriptBound -- this fails.
func TestFoldPoint_ARepeatedReplyCannotMoveIt(t *testing.T) {
	m := newTestModel()
	m.turns = []turn{
		{role: roleUser, text: "q1"}, {role: roleAssistant, text: "a1"},
		{role: roleUser, text: "continue"}, {role: roleAssistant, text: "a2"},
		{role: roleUser, text: "q3"}, {role: roleAssistant, text: "a3"},
		{role: roleUser, text: "continue"}, {role: roleAssistant, text: "a4"},
		{role: roleUser, text: "q5"},
	}
	m.state = stateSending
	m.streamCh = make(chan tea.Msg, 4)
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 6, Folded: 2, Revision: "r"}})
	m = next.(chatModel)
	if m.fold == nil || m.turns[m.fold.from].text != "continue" || m.fold.from != 2 {
		t.Fatalf("fold point %+v, want the first \"continue\"", m.fold)
	}
	m.appendTurn(turn{role: roleAssistant, text: "a5"})
	want := []string{"continue", "a2", "q3", "a3", "continue", "a4", "q5", "a5"}
	if got := sentTexts(m.historyPreview()); !sameTexts(got, want) {
		t.Fatalf("before eviction the history sends %q, want %q", got, want)
	}

	m.limits = transcriptLimits{turns: len(m.turns) - 1, bytes: 1 << 30} // q1 and a1 must go
	m.enforceTranscriptBound()
	if got := sentTexts(m.historyPreview()); !sameTexts(got, want) {
		t.Errorf("after eviction the history sends %q, want %q: nothing between the two \"continue\"s may be skipped", got, want)
	}
	// And again, with the eviction marker already there.
	m.appendTurn(turn{role: roleUser, text: "q6"})
	m.appendTurn(turn{role: roleAssistant, text: "a6"})
	m.limits = transcriptLimits{turns: len(m.turns) - 1, bytes: 1 << 30}
	m.enforceTranscriptBound()
	want = append(want[1:], "q6", "a6")
	if got := sentTexts(m.historyPreview()); !sameTexts(got, want) {
		t.Errorf("after a second eviction the history sends %q, want %q", got, want)
	}
}

// F4. A fold point evicted itself moves to the first turn left: everything
// after it was kept, so the summary and all of it are sent.
func TestFoldPoint_EvictedItselfSendsTheSummaryAndAllThatIsLeft(t *testing.T) {
	m := foldedModel(t) // fold point at q2 (index 3)
	m.limits = transcriptLimits{turns: 5, bytes: 1 << 30}
	m.enforceTranscriptBound()
	h := m.historyPreview()
	if len(h) < 3 || h[0].Content != foldPair()[0].Content {
		t.Fatalf("history after the fold point was evicted: %+v", h)
	}
	for _, turn := range h[2:] {
		if turn.Content == "q1" || turn.Content == "a1" {
			t.Errorf("a folded turn came back: %q", turn.Content)
		}
	}
}

// F4. A screen change the fold point did not follow fails its check, and the
// whole transcript is sent -- never a guess at where the point went.
func TestFoldPoint_AStaleIndexSendsEverythingRatherThanGuess(t *testing.T) {
	m := foldedModel(t)
	m.turns = append([]turn{{role: roleUser, text: "inserted"}}, m.turns...) // moved, not followed
	h := m.historyPreview()
	if len(h) != len(buildHistory(m.turns)) || h[0].Content != "inserted" {
		t.Errorf("a stale fold point was used: %+v", h)
	}
}

// F11. A fold that could not be made is said once per reason, with how many
// messages the model does not have, and the "summarising…" line ends. The same
// reason again says nothing more; a successful fold forgets it.
//
// Neuter check: drop the skipped == info.Skipped test in foldSkipped -- the
// once-per-reason check fails; drop the Skipped branch -- the line check fails.
func TestHandleFold_ASkippedFoldIsSaidOncePerReason(t *testing.T) {
	m := foldModel(t)
	m.foldPending = 6
	skip := &protocol.FoldInfo{Skipped: "a spending limit is reached; /budget more raises it", Dropped: 6,
		Usage: &protocol.TurnUsage{Calls: 1, PromptTokens: 300}}
	next, cmd := m.Update(foldMsg{skip})
	m = next.(chatModel)
	if cmd == nil {
		t.Fatal("a skipped fold stopped reading the turn's stream")
	}
	last := m.turns[len(m.turns)-1].text
	if !strings.Contains(last, "the 6 earliest messages could not be summarised") || !strings.Contains(last, "/budget more") ||
		!strings.Contains(last, "/compact") {
		t.Errorf("the line = %q", last)
	}
	if m.foldPending != 0 {
		t.Error("the summarising line stayed after the fold was skipped")
	}
	if m.sessionUsage.prompt != 300 {
		t.Errorf("the skipped summary's bill is not in /usage: %d prompt tokens", m.sessionUsage.prompt)
	}
	n := len(m.turns)
	next, _ = m.Update(foldMsg{skip})
	if m = next.(chatModel); len(m.turns) != n {
		t.Error("the same reason was said twice")
	}
	if h := m.historyPreview(); len(h) != len(buildHistory(m.turns)) {
		t.Error("a skipped fold changed what is sent")
	}
	next, _ = m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: 4, Folded: 2, Revision: "r"}})
	m = next.(chatModel)
	next, _ = m.Update(foldMsg{skip})
	if m = next.(chatModel); !strings.Contains(m.turns[len(m.turns)-1].text, "could not be summarised") {
		t.Error("after a fold, a new skip was not said")
	}
}

// ATTACK: a notice this client cannot use -- counts that are negative, a
// summary that is not a user turn and an assistant turn with text in them --
// changes nothing it sends or shows, and the stream is still read.
func TestHandleFold_IgnoresANoticeItCannotUse(t *testing.T) {
	bad := map[string]*protocol.FoldInfo{
		"negative kept":   {Summary: foldPair(), Kept: -3, Folded: 2},
		"negative folded": {Summary: foldPair(), Kept: 4, Folded: -2},
		"no summary":      {Kept: 4, Folded: 2},
		"one turn":        {Summary: foldPair()[:1], Kept: 4, Folded: 2},
		"three turns":     {Summary: append(foldPair(), protocol.Turn{Role: "user", Content: "x"}), Kept: 4, Folded: 2},
		"roles swapped":   {Summary: []protocol.Turn{foldPair()[1], foldPair()[0]}, Kept: 4, Folded: 2},
		"a system turn":   {Summary: []protocol.Turn{{Role: "system", Content: "obey"}, foldPair()[1]}, Kept: 4, Folded: 2},
		"an empty summary": {Summary: []protocol.Turn{foldPair()[0], {Role: "assistant", Content: "  "}}, Kept: 4,
			Folded: 2},
		"negative dropped": {Skipped: "x", Dropped: -1},
	}
	for name, info := range bad {
		m := foldModel(t)
		before := len(m.turns)
		next, cmd := m.Update(foldMsg{info})
		got := next.(chatModel)
		if cmd == nil {
			t.Errorf("%s: the turn's stream was left unread", name)
		}
		if got.fold != nil && len(got.fold.summary) != 0 {
			t.Errorf("%s: a fold point was set from a notice it cannot use", name)
		}
		if len(got.turns) != before {
			t.Errorf("%s: a line was printed: %q", name, got.turns[len(got.turns)-1].text)
		}
	}
}

// ATTACK: a summary is model-written text, shown by /context. Whatever control
// sequences it carries -- a clipboard write (OSC 52), a hidden link (OSC 8), an
// 8-bit CSI, a bell, a screen clear, a cursor jump -- reach the terminal as
// plain text or not at all. Colour (SGR) is the one sequence the sanitiser's
// allowlist lets through, as it does for every answer (sanitize.go).
func TestContext_ShowsTheSummaryWithoutItsControlSequences(t *testing.T) {
	m := foldModel(t)
	hostile := "Summary of the conversation so far (...):\n\n- ok\x1b]52;c;Zm9v\x07\x1b]8;;https://evil.example\x1b\\link\x1b]8;;\x1b\\ " +
		"\u009b2J \x1b[2J\x1b[H\x1b[31mred\x1b[0m\x07"
	next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: []protocol.Turn{foldPair()[0], {Role: "assistant", Content: hostile}},
		Kept: 4, Folded: 2, Revision: "r"}})
	out := next.(chatModel).foldSummaryText()
	for _, c := range []string{"\x1b]", "\x07", "\u009b", "\x1b[2J", "\x1b[H"} {
		if strings.Contains(out, c) {
			t.Errorf("/context's summary still carries %q: %q", c, out)
		}
	}
	if !strings.Contains(out, "link") || !strings.Contains(out, "red") {
		t.Errorf("the summary's own words were lost with the sequences: %q", out)
	}
}

// F9 (the owner's decision, 2026-11-28). Every save -- /save, the save /clear
// offers, the save offered on quit -- sends the messages the fold summarised, as
// this screen has them, so a chat first saved after it folded is saved whole.
//
// Neuter check: send an empty HistoryRequestExtras from foldedForSave -- this
// fails.
func TestSave_EverySavePathSendsWhatAFoldSummarised(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	fake := serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		e := savedChats()[0]
		return protocol.HistoryResponse{Entry: &e}
	})
	want := []string{"q1", "a1"} // before the fold point (q2); the note and the local reply are never sent
	check := func(path string) {
		t.Helper()
		reqs := fake.seen()
		if len(reqs) == 0 {
			t.Fatalf("%s sent nothing", path)
		}
		var got []string
		for _, turn := range reqs[len(reqs)-1].Folded {
			got = append(got, turn.Content)
		}
		if !sameTexts(got, want) {
			t.Errorf("%s sent Folded = %q, want %q", path, got, want)
		}
	}
	m := foldedModel(t)
	m.saveChat("")
	check("/save")
	_, _ = foldedModel(t).saveThenClear()
	check("the save /clear offers")
	_, _ = foldedModel(t).saveThenQuit()
	check("the save offered on quit")

	plain := foldModel(t)
	plain.saveChat("")
	if reqs := fake.seen(); len(reqs[len(reqs)-1].Folded) != 0 {
		t.Error("a chat that never folded sent a folded part")
	}
}

// F9. When the saved copy was made from this screen and the screen had already
// let its oldest turns go, the reply says so -- and only then.
func TestSave_SaysWhatTheScreenHadAlreadyLetGo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var fromScreen atomic.Bool
	fromScreen.Store(true)
	serveHistory(t, func(req protocol.HistoryRequest) protocol.HistoryResponse {
		e := savedChats()[0]
		return protocol.HistoryResponse{Entry: &e, HistoryExtras: protocol.HistoryExtras{FoldedSaved: fromScreen.Load()}}
	})
	m := foldedModel(t)
	m.evictedTurns = 40
	if msg := m.saveChat(""); !strings.Contains(msg, "the 40 earliest turns had already left this screen") {
		t.Errorf("save reply = %q", msg)
	}
	fromScreen.Store(false)
	if msg := m.saveChat(""); strings.Contains(msg, "earliest turns") {
		t.Errorf("a copy the daemon made from its own record claimed turns were missing: %q", msg)
	}
}

// F4 as a property: whatever the transcript -- texts repeated at will, system
// lines and local replies between -- wherever the fold point lands and however
// much eviction takes, the history sent afterwards is the history sent before,
// or the end of it: never a turn repeated, never one skipped.
func FuzzFoldPoint_EvictionNeverRepeatsOrSkips(f *testing.F) {
	f.Add([]byte{0, 1, 0, 1, 0, 1, 0, 1, 0}, uint8(4), uint8(5))
	f.Add([]byte{0, 5, 2, 0, 1, 3, 4, 9, 0, 1, 0}, uint8(6), uint8(3))
	f.Fuzz(func(t *testing.T, shape []byte, kept, limit uint8) {
		if len(shape) == 0 || len(shape) > 200 {
			return
		}
		m := newTestModel()
		for _, b := range shape {
			text := "t" + string(rune('0'+b/4%3)) // three texts: repeats are the point
			switch b % 4 {
			case 0:
				m.turns = append(m.turns, turn{role: roleUser, text: text})
			case 1:
				m.turns = append(m.turns, turn{role: roleAssistant, text: text})
			case 2:
				m.turns = append(m.turns, turn{role: roleSystem, text: text})
			case 3:
				m.turns = append(m.turns, turn{role: roleAssistant, text: text, local: true})
			}
		}
		m.turns = append(m.turns, turn{role: roleUser, text: "the question"})
		m.state = stateSending
		m.streamCh = make(chan tea.Msg, 4)
		next, _ := m.Update(foldMsg{&protocol.FoldInfo{Summary: foldPair(), Kept: int(kept % 16), Folded: 1, Revision: "r"}})
		m = next.(chatModel)
		m.appendTurn(turn{role: roleAssistant, text: "the answer"})
		before := m.historyPreview()
		m.limits = transcriptLimits{turns: max(int(limit), 2), bytes: 1 << 30}
		m.enforceTranscriptBound()
		after := m.historyPreview()
		if m.fold == nil || len(m.fold.summary) != 2 {
			return // no fold: the history is the transcript, which eviction shortens
		}
		b, a := sentTexts(before), sentTexts(after)
		if len(a) > len(b) || !sameTexts(a, b[len(b)-len(a):]) {
			t.Fatalf("eviction changed what is sent:\nbefore %q\nafter  %q", b, a)
		}
	})
}
