package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// WHAT A SAVE HOLDS, AND WHAT SAVING AGAIN MAY DO TO A COPY (2026-10-11).
//
// Two things went wrong together. /clear emptied the screen and told the daemon
// nothing, so a save wrote every turn since the last ctrl+n; and a save wrote
// whatever memory.db held OVER the saved copy, while memory.db was dropping
// turns older than thirty days. The first is the client's half (the reset it
// now sends is the one pinned here); the second is all in this package.

func turnsOf(pairs ...string) []storedTurn {
	out := make([]storedTurn, 0, len(pairs))
	for i, p := range pairs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, storedTurn{Role: role, Content: p})
	}
	return out
}

func contentsOf(turns []storedTurn) string {
	parts := make([]string, len(turns))
	for i, t := range turns {
		parts[i] = t.Content
	}
	return strings.Join(parts, " ")
}

// dropOldestTurns removes the n oldest rows of the workspace's chat, the way
// the count cap does, without writing a thousand turns to get there.
func dropOldestTurns(t *testing.T, mem *MemoryStore, workspace string, n int) {
	t.Helper()
	if _, err := mem.db.Exec(
		`DELETE FROM turns WHERE id IN (SELECT id FROM turns WHERE workspace = ? ORDER BY id LIMIT ?)`,
		workspace, n); err != nil {
		t.Fatalf("dropping the oldest turns: %v", err)
	}
}

func TestExtendSaved(t *testing.T) {
	for _, tc := range []struct {
		name           string
		saved, current []storedTurn
		want           string
		wantOK         bool
	}{
		{"the chat went on after the save",
			turnsOf("q1", "a1"), turnsOf("q1", "a1", "q2", "a2"), "q1 a1 q2 a2", true},
		{"nothing new since the save",
			turnsOf("q1", "a1"), turnsOf("q1", "a1"), "q1 a1", true},
		{"the store dropped the oldest turns and the chat went on",
			turnsOf("q1", "a1", "q2", "a2"), turnsOf("q2", "a2", "q3", "a3"), "q1 a1 q2 a2 q3 a3", true},
		{"the store dropped the oldest turns and nothing is new",
			turnsOf("q1", "a1", "q2", "a2"), turnsOf("q2", "a2"), "q1 a1 q2 a2", true},
		{"a chat that repeats itself lines up on the most that agrees",
			turnsOf("hi", "hello", "hi", "hello"), turnsOf("hi", "hello", "hi", "hello", "q", "a"), "hi hello hi hello q a", true},
		{"a different chat is not a continuation",
			turnsOf("q1", "a1"), turnsOf("other", "chat"), "", false},
		{"the same words in the other role are not the same turn",
			turnsOf("q1", "a1"), []storedTurn{{Role: "assistant", Content: "q1"}, {Role: "user", Content: "a1"}}, "", false},
		{"a chat holding only the MIDDLE of the copy is not a continuation",
			turnsOf("q1", "a1", "q2", "a2", "q3", "a3"), turnsOf("q2", "a2"), "", false},
		{"an empty copy continues nothing",
			nil, turnsOf("q1", "a1"), "", false},
	} {
		got, ok := extendSaved(tc.saved, tc.current)
		if ok != tc.wantOK || contentsOf(got) != tc.want {
			t.Errorf("%s: extendSaved = %q, %v; want %q, %v", tc.name, contentsOf(got), ok, tc.want, tc.wantOK)
		}
	}
}

// The owner's report, at the daemon: what was cleared is not in the save and
// does not come back on resume. The reset between the exchanges is what /clear
// sends now (clients/tui TestClear_*); before, it sent nothing and the save
// below held all six turns.
func TestServer_AClearedChatIsNotInTheNextSave(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)

	srv.persistTurn("before the clear, one", "answer one", nil)
	srv.persistTurn("before the clear, two", "answer two", nil)
	srv.resetPersistedHistory()
	srv.persistTurn("after the clear", "answer three", nil)

	saved := saveNow(t, srv, "")
	if saved.Entry.Turns != 2 || saved.Entry.Title != "after the clear" {
		t.Fatalf("save = %+v, want only the exchange after the clear", saved.Entry)
	}
	if back := srv.loadPersistedHistory(context.Background()); len(back) != 2 {
		t.Errorf("a relaunch would show %d turns, want the 2 after the clear: %+v", len(back), back)
	}
	srv.resetPersistedHistory()
	resumed := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryResume, ID: saved.Entry.ID})
	if len(resumed.Turns) != 2 || resumed.Turns[0].Content != "after the clear" {
		t.Errorf("resume = %+v, want only the exchange after the clear", resumed.Turns)
	}
}

// MEASURED before the fix: 20 turns resumed, one question, one save -- the
// store held 2 turns, the save said "2 turns", and the 20-turn file was gone.
//
// Neuter check: put `OR created_at < ?` back in pruneWorkspace and the store
// holds 2 turns after the new exchange. Saving `current` instead of the
// extended copy in saveChatLocked does NOT turn this red on its own -- with no
// age rule the store still holds everything -- which is why that half has its
// own test, TestServer_SavingAgainKeepsTurnsTheStoreHasDropped.
func TestServer_SavingAResumedOldChatKeepsEveryTurn(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	root, err := historyRoot()
	if err != nil {
		t.Fatal(err)
	}
	archive := newChatArchive(root, srv.workspace)

	then := time.Now().Add(-40 * 24 * time.Hour).UTC()
	var old []storedTurn
	for i := 0; i < 10; i++ {
		at := then.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		old = append(old,
			storedTurn{Role: "user", Content: "old question", CreatedAt: at},
			storedTurn{Role: "assistant", Content: "old answer", CreatedAt: at})
	}
	id, err := archive.save(old, "", "forty days old", then)
	if err != nil || id == "" {
		t.Fatalf("save: %q, %v", id, err)
	}
	if resp := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryResume, ID: id}); len(resp.Turns) != 20 {
		t.Fatalf("resume = %+v", resp)
	}

	srv.persistTurn("one new question", "one new answer", nil)
	if now, _ := mem.LoadAllTurns(context.Background(), srv.workspace); len(now) != 22 {
		t.Errorf("after one new exchange the stored chat holds %d turns, want 22", len(now))
	}
	again := saveNow(t, srv, "")
	if again.Entry.Turns != 22 || again.Entry.Title != "forty days old" {
		t.Errorf("saving again = %+v, want all 22 turns under the same name", again.Entry)
	}
	if files := savedFiles(t); len(files) != 1 {
		t.Errorf("saving again left %d files, want the one updated copy: %q", len(files), files)
	}
	_, turns, err := archive.loadStored(again.Entry.ID)
	if err != nil || len(turns) != 22 || turns[0].Content != "old question" || turns[21].Content != "one new answer" {
		t.Errorf("the updated copy holds %d turns (%v), want the 20 old ones then the new exchange", len(turns), err)
	}
}

// The store drops a chat's oldest turns past its cap; the saved copy must not
// lose them when the chat is saved again.
//
// Neuter check: save `current` instead of the extended copy in saveChatLocked
// and the copy comes back as 6 turns starting at "second".
func TestServer_SavingAgainKeepsTurnsTheStoreHasDropped(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)

	srv.persistTurn("first", "a1", nil)
	srv.persistTurn("second", "a2", nil)
	srv.persistTurn("third", "a3", nil)
	saveNow(t, srv, "long chat")

	dropOldestTurns(t, mem, srv.workspace, 2)
	srv.persistTurn("fourth", "a4", nil)
	again := saveNow(t, srv, "")

	if again.Entry.Turns != 8 || again.Entry.Title != "long chat" {
		t.Fatalf("saving again = %+v, want 8 turns, still named", again.Entry)
	}
	root, _ := historyRoot()
	_, turns, err := newChatArchive(root, srv.workspace).loadStored(again.Entry.ID)
	if err != nil || contentsOf(turns) != "first a1 second a2 third a3 fourth a4" {
		t.Errorf("the updated copy = %q (%v), want every turn ever saved, then the new one", contentsOf(turns), err)
	}
	if files := savedFiles(t); len(files) != 1 {
		t.Errorf("saving again left %d files, want the one updated copy", len(files))
	}
	list := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries
	if len(list) != 2 || !list[0].Current || list[0].Unsaved || list[0].SavedAs != again.Entry.ID {
		t.Errorf("after saving, the current chat = %+v, want it marked saved as that copy", list)
	}
}

// A link can outlive the chat it described: the store is emptied and the link
// file is not removed (clearLink is best effort). The next save must neither
// delete that copy nor glue the new chat onto it.
//
// Neuter check: treat "no overlap" as a continuation in extendSaved and the old
// copy is replaced; this test then finds one file.
func TestServer_AChatThatDoesNotContinueItsCopyIsSavedBesideIt(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	ctx := context.Background()

	srv.persistTurn("chat A", "answer A", nil)
	first := saveNow(t, srv, "named A")

	// The store is replaced behind the link's back.
	if err := mem.ClearWorkspace(ctx, srv.workspace); err != nil {
		t.Fatal(err)
	}
	srv.persistTurn("chat B", "answer B", nil)
	second := saveNow(t, srv, "")

	if second.Entry.ID == first.Entry.ID || second.Entry.Turns != 2 || second.Entry.Title != "chat B" {
		t.Errorf("the second save = %+v, want chat B alone, not under chat A's name", second.Entry)
	}
	root, _ := historyRoot()
	h, turns, err := newChatArchive(root, srv.workspace).loadStored(first.Entry.ID)
	if err != nil || h.Name != "named A" || contentsOf(turns) != "chat A answer A" {
		t.Errorf("chat A's copy = %q, %q (%v), want it untouched", h.Name, contentsOf(turns), err)
	}
	if files := savedFiles(t); len(files) != 2 {
		t.Errorf("%d saved files, want both chats", len(files))
	}
}

// A row that would not be read back is not written, so a copy as it is READ
// still lines up with the chat as it is STORED.
//
// Neuter check: save `stored` instead of readableTurns(stored). The copy read
// back then lacks a row the store still has in the middle of the chat, the chat
// no longer "continues" its own copy, and the second save adds a file instead
// of updating one.
func TestServer_ARowThatCannotBeReadBackDoesNotBreakUpdating(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	ctx := context.Background()

	srv.persistTurn("first", "a1", nil)
	// An empty answer from before the write-side guard existed (persistTurn).
	if _, err := mem.db.ExecContext(ctx,
		`INSERT INTO turns (workspace, role, content, created_at) VALUES (?, 'assistant', '', ?)`,
		srv.workspace, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	srv.persistTurn("second", "a2", nil)
	if first := saveNow(t, srv, ""); first.Entry.Turns != 4 {
		t.Errorf("the first save = %d turns, want the 4 that can be read back", first.Entry.Turns)
	}
	srv.persistTurn("third", "a3", nil)
	again := saveNow(t, srv, "")

	if again.Entry.Turns != 6 || len(savedFiles(t)) != 1 {
		t.Errorf("saving again = %d turns in %d file(s), want the one copy updated to 6 readable turns",
			again.Entry.Turns, len(savedFiles(t)))
	}
	list := askHistory(t, srv, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries
	if len(list) != 2 || list[0].Unsaved {
		t.Errorf("after saving, the list = %+v, want the current chat marked saved", list)
	}
}

// A file load would refuse is not written: "saved" has to mean it can come back.
func TestArchive_SaveRefusesAChatTooLargeToReadBack(t *testing.T) {
	a := testArchive(t, "/w")
	big := turnsOf("q", strings.Repeat("x", maxArchiveBytes+1))
	id, err := a.save(big, "", "", time.Now())
	if !errors.Is(err, errChatTooLarge) || id != "" {
		t.Fatalf("save of an oversized chat = %q, %v; want errChatTooLarge", id, err)
	}
	if left, _ := filepath.Glob(filepath.Join(a.dir, "*")); len(left) != 0 {
		t.Errorf("the refused save left files behind: %q", left)
	}

	// Just inside the limit still saves, and reads back.
	fits := turnsOf("q", strings.Repeat("x", maxArchiveBytes-4096))
	id, err = a.save(fits, "", "", time.Now())
	if err != nil || id == "" {
		t.Fatalf("save of a chat inside the limit = %q, %v", id, err)
	}
	if _, turns, err := a.loadStored(id); err != nil || len(turns) != 2 {
		t.Errorf("a chat saved inside the limit does not read back: %d turns, %v", len(turns), err)
	}
}

// When the copy plus what is new no longer fits in one file, the copy keeps
// what it has and the current chat is saved beside it -- refusing would mean
// this chat could never be saved again.
func TestServer_AnUpdateTooLargeForOneFileIsSavedBesideTheCopy(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	half := strings.Repeat("x", maxArchiveBytes*5/8)

	srv.persistTurn("first", half, nil)
	srv.persistTurn("second", "a2", nil)
	first := saveNow(t, srv, "big")

	dropOldestTurns(t, mem, srv.workspace, 2)
	srv.persistTurn("third", half, nil)
	second := saveNow(t, srv, "")

	if second.Entry.ID == first.Entry.ID || second.Entry.Turns != 4 {
		t.Fatalf("the second save = %+v, want the current 4 turns as their own copy", second.Entry)
	}
	root, _ := historyRoot()
	archive := newChatArchive(root, srv.workspace)
	if _, turns, err := archive.loadStored(first.Entry.ID); err != nil || len(turns) != 4 || turns[0].Content != "first" {
		t.Errorf("the first copy: %d turns, %v; want it kept whole", len(turns), err)
	}
	if _, turns, err := archive.loadStored(second.Entry.ID); err != nil || len(turns) != 4 || turns[0].Content != "second" {
		t.Errorf("the second copy: %d turns, %v; want the current chat", len(turns), err)
	}
}

// The client is told why, in words, and never a path.
func TestHistoryClientError_ATooLargeChatSaysSo(t *testing.T) {
	msg := historyClientError("saving this chat", errors.Join(errors.New("/home/someone/.local/state"), errChatTooLarge))
	if !strings.Contains(msg, "too large") || strings.Contains(msg, "/home") || strings.Contains(msg, string(os.PathSeparator)+"local") {
		t.Errorf("the client is told %q", msg)
	}
}
