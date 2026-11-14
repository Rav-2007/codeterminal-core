package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// EACH TERMINAL WINDOW HAS ITS OWN CHAT (chatscope.go, chatwindow.go). The owner,
// 2026-11-14, answering "should two terminal windows in one folder each get their
// own chat?": yes. These tests are the rule that one window's chat is not another's
// and that a window's id can never be anything but a window.

const (
	winA = "aaaaaaaaaaaaaaaa"
	winB = "bbbbbbbbbbbbbbbb"
	winC = "cccccccccccccccc"
)

func winScope(ws, window string) chatScope {
	return chatScope{Workspace: ws, Surface: SurfaceTerminal, Window: window}
}

// WHICH IDS ARE WINDOWS. Only the terminal has them; an id must be 8 to 32
// lowercase hex digits and nothing else -- it becomes part of a file name and a SQL
// parameter, so a form that can be neither a path nor a surprise is the only form
// accepted. Anything else is not refused, it is the shared chat.
//
// Neuter check: let windowFor return the id for any surface, or skip the pattern.
func TestWindowFor(t *testing.T) {
	for _, c := range []struct {
		surface Surface
		id      string
		want    string
	}{
		{SurfaceTerminal, winA, winA},
		{SurfaceTerminal, "0123abcd", "0123abcd"},                           // 8: the shortest
		{SurfaceTerminal, strings.Repeat("f", 32), strings.Repeat("f", 32)}, // 32: the longest
		{SurfaceTerminal, "", ""},                                           // no window: the shared chat
		{SurfaceTerminal, "0123abc", ""},                                    // 7
		{SurfaceTerminal, strings.Repeat("f", 33), ""},                      // 33
		{SurfaceTerminal, "AAAAAAAAAAAAAAAA", ""},                           // upper case
		{SurfaceTerminal, "../../etc/passwd", ""},                           // a path
		{SurfaceTerminal, "aaaaaaaa/../bbbb", ""},                           //
		{SurfaceTerminal, "aaaaaaaa.json", ""},                              //
		{SurfaceTerminal, "aaaaaaaa\x00", ""},                               // a NUL
		{SurfaceTerminal, " " + winA, ""},                                   // no trimming
		{SurfaceVSCode, winA, ""},                                           // the editor's windows share one chat
		{SurfaceOther, winA, ""},                                            // so does the isolated surface
	} {
		if got := windowFor(c.surface, c.id); got != c.want {
			t.Errorf("windowFor(%s, %q) = %q, want %q", c.surface, c.id, got, c.want)
		}
	}
}

// THE STORE REFUSES A WINDOW THAT IS NOT ONE, however it got there.
func TestChatScope_RefusesAMalformedWindowOrOneOnASurfaceWithNone(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	for _, bad := range []chatScope{
		{Workspace: "/w", Surface: SurfaceVSCode, Window: winA},
		{Workspace: "/w", Surface: SurfaceOther, Window: winA},
		{Workspace: "/w", Surface: SurfaceTerminal, Window: "../x"},
		{Workspace: "/w", Surface: SurfaceTerminal, Window: "SHORT"},
	} {
		if err := bad.check(); err != errBadWindow {
			t.Errorf("%+v.check() = %v, want errBadWindow", bad, err)
		}
		if err := s.AppendTurn(ctx, bad, "user", "x"); err != errBadWindow {
			t.Errorf("AppendTurn(%+v) = %v, want errBadWindow", bad, err)
		}
		if _, err := s.LoadAllTurns(ctx, bad); err != errBadWindow {
			t.Errorf("LoadAllTurns(%+v) = %v, want errBadWindow", bad, err)
		}
		a := newChatArchive(filepath.Join(t.TempDir(), "history"), bad)
		if err := a.ensureDir(); err != errBadWindow {
			t.Errorf("an archive for %+v made its folder: %v", bad, err)
		}
	}
	if err := winScope("/w", winA).check(); err != nil {
		t.Errorf("a terminal window is refused: %v", err)
	}
}

// EVERY STORE METHOD IS ISOLATED PER WINDOW, and the terminal's shared chat is a
// chat of its own beside them. Rows are interleaved so their ids are.
//
// Neuter check: drop `AND window_id = ?` from any one of loadRecentTurnsAt,
// LoadAllTurns, turnsAfter, lastTurnID, ClearWorkspace, ReplaceWorkspace, the
// SearchTurns MATCH, or pruneWorkspace.
func TestWindows_EveryStoreMethodIsIsolatedPerWindow(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/w"
	a, b, shared := winScope(ws, winA), winScope(ws, winB), winScope(ws, "")

	appendOrFail(t, s, a, "user", "window A question needle-a")    // 1
	appendOrFail(t, s, b, "user", "window B question needle-b")    // 2
	appendOrFail(t, s, shared, "user", "shared question needle-s") // 3
	appendOrFail(t, s, a, "assistant", "window A answer")          // 4
	appendOrFail(t, s, b, "assistant", "window B answer")          // 5

	all := func(sc chatScope) string {
		got, err := s.LoadAllTurns(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		return storedContents(got)
	}
	if got := all(a); got != "window A question needle-a | window A answer" {
		t.Errorf("LoadAllTurns(A) = %q", got)
	}
	if got := all(b); got != "window B question needle-b | window B answer" {
		t.Errorf("LoadAllTurns(B) = %q", got)
	}
	if got := all(shared); got != "shared question needle-s" {
		t.Errorf("LoadAllTurns(shared) = %q", got)
	}
	if rec, last, err := s.loadRecentTurnsAt(ctx, a, 50, false); err != nil || len(rec) != 2 || last != 4 {
		t.Errorf("loadRecentTurnsAt(A) = %+v, last %d, %v; want its two turns, last id 4", rec, last, err)
	}
	// B's newest row is also the newest of all, so it cannot show a missing window
	// filter; A's (id 4) and the shared chat's (id 3) can: they sit below id 5.
	for _, c := range []struct {
		name string
		sc   chatScope
		want int64
	}{{"A", a, 4}, {"B", b, 5}, {"shared", shared, 3}} {
		if id, err := s.lastTurnID(ctx, c.sc); err != nil || id != c.want {
			t.Errorf("lastTurnID(%s) = %d, %v; want %d", c.name, id, err, c.want)
		}
	}
	if got, last, err := s.turnsAfter(ctx, a, 1); err != nil || storedContents(got) != "window A answer" || last != 4 {
		t.Errorf("turnsAfter(A, 1) = %q, last %d, %v; want only A's own later turn", storedContents(got), last, err)
	}
	search := func(sc chatScope, q string) int {
		h, err := s.SearchTurns(ctx, sc, q, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(h)
	}
	if search(a, "needle-a") != 1 || search(a, "needle-b") != 0 || search(a, "needle-s") != 0 {
		t.Error("window A's search sees another chat's text, or misses its own")
	}
	if n := search(a, "question"); n != 1 {
		t.Errorf("search(A, \"question\") = %d hits, want 1: B's and the shared chat's questions are not A's", n)
	}
	if search(shared, "needle-s") != 1 || search(shared, "needle-a") != 0 {
		t.Error("the shared chat's search sees a window's text, or misses its own")
	}

	if err := s.ReplaceWorkspace(ctx, a, []storedTurn{{Role: "user", Content: "A replaced"}}); err != nil {
		t.Fatal(err)
	}
	if all(a) != "A replaced" || all(b) != "window B question needle-b | window B answer" || all(shared) != "shared question needle-s" {
		t.Errorf("ReplaceWorkspace(A) reached another chat: A=%q B=%q shared=%q", all(a), all(b), all(shared))
	}
	if err := s.ClearWorkspace(ctx, a); err != nil {
		t.Fatal(err)
	}
	if all(a) != "" || all(b) == "" || all(shared) == "" {
		t.Errorf("ClearWorkspace(A) erased another chat: B=%q shared=%q", all(b), all(shared))
	}
	ftsIntegrity(t, s.db)
}

// RETENTION IS PER WINDOW: a window that talks past the cap evicts its own oldest
// turns and nobody else's.
//
// Neuter check: drop `AND window_id = ?` from pruneWorkspace.
func TestWindows_RetentionIsPerWindow(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	a, b := winScope("/w", winA), winScope("/w", winB)
	appendOrFail(t, s, b, "user", "B's only turn")
	for i := 0; i < maxTurnsPerWorkspace+3; i++ {
		appendOrFail(t, s, a, "user", fmt.Sprintf("a %d", i))
	}
	if at, err := s.LoadAllTurns(ctx, a); err != nil || len(at) != maxTurnsPerWorkspace {
		t.Errorf("window A holds %d turns (%v), want the cap", len(at), err)
	}
	if bt, err := s.LoadAllTurns(ctx, b); err != nil || len(bt) != 1 {
		t.Errorf("window B holds %d turns (%v) after A passed the cap, want its own 1", len(bt), err)
	}
}

// EXISTING ROWS HAVE NO WINDOW: the v5 migration files them under the shared chat.
func TestWindows_TheMigrationLeavesEveryOldRowInTheSharedChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	buildV5(t, path)
	s, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM turns WHERE window_id != ''`); n != 0 {
		t.Errorf("%d migrated rows were given a window", n)
	}
	if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM turns WHERE window_id = '' AND surface = 'vscode'`); n != 3 {
		t.Errorf("%d migrated rows are the editor's shared chat, want 3", n)
	}
}

// --- through real connections ---

func (d *syncDaemon) startWin(t *testing.T, name, window string) ([]protocol.Turn, string) {
	t.Helper()
	_, _, hs, closeConn := d.connectWindow(t, name, window)
	closeConn()
	return hs.PersistedHistory, hs.ChatRevision
}

func (d *syncDaemon) askWin(t *testing.T, name, window string, req protocol.PromptRequest) protocol.TokenResponse {
	t.Helper()
	enc, dec, _, closeConn := d.connectWindow(t, name, window)
	defer closeConn()
	req.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(req); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the answer: %v", err)
		}
		if tok.Error != "" {
			t.Fatalf("the turn failed: %s", tok.Error)
		}
		if tok.Done {
			return tok
		}
	}
}

func askHistoryWin(t *testing.T, srv *Server, window string, req protocol.HistoryRequest) protocol.HistoryResponse {
	t.Helper()
	req.Chats = true
	var buf bytes.Buffer
	ctx := withWindow(withSurface(context.Background(), SurfaceTerminal), window)
	srv.handleHistory(ctx, json.NewEncoder(&buf), req)
	var resp protocol.HistoryResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decoding the history reply %q: %v", buf.String(), err)
	}
	return resp
}

// TWO TERMINAL WINDOWS, ONE FOLDER, ONE DAEMON: each window's chat is its own --
// hydration, the history the model is given, /history's current chat, search --
// and a window with no id (an older terminal, `mochiii -prompt`) has the terminal's
// shared chat beside them. A new chat in one leaves the others as they were.
//
// Neuter check: drop withWindow from serveConn, or make scope() ignore the window.
func TestWindows_TwoTerminalsOnOneFolderKeepSeparateChats(t *testing.T) {
	d := newSyncDaemon(t)
	d.askWin(t, nameTUI, winA, protocol.PromptRequest{Prompt: "WINDOW-A-ONLY question"})
	d.askWin(t, nameTUI, winB, protocol.PromptRequest{Prompt: "WINDOW-B-ONLY question"})
	d.askWin(t, nameTUI, "", protocol.PromptRequest{Prompt: "SHARED-ONLY question"})
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "EDITOR-ONLY question"})

	hydrate := func(name, window string) (string, string) {
		h, rev := d.startWin(t, name, window)
		return turnTexts(h), rev
	}
	hA, revA := hydrate(nameTUI, winA)
	hB, revB := hydrate(nameTUI, winB)
	hS, _ := hydrate(nameTUI, "")
	hV, _ := hydrate(nameVSCode, "")
	for _, c := range []struct{ who, got, want string }{
		{"window A", hA, "WINDOW-A-ONLY"}, {"window B", hB, "WINDOW-B-ONLY"},
		{"the shared terminal chat", hS, "SHARED-ONLY"}, {"the editor", hV, "EDITOR-ONLY"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s's hydration = %q, want it to have %s", c.who, c.got, c.want)
		}
		for _, other := range []string{"WINDOW-A-ONLY", "WINDOW-B-ONLY", "SHARED-ONLY", "EDITOR-ONLY"} {
			if other != c.want && strings.Contains(c.got, other) {
				t.Errorf("%s's hydration holds %s: %q", c.who, other, c.got)
			}
		}
	}
	if revA == revB {
		t.Errorf("two windows have the same revision %q", revA)
	}

	// A window whose copy is behind is answered from ITS stored chat.
	d.askWin(t, nameTUI, winA, protocol.PromptRequest{Prompt: "second A question", ChatRevision: revB})
	if body := d.lastBody(t); !strings.Contains(body, "WINDOW-A-ONLY") || strings.Contains(body, "WINDOW-B-ONLY") {
		t.Errorf("window A's turn was answered from the wrong chat:\n%s", body)
	}

	// /history's current chat, and search.
	cur := askHistoryWin(t, d.srv, winB, protocol.HistoryRequest{Action: protocol.HistoryCurrent})
	if s := turnTexts(cur.Turns); !strings.Contains(s, "WINDOW-B-ONLY") || strings.Contains(s, "WINDOW-A-ONLY") {
		t.Errorf("window B's current chat = %q", s)
	}
	searchWin := func(window, q string) int {
		var buf bytes.Buffer
		ctx := withWindow(withSurface(context.Background(), SurfaceTerminal), window)
		d.srv.handleSearch(ctx, json.NewEncoder(&buf), protocol.SearchRequest{Query: q})
		var resp protocol.SearchResponse
		if err := json.Unmarshal(buf.Bytes(), &resp); err != nil || resp.Error != "" {
			t.Fatalf("search reply %q: %v", buf.String(), err)
		}
		return len(resp.Results)
	}
	if searchWin(winA, "WINDOW-B-ONLY") != 0 || searchWin(winA, "SHARED-ONLY") != 0 || searchWin(winA, "WINDOW-A-ONLY") == 0 {
		t.Error("window A's search crosses into another chat, or misses its own")
	}

	// A new chat in window A: B, the shared chat and the editor are as they were.
	d.askWin(t, nameTUI, winA, protocol.PromptRequest{Reset: true})
	if h, _ := d.startWin(t, nameTUI, winA); len(h) != 0 {
		t.Errorf("window A's chat has %d turns after its reset", len(h))
	}
	if h2, rev2 := hydrate(nameTUI, winB); h2 != hB || rev2 != revB {
		t.Errorf("window A's reset changed window B's chat or revision: %q (%s)", h2, rev2)
	}
	if h2, _ := hydrate(nameTUI, ""); h2 != hS {
		t.Errorf("window A's reset changed the shared terminal chat: %q", h2)
	}
	if h2, _ := hydrate(nameVSCode, ""); h2 != hV {
		t.Errorf("window A's reset changed the editor's chat: %q", h2)
	}
}

// ONLY THE TERMINAL HAS WINDOWS. Two editor windows share the editor's chat --
// that is the point of the catch-up machinery -- whatever id they send; and a
// terminal that sends a malformed id is in the shared terminal chat, not refused.
func TestWindows_TheEditorIgnoresAWindowAndABadIdIsTheSharedChat(t *testing.T) {
	d := newSyncDaemon(t)
	d.askWin(t, nameVSCode, winA, protocol.PromptRequest{Prompt: "EDITOR-ONE"})
	if h, _ := d.startWin(t, nameVSCode, winB); !strings.Contains(turnTexts(h), "EDITOR-ONE") {
		t.Errorf("a second editor window does not see the first's chat: %q", turnTexts(h))
	}
	d.askWin(t, nameTUI, "../../escape", protocol.PromptRequest{Prompt: "TERMINAL-BADID"})
	if h, _ := d.startWin(t, nameTUI, ""); !strings.Contains(turnTexts(h), "TERMINAL-BADID") {
		t.Errorf("a malformed window id did not land in the shared terminal chat: %q", turnTexts(h))
	}
	var n int
	if err := d.srv.memory.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE window_id != ''`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d rows were stored under a window (%v) though no valid terminal window talked", n, err)
	}
}

// A RESET IN ONE WINDOW DOES NOT MOVE ANOTHER'S REVISION, and a revision from one
// window's chat is not accepted as another's.
//
// Neuter check: key chatOf by surface alone; leave the window out of chatEpochFor.
func TestWindows_RevisionsAndLocksAreNotShared(t *testing.T) {
	d := newSyncDaemon(t)
	d.askWin(t, nameTUI, winA, protocol.PromptRequest{Prompt: "A"})
	d.askWin(t, nameTUI, winB, protocol.PromptRequest{Prompt: "B"})
	a, b := winScope(d.srv.workspace, winA), winScope(d.srv.workspace, winB)

	genB := d.srv.chatOf(b).gen.Load()
	revB, err := d.srv.chatRevisionNow(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	d.srv.resetPersistedHistory(a)
	if got := d.srv.chatOf(b).gen.Load(); got != genB {
		t.Errorf("window A's reset moved window B's generation: %d -> %d", genB, got)
	}
	if now, _ := d.srv.chatRevisionNow(context.Background(), b); now != revB {
		t.Errorf("window B's revision changed from %q to %q", revB, now)
	}

	// A revision of A's chat, offered by window B, is not B's.
	d.askWin(t, nameTUI, winA, protocol.PromptRequest{Prompt: "A again"})
	_, revA := d.startWin(t, nameTUI, winA)
	ctxB := withWindow(withSurface(context.Background(), SurfaceTerminal), winB)
	resp, err := d.srv.currentChat(ctxB, revA)
	if err != nil || resp.Append {
		t.Errorf("window A's revision was accepted by window B: append=%v (%v)", resp.Append, err)
	}
	if chatEpochFor(a) == chatEpochFor(b) || !strings.Contains(chatEpochFor(a), winA) {
		t.Errorf("window epochs %q and %q do not tell the windows apart", chatEpochFor(a), chatEpochFor(b))
	}

	// One window's lock does not hold up another's turn; the same window's does.
	d.srv.chatOf(a).mu.Lock()
	released := false
	defer func() {
		if !released {
			d.srv.chatOf(a).mu.Unlock()
		}
	}()
	other := make(chan struct{})
	go func() { d.srv.persistTurn(b, "b q", "b a", nil); close(other) }()
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("window B's turn waited on window A's lock")
	}
	same := make(chan struct{})
	go func() { d.srv.persistTurn(a, "a q", "a a", nil); close(same) }()
	select {
	case <-same:
		t.Fatal("window A's turn was saved while its own lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	d.srv.chatOf(a).mu.Unlock()
	released = true
	select {
	case <-same:
	case <-time.After(5 * time.Second):
		t.Fatal("window A's turn never completed after its lock was released")
	}
}

// chatOf hands every caller the same state for the same chat, however many ask at
// once -- two states for one chat would be two locks for it.
func TestWindows_ChatOfIsOneStatePerChat(t *testing.T) {
	srv := &Server{}
	sc := winScope("/w", winA)
	var wg sync.WaitGroup
	got := make([]*chatState, 32)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i] = srv.chatOf(sc) }()
	}
	wg.Wait()
	for i, st := range got {
		if st != got[0] {
			t.Fatalf("caller %d got a different state for the same chat", i)
		}
	}
	if srv.chatOf(winScope("/w", winB)) == got[0] || srv.chatOf(chatScope{Workspace: "/w", Surface: SurfaceVSCode}) == got[0] {
		t.Error("another chat shares this chat's state")
	}
}

// --- saved chats: the surface's, shared by its windows; the link is the window's ---

// THE SAVED CHATS ARE THE TERMINAL'S, whichever window saved them; the link that
// says which of them the LIVE chat is belongs to a window. So a chat window A saved
// is in window B's list (but not A's live chat), and A's ctrl+n unlinks A only.
//
// Neuter check: have linkFile ignore the window.
func TestWindows_SavedChatsAreSharedAndLinksAreNot(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	ctx := context.Background()
	a, b := winScope(srv.workspace, winA), winScope(srv.workspace, winB)
	for _, sc := range []chatScope{a, b} {
		if err := mem.AppendTurn(ctx, sc, "user", sc.Window[:1]+" question"); err != nil {
			t.Fatal(err)
		}
		if err := mem.AppendTurn(ctx, sc, "assistant", sc.Window[:1]+" answer"); err != nil {
			t.Fatal(err)
		}
	}
	saved := askHistoryWin(t, srv, winA, protocol.HistoryRequest{Action: protocol.HistorySave, Name: "from A"})
	if saved.Error != "" || saved.Entry == nil {
		t.Fatalf("save = %+v", saved)
	}

	root, _ := historyRoot()
	dir := filepath.Join(root, workspaceKey(srv.workspace), "terminal")
	if _, err := os.Stat(filepath.Join(dir, "current-"+winA+".json")); err != nil {
		t.Errorf("window A has no link of its own: %v", err)
	}
	for _, name := range []string{"current.json", "current-" + winB + ".json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s exists after only window A saved: %v", name, err)
		}
	}

	titles := func(window string) (saved []string, current []protocol.HistoryEntry) {
		for _, e := range askHistoryWin(t, srv, window, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries {
			if e.Current {
				current = append(current, e)
			} else {
				saved = append(saved, e.Title)
			}
		}
		return
	}
	if s, _ := titles(winB); strings.Join(s, ",") != "from A" {
		t.Errorf("window B's saved chats = %v, want the one window A saved", s)
	}
	if _, cur := titles(winB); len(cur) != 1 || cur[0].SavedAs != "" || !cur[0].Unsaved {
		t.Errorf("window B's live chat looks saved because window A saved: %+v", cur)
	}
	if _, cur := titles(winA); len(cur) != 1 || cur[0].SavedAs == "" || cur[0].Unsaved {
		t.Errorf("window A's live chat does not know it is saved: %+v", cur)
	}

	srv.resetPersistedHistory(a)
	if _, err := os.Stat(filepath.Join(dir, "current-"+winA+".json")); !os.IsNotExist(err) {
		t.Errorf("window A's link survived its reset: %v", err)
	}
}

// --- the sweep of windows nobody is using ---

func insertAged(t *testing.T, s *MemoryStore, sc chatScope, content string, at time.Time) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO turns (workspace, surface, window_id, role, content, created_at) VALUES (?, ?, ?, 'user', ?, ?)`,
		sc.Workspace, string(sc.Surface), sc.Window, content, at.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func windowTurns(t *testing.T, s *MemoryStore, sc chatScope) int {
	t.Helper()
	got, err := s.LoadAllTurns(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	return len(got)
}

// A WINDOW IDLE FOR LONGER THAN THE TTL LOSES ITS CHAT; one with a recent turn, one
// this daemon has heard from, and every shared chat keep theirs. The link file of a
// window with no chat is removed; a live window's is not.
//
// Neuter check: sweep window "" too; ignore keep(); drop the HAVING; never remove links.
func TestSweep_RemovesIdleWindowsAndOnlyThose(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.logger = discardLogger()
	ws := srv.workspace
	now := time.Now()
	old := now.Add(-(idleWindowTTL + time.Hour))
	recent := now.Add(-time.Hour)

	idle, seen, fresh := winScope(ws, winA), winScope(ws, winB), winScope(ws, winC)
	sharedTerm := winScope(ws, "")
	editor := chatScope{Workspace: ws, Surface: SurfaceVSCode}
	insertAged(t, mem, idle, "idle window", old)
	insertAged(t, mem, seen, "old turns, but the window is in use", old)
	insertAged(t, mem, fresh, "a recent turn", recent)
	insertAged(t, mem, sharedTerm, "the shared terminal chat, old", old)
	insertAged(t, mem, editor, "the editor's chat, old", old)
	srv.windowSeen.Store(winB, now.UnixNano())

	// Link files: the idle window's, the seen window's, the fresh one's, and an
	// orphan whose window has no chat and was never seen.
	root, _ := historyRoot()
	archive := newChatArchive(root, winScope(ws, ""))
	if err := archive.ensureDir(); err != nil {
		t.Fatal(err)
	}
	const orphan = "dddddddddddddddd"
	for _, w := range []string{winA, winB, winC, orphan} {
		if err := os.WriteFile(filepath.Join(archive.dir, "current-"+w+".json"), []byte(`{"id":"20260928T100000Z-0000aaaa","turns":2}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	srv.sweepIdleWindows(now, idleWindowTTL)

	if n := windowTurns(t, mem, idle); n != 0 {
		t.Errorf("the idle window kept %d turns", n)
	}
	for name, sc := range map[string]chatScope{"the window in use": seen, "the fresh window": fresh, "the shared terminal chat": sharedTerm, "the editor's chat": editor} {
		if n := windowTurns(t, mem, sc); n != 1 {
			t.Errorf("%s has %d turns after the sweep, want its 1", name, n)
		}
	}
	exists := func(w string) bool {
		_, err := os.Stat(filepath.Join(archive.dir, "current-"+w+".json"))
		return err == nil
	}
	if exists(winA) || exists(orphan) {
		t.Error("the link file of a window with no chat was left behind")
	}
	if !exists(winB) || !exists(winC) {
		t.Error("the link file of a live window was swept")
	}
	ftsIntegrity(t, mem.db)
}

// THE SWEEP RUNS ONCE AN HOUR, and the first connection a daemon sees starts the
// clock. A connection also records that its window is alive.
//
// Neuter check: drop the throttle (sweep on every connection); never record the window.
func TestSweep_IsThrottledAndRecordsTheWindow(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.logger = discardLogger()
	ws := srv.workspace
	old := time.Now().Add(-(idleWindowTTL + time.Hour))
	idleA := winScope(ws, winA)
	insertAged(t, mem, idleA, "idle", old)

	ctxB := withWindow(withSurface(context.Background(), SurfaceTerminal), winB)
	srv.noteWindowAndSweep(ctxB) // first: sweeps, records B
	if n := windowTurns(t, mem, idleA); n != 0 {
		t.Errorf("the first connection did not sweep: window A kept %d turns", n)
	}
	if !srv.seenSince(winB, time.Now().Add(-time.Minute)) {
		t.Error("the connection's window was not recorded as alive")
	}

	insertAged(t, mem, idleA, "idle again", old)
	srv.noteWindowAndSweep(ctxB) // within the hour: must not sweep
	if n := windowTurns(t, mem, idleA); n != 1 {
		t.Errorf("a second connection inside the hour swept: window A has %d turns, want 1", n)
	}
	srv.lastSweep.Store(time.Now().Add(-2 * sweepEvery).UnixNano())
	srv.noteWindowAndSweep(ctxB) // an hour on: sweeps again
	if n := windowTurns(t, mem, idleA); n != 0 {
		t.Errorf("a connection an hour later did not sweep: window A has %d turns", n)
	}
}

// A WINDOW THAT RESUMED A MONTH-OLD CHAT AND HAS ASKED NOTHING YET is idle by the
// turns' own clock (a resumed turn keeps the time it was first said) but is the
// window being used: the daemon has just heard from it, so it is not swept.
func TestSweep_DoesNotSweepAWindowThatJustResumedAnOldChat(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	srv.logger = discardLogger()
	sc := winScope(srv.workspace, winA)
	ancient := time.Now().Add(-40 * 24 * time.Hour)
	insertAged(t, mem, sc, "a month-old chat just resumed", ancient)
	srv.noteWindowAndSweep(withWindow(withSurface(context.Background(), SurfaceTerminal), winA))
	if n := windowTurns(t, mem, sc); n != 1 {
		t.Errorf("the window in use lost its resumed chat: %d turns", n)
	}
}
