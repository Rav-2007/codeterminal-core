package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mochiii/protocol"
)

func appendExchange(t *testing.T, mem *MemoryStore, ws, q, a string) {
	t.Helper()
	ctx := context.Background()
	if err := mem.AppendTurn(ctx, ws, "user", q); err != nil {
		t.Fatal(err)
	}
	if err := mem.AppendTurn(ctx, ws, "assistant", a); err != nil {
		t.Fatal(err)
	}
}

// Bookmarking the current chat saves it and pins it, and saving more work later
// keeps the pin -- the updated copy replaces the old one, bookmark included.
func TestHistory_BookmarkingTheCurrentChatSavesAndPinsIt(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	appendExchange(t, mem, srv.workspace, "plan the launch", "here is a plan")

	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryBookmark})
	if resp.Error != "" || resp.Entry == nil || !resp.Entry.Bookmarked {
		t.Fatalf("bookmark = %+v", resp)
	}
	list := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList})
	if len(list.Entries) == 0 || !list.Entries[0].Current || !list.Entries[0].Bookmarked {
		t.Fatalf("the current chat does not show its bookmark: %+v", list.Entries)
	}

	appendExchange(t, mem, srv.workspace, "and the budget?", "about 5000")
	saved := saveNow(t, srv, "")
	if !saved.Entry.Bookmarked {
		t.Errorf("saving more work unpinned the chat: %+v", saved.Entry)
	}
	if files := savedFiles(t); len(files) != 1 {
		t.Errorf("the re-save added a copy instead of replacing it: %v", files)
	}

	un := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryUnbookmark, ID: firstSavedID(t, srv)})
	if un.Error != "" || un.Entry == nil || un.Entry.Bookmarked {
		t.Errorf("unbookmark = %+v", un)
	}
}

func firstSavedID(t *testing.T, srv *Server) string {
	t.Helper()
	for _, e := range askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries {
		if !e.Current {
			return e.ID
		}
	}
	t.Fatal("no saved chat")
	return ""
}

// Pruning drops the oldest chats past the limit -- never a bookmarked one, and
// a bookmarked one does not use up the limit either.
func TestHistory_PruningNeverRemovesABookmarkedChat(t *testing.T) {
	a := testArchive(t, "/ws/prune")
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 5; i++ {
		turns := []storedTurn{{Role: "user", Content: fmt.Sprintf("q%d", i)}, {Role: "assistant", Content: "a"}}
		id, err := a.saveWith(turns, "", "", false, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := a.setBookmark(ids[0], true); err != nil { // the OLDEST
		t.Fatal(err)
	}
	removed := pruneHistory(a.root, 2, 1<<30, base.Add(10*time.Hour))
	if removed != 2 {
		t.Errorf("removed %d, want the 2 oldest unbookmarked", removed)
	}
	left, _ := a.list()
	got := map[string]bool{}
	for _, c := range left {
		got[c.ID] = true
	}
	if !got[ids[0]] || !got[ids[3]] || !got[ids[4]] || len(left) != 3 {
		t.Errorf("left %v, want the bookmarked oldest plus the 2 newest", left)
	}
}

// The history search finds a chat by what was said in it, not only its title.
func TestHistory_SearchMatchesWhatWasSaid(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	appendExchange(t, mem, srv.workspace, "read my certificate", "It is a UDYAM registration certificate.")
	saveNow(t, srv, "")
	srv.resetPersistedHistory()
	appendExchange(t, mem, srv.workspace, "plan the launch", "here is a plan")

	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList, Query: "udyam"})
	if len(resp.Entries) != 1 || resp.Entries[0].Title != "read my certificate" {
		t.Errorf("search for a word in an answer = %+v", resp.Entries)
	}
	if all := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList}); len(all.Entries) != 2 {
		t.Errorf("an empty query must list everything: %+v", all.Entries)
	}
}

// /compact: the older turns become a summary written by the model, the most
// recent exchanges stay word for word, and the full chat is saved first.
func TestHistory_CompactSummarisesTheOlderTurnsAndSavesTheWholeChat(t *testing.T) {
	var sent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"- The user is planning a launch.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.apiBase, srv.apiKey, srv.cfg, srv.modelOverride = upstream.URL, "k", &Config{}, "test/model"
	for i := 1; i <= 5; i++ {
		appendExchange(t, mem, srv.workspace, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
	}

	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryCompact})
	if resp.Error != "" {
		t.Fatalf("compact: %s", resp.Error)
	}
	if resp.Compacted != 6 || len(resp.Turns) != 6 {
		t.Fatalf("compacted=%d turns=%d, want 6 folded and summary+4 kept", resp.Compacted, len(resp.Turns))
	}
	if resp.Turns[0].Content != "Continuing: question 1" || resp.Turns[0].Role != "user" {
		t.Errorf("turn 0 = %+v: it is the compacted chat's title in history, so nothing more", resp.Turns[0])
	}
	if !strings.Contains(resp.Turns[1].Content, "The user is planning a launch.") {
		t.Errorf("the summary is not the model's: %+v", resp.Turns[1])
	}
	if resp.Turns[2].Content != "question 4" || resp.Turns[5].Content != "answer 5" {
		t.Errorf("the recent turns were not kept verbatim: %+v", resp.Turns[2:])
	}
	// Only the OLDER turns went to be summarised.
	if !strings.Contains(sent, "question 3") || strings.Contains(sent, "question 4") {
		t.Errorf("the summary request carried the wrong turns:\n%s", sent)
	}
	// The whole chat is in history, and the compacted one is a new, unsaved chat.
	files := savedFiles(t)
	if len(files) != 1 {
		t.Fatalf("saved %v, want the full chat", files)
	}
	list := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList})
	if len(list.Entries) != 2 || !list.Entries[0].Current || !list.Entries[0].Unsaved || list.Entries[1].Turns != 10 {
		t.Errorf("list after compact = %+v", list.Entries)
	}
	if now, _ := mem.LoadAllTurns(context.Background(), srv.workspace); len(now) != 6 {
		t.Errorf("memory holds %d turns, want 6", len(now))
	}
}

// A provider that refuses the summary request as too large -- as Groq's free
// tier did for a 46-message chat, 413 "Limit 7000, Requested 13460" -- gets one
// sized from its own figures, and every older question still reaches it.
func TestHistory_CompactShrinksARequestTheProviderRefusesAsTooLarge(t *testing.T) {
	const acceptBytes = 12_000
	var calls int
	var last, model string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls++
		if len(b) > acceptBytes {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprintf(w, `{"error":{"message":"Request too large for model on input tokens per minute (ITPM): Limit %d, Requested %d, please reduce your message size and try again. Need more tokens? https://example.invalid/settings/billing","type":"tokens","code":"rate_limit_exceeded"}}`,
				acceptBytes/2, len(b)/2)
			return
		}
		last = string(b)
		var req struct{ Model string }
		_ = json.Unmarshal(b, &req)
		model = req.Model
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"- A long chat, summarised.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.apiBase, srv.apiKey = upstream.URL, "k"
	srv.cfg = &Config{DefaultTier: "default", Tiers: map[string]ModelTier{
		"default": {Slug: "test/default", Active: true},
		"chosen":  {Slug: "test/chosen", Active: true},
	}}
	for i := 1; i <= 20; i++ {
		appendExchange(t, mem, srv.workspace, fmt.Sprintf("question %d", i), strings.Repeat(fmt.Sprintf("a long answer %d. ", i), 200))
	}

	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryCompact, Tier: "chosen"})
	if resp.Error != "" {
		t.Fatalf("compact: %s", resp.Error)
	}
	if calls != 2 {
		t.Errorf("%d requests, want the refused one and one sized from its figures", calls)
	}
	if model != "test/chosen" {
		t.Errorf("the summary was written by %q, want the model the client chose", model)
	}
	for i := 1; i <= 18; i++ { // the older turns; 19 and 20 are kept verbatim
		if !strings.Contains(last, fmt.Sprintf("question %d\\n", i)) {
			t.Errorf("question %d did not reach the summary: trimming must cut the long answers, not drop exchanges", i)
		}
	}
	if !strings.Contains(resp.Turns[1].Content, "A long chat, summarised.") {
		t.Errorf("summary turn = %+v", resp.Turns[1])
	}
}

// When the model cannot write the summary, the client is told why -- in the
// scrubbed words, not the provider's -- and the chat is left as it was.
func TestHistory_CompactSaysWhyTheModelFailed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Invalid API key org_secret_detail"}}`)
	}))
	defer upstream.Close()

	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.apiBase, srv.apiKey, srv.cfg, srv.modelOverride = upstream.URL, "k", &Config{}, "test/model"
	for i := 1; i <= 5; i++ {
		appendExchange(t, mem, srv.workspace, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
	}

	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryCompact})
	if !strings.HasPrefix(resp.Error, "the model could not write the summary: the provider rejected the API key") {
		t.Errorf("error = %q", resp.Error)
	}
	if strings.Contains(resp.Error, "daemon log") || strings.Contains(resp.Error, "org_secret_detail") {
		t.Errorf("error = %q: it must say why, without the provider's own text", resp.Error)
	}
	if now, _ := mem.LoadAllTurns(context.Background(), srv.workspace); len(now) != 10 {
		t.Errorf("a failed compact changed the chat: %d turns, want 10", len(now))
	}
}

func TestCompactTranscript_TrimsTheLongestMessagesBeforeDroppingAny(t *testing.T) {
	var turns []storedTurn
	for i := 1; i <= 10; i++ {
		turns = append(turns,
			storedTurn{Role: "user", Content: fmt.Sprintf("question %d", i)},
			storedTurn{Role: "assistant", Content: strings.Repeat("é", 2000)}) // 2 bytes a rune
	}
	out := compactTranscript(turns, 6_000)
	if len(out) > 6_000 {
		t.Fatalf("%d bytes, over the 6000 budget", len(out))
	}
	if !utf8.ValidString(out) {
		t.Error("a trim split a multi-byte character")
	}
	for i := 1; i <= 10; i++ {
		if !strings.Contains(out, fmt.Sprintf("User: question %d\n", i)) {
			t.Errorf("question %d was dropped", i)
		}
	}
	if strings.Contains(out, "earliest part omitted") || !strings.Contains(out, "[…]") {
		t.Error("over budget, the answers must be trimmed and marked, and nothing omitted")
	}

	// Too small for even a short excerpt of each: the earliest go, the newest stay.
	tiny := compactTranscript(turns, 1_200)
	if len(tiny) > 1_200 || !strings.HasPrefix(tiny, "(earliest part omitted)") || !strings.Contains(tiny, "question 10\n") || strings.Contains(tiny, "question 1\n") {
		t.Errorf("tiny budget:\n%s", tiny)
	}

	// Under budget, the transcript is whole.
	if whole := compactTranscript(turns[:2], 60_000); strings.Contains(whole, "[…]") {
		t.Error("a transcript under budget was trimmed")
	}
}

func TestShrinkCompactBudget(t *testing.T) {
	groq := "Request too large ... (ITPM): Limit 7000, Requested 13460, please reduce your message size"
	if got := shrinkCompactBudget(60_000, groq); got < 25_000 || got > 27_000 {
		t.Errorf("from Groq's figures: %d, want about 60000*7000/13460*0.85 = 26500", got)
	}
	if got := shrinkCompactBudget(60_000, "maximum context length is 8192 tokens"); got != 30_000 {
		t.Errorf("without figures: %d, want half", got)
	}
	if got := shrinkCompactBudget(10_000, "Limit 7000, Requested 7001"); got > 8_500 {
		t.Errorf("a near miss must still shrink by the margin, or the retry is refused again: %d", got)
	}
	if got := shrinkCompactBudget(10_000, "Limit 9000, Requested 7000"); got != 5_000 {
		t.Errorf("figures that do not describe an overrun are ignored: %d, want half", got)
	}
}

func TestHistory_CompactSaysWhenThereIsNothingToCompact(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	appendExchange(t, mem, srv.workspace, "hi", "hello")
	appendExchange(t, mem, srv.workspace, "and?", "that's all")
	resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryCompact})
	if !strings.HasPrefix(resp.Error, "nothing to compact yet: this chat has 4 messages") {
		t.Errorf("error = %q", resp.Error)
	}
	if files := savedFiles(t); len(files) != 0 {
		t.Errorf("a refused compact saved %v", files)
	}
}
