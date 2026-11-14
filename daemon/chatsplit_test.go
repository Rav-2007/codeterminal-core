package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// THE SPLIT OF CHAT STORAGE BY CLIENT (chatscope.go,
// docs/CHAT_STORAGE_SPLIT_DESIGN_2026-11-14.md). The terminal and the VS Code
// panel used to write one chat per workspace; a chat is now (workspace,
// surface), and these tests are the rule that nothing one surface says reaches
// the other, through any way a chat is read.

func scopeOn(ws string, s Surface) chatScope { return chatScope{Workspace: ws, Surface: s} }

func appendOrFail(t *testing.T, s *MemoryStore, sc chatScope, role, content string) {
	t.Helper()
	if err := s.AppendTurn(context.Background(), sc, role, content); err != nil {
		t.Fatalf("AppendTurn(%+v): %v", sc, err)
	}
}

func storedContents(turns []storedTurn) string {
	var parts []string
	for _, t := range turns {
		parts = append(parts, t.Content)
	}
	return strings.Join(parts, " | ")
}

func TestSurfaceOf(t *testing.T) {
	for name, want := range map[string]Surface{
		"mochiii-tui":    SurfaceTerminal,
		"mochiii-vscode": SurfaceVSCode,
		"":               SurfaceOther,
		"test":           SurfaceOther,
		"vscode":         SurfaceOther, // the panel's pages fallback name, never a chat's
		"mochiii-tui ":   SurfaceOther, // exact names only: no trimming, no case folding
		"MOCHIII-TUI":    SurfaceOther,
	} {
		if got := surfaceOf(name); got != want {
			t.Errorf("surfaceOf(%q) = %q, want %q", name, got, want)
		}
	}
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode, SurfaceOther} {
		if !s.valid() {
			t.Errorf("%q is not valid", s)
		}
	}
	for _, s := range []Surface{"", "bogus", "Terminal"} {
		if s.valid() {
			t.Errorf("%q is valid", s)
		}
	}
	if surfaceFrom(context.Background()) != SurfaceOther {
		t.Error("a context that came from no connection is not the isolated surface")
	}
	if got := surfaceFrom(withSurface(context.Background(), SurfaceVSCode)); got != SurfaceVSCode {
		t.Errorf("surfaceFrom(withSurface(vscode)) = %q", got)
	}
}

// EVERY METHOD THAT READS A CHAT ANSWERS FOR ONE SURFACE ONLY. The rows are
// written interleaved, so their ids interleave too: a method that filtered by
// workspace alone would return the other client's turns, and one that filtered
// by id alone (turnsAfter) would too.
//
// Neuter check: drop `AND surface = ?` from any one of loadRecentTurnsAt,
// LoadAllTurns, turnsAfter, lastTurnID, ClearWorkspace, ReplaceWorkspace or the
// SearchTurns MATCH -- the assertion for that method fails.
func TestSplit_EveryStoreMethodIsIsolatedPerSurface(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	const ws = "/w"
	term, code, other := scopeOn(ws, SurfaceTerminal), scopeOn(ws, SurfaceVSCode), scopeOn(ws, SurfaceOther)

	appendOrFail(t, s, term, "user", "terminal question needle-t") // id 1
	appendOrFail(t, s, code, "user", "editor question needle-v")   // id 2
	appendOrFail(t, s, term, "assistant", "terminal answer")       // id 3
	appendOrFail(t, s, code, "assistant", "editor answer")         // id 4
	appendOrFail(t, s, other, "user", "somebody else's needle-o")  // id 5

	all := func(sc chatScope) string {
		got, err := s.LoadAllTurns(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		return storedContents(got)
	}
	if got := all(term); got != "terminal question needle-t | terminal answer" {
		t.Errorf("LoadAllTurns(terminal) = %q", got)
	}
	if got := all(code); got != "editor question needle-v | editor answer" {
		t.Errorf("LoadAllTurns(vscode) = %q", got)
	}
	if got := all(other); got != "somebody else's needle-o" {
		t.Errorf("LoadAllTurns(other) = %q", got)
	}

	recent, last, err := s.loadRecentTurnsAt(ctx, term, 50, false)
	if err != nil || len(recent) != 2 || recent[0].Content != "terminal question needle-t" || last != 3 {
		t.Errorf("loadRecentTurnsAt(terminal) = %+v, last %d, err %v; want its own two turns, last id 3", recent, last, err)
	}
	if got, err := s.LoadRecentTurns(ctx, code, 50, false); err != nil || len(got) != 2 || got[1].Content != "editor answer" {
		t.Errorf("LoadRecentTurns(vscode) = %+v, %v", got, err)
	}

	if id, err := s.lastTurnID(ctx, term); err != nil || id != 3 {
		t.Errorf("lastTurnID(terminal) = %d, %v; want 3 (not the editor's 4)", id, err)
	}
	if id, err := s.lastTurnID(ctx, code); err != nil || id != 4 {
		t.Errorf("lastTurnID(vscode) = %d, %v; want 4", id, err)
	}
	// After id 1: the terminal's turn 3 only. Id 2 is the editor's and must not appear.
	if got, last, err := s.turnsAfter(ctx, term, 1); err != nil || storedContents(got) != "terminal answer" || last != 3 {
		t.Errorf("turnsAfter(terminal, 1) = %q, last %d, %v; want only the terminal's own turn", storedContents(got), last, err)
	}

	search := func(sc chatScope, q string) int {
		hits, err := s.SearchTurns(ctx, sc, q, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if search(term, "needle-t") != 1 || search(term, "needle-v") != 0 || search(term, "needle-o") != 0 {
		t.Error("the terminal's search sees the other surfaces' text, or misses its own")
	}
	if search(code, "needle-v") != 1 || search(code, "needle-t") != 0 {
		t.Error("the editor's search sees the terminal's text, or misses its own")
	}
	// A word in BOTH chats: each finds its own row, not two.
	if n := search(term, "question"); n != 1 {
		t.Errorf("search(terminal, \"question\") = %d hits, want 1: the editor's question is not its to find", n)
	}
	if n := search(code, "question"); n != 1 {
		t.Errorf("search(vscode, \"question\") = %d hits, want 1", n)
	}

	// Replacing one surface's chat leaves the other's alone.
	if err := s.ReplaceWorkspace(ctx, term, []storedTurn{{Role: "user", Content: "replaced"}}); err != nil {
		t.Fatal(err)
	}
	if got := all(term); got != "replaced" {
		t.Errorf("after ReplaceWorkspace the terminal's chat = %q", got)
	}
	if got := all(code); got != "editor question needle-v | editor answer" {
		t.Errorf("ReplaceWorkspace(terminal) reached the editor's chat: %q", got)
	}
	// Clearing one leaves the other's.
	if err := s.ClearWorkspace(ctx, term); err != nil {
		t.Fatal(err)
	}
	if got := all(term); got != "" {
		t.Errorf("after ClearWorkspace the terminal's chat = %q", got)
	}
	if got := all(code); got != "editor question needle-v | editor answer" {
		t.Errorf("ClearWorkspace(terminal) erased the editor's chat: %q", got)
	}
	if got := all(other); got == "" {
		t.Error("ClearWorkspace(terminal) erased the isolated surface's chat")
	}
	ftsIntegrity(t, s.db)
}

// RETENTION IS PER CHAT. A terminal that talks past the cap evicts its own
// oldest turns and none of the editor's.
//
// Neuter check: drop `AND surface = ?` from pruneWorkspace's DELETE.
func TestSplit_RetentionIsPerChat(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	term, code := scopeOn("/w", SurfaceTerminal), scopeOn("/w", SurfaceVSCode)
	appendOrFail(t, s, code, "user", "the editor's first")
	appendOrFail(t, s, code, "assistant", "the editor's second")
	for i := 0; i < maxTurnsPerWorkspace+5; i++ {
		appendOrFail(t, s, term, "user", fmt.Sprintf("terminal %d", i))
	}
	tt, err := s.LoadAllTurns(ctx, term)
	if err != nil || len(tt) != maxTurnsPerWorkspace {
		t.Errorf("the terminal holds %d turns (%v), want the cap %d", len(tt), err, maxTurnsPerWorkspace)
	}
	if ct, err := s.LoadAllTurns(ctx, code); err != nil || len(ct) != 2 {
		t.Errorf("the editor holds %d turns (%v) after the terminal passed the cap, want its own 2", len(ct), err)
	}
}

// A SCOPE WITH NO SURFACE, OR AN UNKNOWN ONE, IS AN ERROR FROM EVERY METHOD --
// never a query. The zero value of a surface is the one thing that must not
// reach SQL: `surface = ”` would match nothing today and whatever a future
// writer puts there tomorrow.
func TestSplit_AnEmptyOrUnknownSurfaceIsRefusedEverywhere(t *testing.T) {
	s, _ := openTestMemoryStore(t)
	ctx := context.Background()
	for _, bad := range []Surface{"", "bogus", "Terminal"} {
		sc := scopeOn("/w", bad)
		checks := map[string]error{
			"AppendTurn":       s.AppendTurn(ctx, sc, "user", "x"),
			"ReplaceWorkspace": s.ReplaceWorkspace(ctx, sc, nil),
			"ClearWorkspace":   s.ClearWorkspace(ctx, sc),
			"pruneWorkspace":   s.pruneWorkspace(ctx, sc),
		}
		_, _, e1 := s.loadRecentTurnsAt(ctx, sc, 5, false)
		_, e2 := s.LoadRecentTurns(ctx, sc, 5, false)
		_, e3 := s.lastTurnID(ctx, sc)
		_, _, e4 := s.turnsAfter(ctx, sc, 0)
		_, e5 := s.LoadAllTurns(ctx, sc)
		_, e6 := s.SearchTurns(ctx, sc, "x", 5)
		checks["loadRecentTurnsAt"], checks["LoadRecentTurns"], checks["lastTurnID"] = e1, e2, e3
		checks["turnsAfter"], checks["LoadAllTurns"], checks["SearchTurns"] = e4, e5, e6
		for name, err := range checks {
			if !errors.Is(err, errBadSurface) {
				t.Errorf("%s with surface %q = %v, want errBadSurface", name, bad, err)
			}
		}
		a := newChatArchive(filepath.Join(t.TempDir(), "history"), sc)
		if err := a.ensureDir(); !errors.Is(err, errBadSurface) {
			t.Errorf("an archive for surface %q made its folder: %v", bad, err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused write left %d rows (%v)", n, err)
	}
}

// buildV5 writes a schema-v5 database by hand: the turns table without a
// surface, the workspace index, and the v5 search index (external content,
// workspace only) with its triggers.
func buildV5(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE schema_meta (version INTEGER NOT NULL)`,
		`INSERT INTO schema_meta (version) VALUES (5)`,
		legacyTurnsTableDDL,
		legacyTurnsWorkspaceIndexDDL,
		`CREATE VIRTUAL TABLE turns_fts USING fts5(content, workspace UNINDEXED, tokenize='trigram', content='turns', content_rowid='id')`,
		`CREATE TRIGGER turns_ai AFTER INSERT ON turns BEGIN
			INSERT INTO turns_fts(rowid, content, workspace) VALUES (new.id, new.content, new.workspace); END`,
		`CREATE TRIGGER turns_ad AFTER DELETE ON turns BEGIN
			INSERT INTO turns_fts(turns_fts, rowid, content, workspace) VALUES ('delete', old.id, old.content, old.workspace); END`,
		`INSERT INTO turns (id, workspace, role, content, created_at) VALUES (10, '/a', 'user', 'alpha question', '2026-10-01T10:00:00Z')`,
		`INSERT INTO turns (id, workspace, role, content, created_at) VALUES (11, '/a', 'assistant', 'alpha answer', '2026-10-01T10:00:01Z')`,
		`INSERT INTO turns (id, workspace, role, content, created_at) VALUES (12, '/b', 'user', 'beta question', '2026-10-02T10:00:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("building the v5 fixture: %v\n%s", err, stmt)
		}
	}
}

// A v5 DATABASE BECOMES v6 IN PLACE: every turn kept with its id, and every one
// of them the editor's -- the owner's decision for what existed before the split.
// The search index is rebuilt around the new column, its triggers carry it, and
// nothing of the old shape is left.
//
// Neuter check: make migrateToScopedTurns insert 'terminal' instead of 'vscode';
// skip the backfill; or leave the old index in place.
func TestSplit_AV5DatabaseMigratesInPlaceToTheEditor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	buildV5(t, path)

	s, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("opening a v5 database: %v", err)
	}
	defer s.Close()
	if v := pragmaIntQuery(t, s.db, `SELECT version FROM schema_meta`); v != 6 {
		t.Errorf("schema version %d, want 6", v)
	}
	rows, err := s.db.Query(`SELECT id, workspace, surface FROM turns ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id int
		var ws, surface string
		if err := rows.Scan(&id, &ws, &surface); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%s:%s", id, ws, surface))
	}
	rows.Close()
	if want := "10:/a:vscode 11:/a:vscode 12:/b:vscode"; strings.Join(got, " ") != want {
		t.Errorf("rows after migration = %v, want ids kept and every one the editor's: %s", got, want)
	}

	ctx := context.Background()
	if hits, err := s.SearchTurns(ctx, scopeOn("/a", SurfaceVSCode), "alpha", 10); err != nil || len(hits) != 2 {
		t.Errorf("the editor's search after migration: %d hits (%v), want 2: the index was not backfilled", len(hits), err)
	}
	if hits, err := s.SearchTurns(ctx, scopeOn("/a", SurfaceTerminal), "alpha", 10); err != nil || len(hits) != 0 {
		t.Errorf("the terminal found %d of the editor's migrated turns (%v)", len(hits), err)
	}
	if hits, err := s.SearchTurns(ctx, scopeOn("/b", SurfaceVSCode), "beta", 10); err != nil || len(hits) != 1 {
		t.Errorf("a second workspace's migrated turn: %d hits (%v)", len(hits), err)
	}

	for _, gone := range []string{"idx_turns_workspace_id", "turns_v5"} {
		if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE name = '`+gone+`'`); n != 0 {
			t.Errorf("%s survived the migration", gone)
		}
	}
	if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_turns_scope_id'`); n != 1 {
		t.Error("the scope index was not created")
	}
	var ddl string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'turns_fts'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "surface") {
		t.Errorf("turns_fts was not rebuilt around the surface column: %q (%v)", ddl, err)
	}

	// The new triggers carry the surface: a turn written after the migration is
	// found, and one deleted is forgotten by the index.
	appendOrFail(t, s, scopeOn("/a", SurfaceTerminal), "user", "gamma written after")
	if hits, _ := s.SearchTurns(ctx, scopeOn("/a", SurfaceTerminal), "gamma", 10); len(hits) != 1 {
		t.Errorf("a turn written after the migration is not searchable: %d hits", len(hits))
	}
	if hits, _ := s.SearchTurns(ctx, scopeOn("/a", SurfaceVSCode), "gamma", 10); len(hits) != 0 {
		t.Error("the new trigger indexed the turn under the wrong surface")
	}
	if err := s.ClearWorkspace(ctx, scopeOn("/a", SurfaceTerminal)); err != nil {
		t.Fatal(err)
	}
	ftsIntegrity(t, s.db)

	// NO DEFAULT: a writer that does not name the surface -- an older daemon
	// still running -- fails loudly instead of landing in the editor's chat.
	if _, err := s.db.Exec(`INSERT INTO turns (workspace, role, content, created_at) VALUES ('/a', 'user', 'x', 'now')`); err == nil {
		t.Error("an insert that omits the surface succeeded: it would land in someone's chat")
	}
}

// A migrated database opened again is left alone: version 6 runs nothing.
func TestSplit_AMigratedDatabaseIsNotMigratedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	buildV5(t, path)
	s, err := OpenMemoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	appendOrFail(t, s, scopeOn("/a", SurfaceTerminal), "user", "after the migration")
	s.Close()

	s, err = OpenMemoryStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer s.Close()
	if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM turns`); n != 4 {
		t.Errorf("%d turns after reopening, want 4: a second open changed the data", n)
	}
	if n := pragmaIntQuery(t, s.db, `SELECT COUNT(*) FROM turns WHERE surface = 'terminal'`); n != 1 {
		t.Errorf("%d terminal turns after reopening, want the 1 written after the migration", n)
	}
}

// TWO DAEMONS MIGRATING AT ONCE. memory.db is shared by the daemons of every
// workspace on the machine, so two can both read "version 5" and both migrate.
// The first to take the write lock does the work; the other waits, reads v6
// again inside its transaction, and does nothing. Without that, the second
// either fails (a stale read cannot be upgraded in WAL mode) or migrates twice.
//
// Neuter check: drop the `UPDATE schema_meta SET version = version` that takes
// the lock first, or the re-read inside the transaction.
func TestSplit_TwoDaemonsMigratingAtOnceMigrateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	buildV5(t, path)

	const daemons = 4
	var wg sync.WaitGroup
	errs := make(chan error, daemons)
	start := make(chan struct{})
	for i := 0; i < daemons; i++ {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA busy_timeout=10000`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- ensureMemorySchema(db)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a daemon failed to open the database while another migrated it: %v", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := pragmaIntQuery(t, db, `SELECT version FROM schema_meta`); v != 6 {
		t.Errorf("version %d after four concurrent opens, want 6", v)
	}
	if n := pragmaIntQuery(t, db, `SELECT COUNT(*) FROM turns`); n != 3 {
		t.Errorf("%d turns after four concurrent migrations, want the original 3 exactly once", n)
	}
	if n := pragmaIntQuery(t, db, `SELECT COUNT(*) FROM turns WHERE surface = 'vscode'`); n != 3 {
		t.Errorf("%d vscode turns, want 3", n)
	}
	ftsIntegrity(t, db)
}

// A DAEMON THAT READ "VERSION 5" BEFORE ANOTHER MIGRATED, AND GOT THE WRITE LOCK
// AFTER IT, MUST FIND THE DATABASE ALREADY MIGRATED AND LEAVE IT ALONE. Running the
// rebuild again would relabel every row -- including the terminal's, written since --
// as the editor's. The concurrency test above cannot see that: its fixture makes
// every row the editor's anyway. This is the late migrator, on its own.
//
// Neuter check: drop the re-read of the version inside migrateToScopedTurns.
func TestSplit_ALateMigratorLeavesAMigratedDatabaseAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	buildV5(t, path)
	s, err := OpenMemoryStore(path) // the first daemon: migrates
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	appendOrFail(t, s, scopeOn("/a", SurfaceTerminal), "user", "terminal row written after the migration")
	appendOrFail(t, s, scopeOn("/a", SurfaceOther), "user", "isolated row written after the migration")

	// The second daemon, which had already read version 5, now takes its turn.
	if err := migrateToScopedTurns(s.db); err != nil {
		t.Fatalf("the late migrator failed instead of finding v6: %v", err)
	}
	for surface, want := range map[string]int{"terminal": 1, "other": 1, "vscode": 3} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE surface = ?`, surface).Scan(&n); err != nil || n != want {
			t.Errorf("%d rows on %q after a late migrator ran, want %d (%v): it relabelled a migrated database", n, surface, want, err)
		}
	}
	ftsIntegrity(t, s.db)
}

// --- the server: two clients on one workspace, two chats ---

// connectAs is syncDaemon.connect with a client name, which is what decides
// whose chat a connection is about.
func (d *syncDaemon) connectAs(t *testing.T, name string) (*json.Encoder, *json.Decoder, protocol.HandshakeResponse, func()) {
	t.Helper()
	return d.connectWindow(t, name, "")
}

// connectWindow also sends a window id, as a terminal window does on every connection.
func (d *syncDaemon) connectWindow(t *testing.T, name, window string) (*json.Encoder, *json.Decoder, protocol.HandshakeResponse, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		d.srv.serveConn(serverConn)
		_ = serverConn.Close()
		close(done)
	}()
	_ = clientConn.SetDeadline(time.Now().Add(10 * time.Second))
	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: name, WindowID: window}); err != nil {
		t.Fatal(err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil || !hs.Ok {
		t.Fatalf("handshake = %+v (err %v)", hs, err)
	}
	return enc, dec, hs, func() { _ = clientConn.Close(); <-done }
}

func (d *syncDaemon) startAs(t *testing.T, name string) ([]protocol.Turn, string) {
	t.Helper()
	_, _, hs, closeConn := d.connectAs(t, name)
	closeConn()
	return hs.PersistedHistory, hs.ChatRevision
}

func (d *syncDaemon) askAs(t *testing.T, name string, req protocol.PromptRequest) protocol.TokenResponse {
	t.Helper()
	enc, dec, _, closeConn := d.connectAs(t, name)
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

func askHistoryAs(t *testing.T, srv *Server, surface Surface, req protocol.HistoryRequest) protocol.HistoryResponse {
	t.Helper()
	req.Chats = true
	var buf bytes.Buffer
	srv.handleHistory(withSurface(context.Background(), surface), json.NewEncoder(&buf), req)
	var resp protocol.HistoryResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decoding the history reply %q: %v", buf.String(), err)
	}
	return resp
}

const (
	nameTUI    = "mochiii-tui"
	nameVSCode = "mochiii-vscode"
)

// THE TEST THE DESIGN ASKED FOR: a real daemon, two handshakes, one workspace.
// What one client says is not in the other's handshake hydration, not in the
// history the model is given for the other's turn, not in the other's /history,
// and not in the other's search; and a new chat in one leaves the other's alone.
//
// Neuter check: make scope() ignore the connection's surface (return SurfaceOther
// for everything shares nothing, so use SurfaceTerminal), or drop withSurface
// from serveConn -- the first assertion that crosses surfaces fails.
func TestSplit_TwoClientsOnOneWorkspaceKeepSeparateChats(t *testing.T) {
	d := newSyncDaemon(t)

	if h, _ := d.startAs(t, nameTUI); len(h) != 0 {
		t.Fatalf("a fresh terminal starts with %d turns", len(h))
	}
	d.askAs(t, nameTUI, protocol.PromptRequest{Prompt: "TERMINAL-ONLY question"})
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "EDITOR-ONLY question"})

	// Hydration: each client's handshake carries its own chat.
	termTurns, termRev := d.startAs(t, nameTUI)
	codeTurns, codeRev := d.startAs(t, nameVSCode)
	if s := turnTexts(termTurns); !strings.Contains(s, "TERMINAL-ONLY") || strings.Contains(s, "EDITOR-ONLY") {
		t.Errorf("the terminal's hydration = %q", s)
	}
	if s := turnTexts(codeTurns); !strings.Contains(s, "EDITOR-ONLY") || strings.Contains(s, "TERMINAL-ONLY") {
		t.Errorf("the editor's hydration = %q", s)
	}
	if termRev == "" || termRev == codeRev {
		t.Errorf("the two chats have revisions %q and %q; they must be told apart", termRev, codeRev)
	}

	// What the model is given: a turn from a client whose copy is behind is
	// answered from the STORED chat -- this surface's.
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "second editor question", ChatRevision: termRev})
	if body := d.lastBody(t); !strings.Contains(body, "EDITOR-ONLY") || strings.Contains(body, "TERMINAL-ONLY") {
		t.Errorf("the editor's turn was answered from the wrong chat:\n%s", body)
	}
	d.askAs(t, nameTUI, protocol.PromptRequest{Prompt: "second terminal question", ChatRevision: "stale"})
	if body := d.lastBody(t); !strings.Contains(body, "TERMINAL-ONLY") || strings.Contains(body, "EDITOR-ONLY") {
		t.Errorf("the terminal's turn was answered from the wrong chat:\n%s", body)
	}

	// /history: the current chat is this surface's.
	for _, c := range []struct {
		surface Surface
		want    string
		not     string
	}{{SurfaceTerminal, "TERMINAL-ONLY", "EDITOR-ONLY"}, {SurfaceVSCode, "EDITOR-ONLY", "TERMINAL-ONLY"}} {
		cur := askHistoryAs(t, d.srv, c.surface, protocol.HistoryRequest{Action: protocol.HistoryCurrent})
		if s := turnTexts(cur.Turns); !strings.Contains(s, c.want) || strings.Contains(s, c.not) {
			t.Errorf("%s's current chat = %q", c.surface, s)
		}
		list := askHistoryAs(t, d.srv, c.surface, protocol.HistoryRequest{Action: protocol.HistoryList, Query: c.not})
		if len(list.Entries) != 0 {
			t.Errorf("%s's /history filtered for %q found %d chat(s): the other client's chat", c.surface, c.not, len(list.Entries))
		}
	}

	// /search.
	searchAs := func(surface Surface, q string) int {
		var buf bytes.Buffer
		d.srv.handleSearch(withSurface(context.Background(), surface), json.NewEncoder(&buf), protocol.SearchRequest{Query: q})
		var resp protocol.SearchResponse
		if err := json.Unmarshal(buf.Bytes(), &resp); err != nil || resp.Error != "" {
			t.Fatalf("search reply %q: %v", buf.String(), err)
		}
		return len(resp.Results)
	}
	if searchAs(SurfaceVSCode, "TERMINAL-ONLY") != 0 || searchAs(SurfaceTerminal, "EDITOR-ONLY") != 0 {
		t.Error("a search found the other client's turns")
	}
	if searchAs(SurfaceVSCode, "EDITOR-ONLY") == 0 || searchAs(SurfaceTerminal, "TERMINAL-ONLY") == 0 {
		t.Error("a search missed a client's own turns")
	}

	// A new chat in the terminal (ctrl+n, and the terminal's fresh start) leaves
	// the editor's chat as it was.
	d.askAs(t, nameTUI, protocol.PromptRequest{Reset: true})
	if h, _ := d.startAs(t, nameTUI); len(h) != 0 {
		t.Errorf("the terminal's chat has %d turns after a reset", len(h))
	}
	if h, rev := d.startAs(t, nameVSCode); !strings.Contains(turnTexts(h), "EDITOR-ONLY") || rev != d.revOf(t, nameVSCode) {
		t.Errorf("the terminal's reset changed the editor's chat: %q", turnTexts(h))
	}
}

func (d *syncDaemon) revOf(t *testing.T, name string) string {
	t.Helper()
	_, rev := d.startAs(t, name)
	return rev
}

// A RESET IN ONE SURFACE DOES NOT MOVE THE OTHER'S REVISION. The generation is
// per surface: a daemon-wide one made the editor redraw its chat whenever the
// terminal started a new one, and the terminal does that on every launch.
//
// Neuter check: make chatOf return one shared state for every surface.
func TestSplit_AResetInOneSurfaceLeavesTheOthersRevisionAlone(t *testing.T) {
	d := newSyncDaemon(t)
	d.askAs(t, nameTUI, protocol.PromptRequest{Prompt: "terminal"})
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "editor"})
	term, code := scopeOn(d.srv.workspace, SurfaceTerminal), scopeOn(d.srv.workspace, SurfaceVSCode)

	genT, genV := d.srv.chatOf(term).gen.Load(), d.srv.chatOf(code).gen.Load()
	revV, err := d.srv.chatRevisionNow(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	if rev := d.srv.resetPersistedHistory(term); rev == "" {
		t.Fatal("the reset reported no revision")
	}
	if got := d.srv.chatOf(term).gen.Load(); got != genT+1 {
		t.Errorf("the terminal's generation = %d, want %d", got, genT+1)
	}
	if got := d.srv.chatOf(code).gen.Load(); got != genV {
		t.Errorf("the terminal's reset moved the editor's generation: %d -> %d", genV, got)
	}
	if now, _ := d.srv.chatRevisionNow(context.Background(), code); now != revV {
		t.Errorf("the editor's revision changed from %q to %q", revV, now)
	}
}

// A REVISION FROM THE OTHER SURFACE IS NOT ACCEPTED. It parses as "not this
// chat's", so the client is sent the whole chat -- never an append onto the
// wrong one. The surface's letter in the epoch is what does it.
//
// Neuter check: have chatEpochFor return chatEpoch for every surface.
func TestSplit_ARevisionFromTheOtherSurfaceIsNotAccepted(t *testing.T) {
	d := newSyncDaemon(t)
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "editor one"})
	d.askAs(t, nameVSCode, protocol.PromptRequest{Prompt: "editor two"})
	ctx := withSurface(context.Background(), SurfaceVSCode)

	_, own := d.startAs(t, nameVSCode)
	resp, err := d.srv.currentChat(ctx, own)
	if err != nil || !resp.Append || len(resp.Turns) != 0 {
		t.Errorf("the editor's own revision: append=%v, %d turns, %v; want an append of nothing", resp.Append, len(resp.Turns), err)
	}

	r, ok := parseChatRevision(own)
	if !ok {
		t.Fatalf("cannot parse the revision %q", own)
	}
	foreign := chatRevision{epoch: chatEpochFor(scopeOn("", SurfaceTerminal)), gen: r.gen, last: r.last}.String()
	resp, err = d.srv.currentChat(ctx, foreign)
	if err != nil || resp.Append || len(resp.Turns) != 4 {
		t.Errorf("a terminal revision offered to the editor: append=%v, %d turns, %v; want the whole chat (4 turns)", resp.Append, len(resp.Turns), err)
	}
	if !strings.Contains(resp.ChatRevision, "-v.") {
		t.Errorf("the editor's revision %q does not carry its surface", resp.ChatRevision)
	}
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode, SurfaceOther} {
		for _, o := range []Surface{SurfaceTerminal, SurfaceVSCode, SurfaceOther} {
			a, b := chatEpochFor(scopeOn("", s)), chatEpochFor(scopeOn("", o))
			if (a == b) != (s == o) {
				t.Errorf("epochs for %s and %s: equal=%v", s, o, a == b)
			}
		}
	}
}

// ONE SURFACE'S LOCK DOES NOT BLOCK THE OTHER'S TURN. /compact holds it across a
// model call; the editor's answer must still be saved while the terminal waits
// on one. The same surface DOES block, which is what the lock is for.
//
// Neuter check: make chatOf return one shared state.
func TestSplit_OneSurfacesLockDoesNotBlockTheOthersTurn(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	term, code := scopeOn(srv.workspace, SurfaceTerminal), scopeOn(srv.workspace, SurfaceVSCode)

	srv.chatOf(term).mu.Lock()
	released := false
	defer func() {
		if !released {
			srv.chatOf(term).mu.Unlock()
		}
	}()

	other := make(chan struct{})
	go func() { srv.persistTurn(code, "editor q", "editor a", nil); close(other) }()
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("the editor's turn waited on the terminal's lock")
	}

	same := make(chan struct{})
	go func() { srv.persistTurn(term, "terminal q", "terminal a", nil); close(same) }()
	select {
	case <-same:
		t.Fatal("the terminal's turn was saved while its own lock was held")
	case <-time.After(200 * time.Millisecond):
	}
	srv.chatOf(term).mu.Unlock()
	released = true
	select {
	case <-same:
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal's turn never completed after its lock was released")
	}
}

// --- saved chats ---

// SAVED CHATS LIVE IN THE SURFACE'S OWN FOLDER, and so does the link to which
// of them the live chat is. A chat one client saved is not in the other's list,
// cannot be resumed by it, and the other's ctrl+n does not unlink it.
//
// Neuter check: drop the surface from newChatArchive's dir.
func TestSplit_SavedChatsAreSeparatePerSurface(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	ctx := context.Background()
	term, code := scopeOn(srv.workspace, SurfaceTerminal), scopeOn(srv.workspace, SurfaceVSCode)
	for _, sc := range []chatScope{term, code} {
		if err := mem.AppendTurn(ctx, sc, "user", string(sc.Surface)+" question"); err != nil {
			t.Fatal(err)
		}
		if err := mem.AppendTurn(ctx, sc, "assistant", string(sc.Surface)+" answer"); err != nil {
			t.Fatal(err)
		}
	}

	savedT := askHistoryAs(t, srv, SurfaceTerminal, protocol.HistoryRequest{Action: protocol.HistorySave, Name: "T"})
	savedV := askHistoryAs(t, srv, SurfaceVSCode, protocol.HistoryRequest{Action: protocol.HistorySave, Name: "V"})
	if savedT.Error != "" || savedV.Error != "" || savedT.Entry == nil || savedV.Entry == nil {
		t.Fatalf("saves: %+v / %+v", savedT, savedV)
	}

	root, _ := historyRoot()
	key := filepath.Join(root, workspaceKey(srv.workspace))
	for _, surface := range []string{"terminal", "vscode"} {
		files, _ := filepath.Glob(filepath.Join(key, surface, "*"+archiveSuffix))
		if len(files) != 1 {
			t.Errorf("%s's folder holds %d saved chats, want 1", surface, len(files))
		}
		if _, err := os.Stat(filepath.Join(key, surface, currentLinkName)); err != nil {
			t.Errorf("%s has no current.json: %v", surface, err)
		}
	}

	titles := func(surface Surface) string {
		var out []string
		for _, e := range askHistoryAs(t, srv, surface, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries {
			if !e.Current {
				out = append(out, e.Title)
			}
		}
		return strings.Join(out, ",")
	}
	if got := titles(SurfaceTerminal); got != "T" {
		t.Errorf("the terminal's saved chats = %q, want T", got)
	}
	if got := titles(SurfaceVSCode); got != "V" {
		t.Errorf("the editor's saved chats = %q, want V", got)
	}

	// The terminal cannot read or resume the editor's saved chat.
	if r := askHistoryAs(t, srv, SurfaceTerminal, protocol.HistoryRequest{Action: protocol.HistoryShow, ID: savedV.Entry.ID}); r.Error == "" {
		t.Error("the terminal read the editor's saved chat")
	}
	if r := askHistoryAs(t, srv, SurfaceTerminal, protocol.HistoryRequest{Action: protocol.HistoryResume, ID: savedV.Entry.ID}); r.Error == "" {
		t.Error("the terminal resumed the editor's saved chat")
	}
	if got, _ := mem.LoadAllTurns(ctx, term); storedContents(got) != "terminal question | terminal answer" {
		t.Errorf("the refused resume changed the terminal's chat: %q", storedContents(got))
	}

	// A new chat in the terminal unlinks the terminal's, not the editor's.
	srv.resetPersistedHistory(term)
	if _, err := os.Stat(filepath.Join(key, "terminal", currentLinkName)); !os.IsNotExist(err) {
		t.Errorf("the terminal's link survived its reset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(key, "vscode", currentLinkName)); err != nil {
		t.Errorf("the terminal's reset removed the editor's link: %v", err)
	}
	for _, e := range askHistoryAs(t, srv, SurfaceVSCode, protocol.HistoryRequest{Action: protocol.HistoryList}).Entries {
		if e.Current && (e.SavedAs == "" || e.Unsaved) {
			t.Errorf("the editor's current chat lost its saved copy after the terminal's reset: %+v", e)
		}
	}
}

// --- migrating what was saved before the split ---

func legacyArchiveAt(t *testing.T, root, ws string) *chatArchive {
	t.Helper()
	keyDir := filepath.Join(root, workspaceKey(ws))
	return &chatArchive{root: root, keyDir: keyDir, dir: keyDir, scope: tscope(ws)}
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func surfaceDirOf(root, ws string, s Surface) string {
	return filepath.Join(root, workspaceKey(ws), string(s))
}

const idA, idB = "20260928T100000Z-0000aaaa", "20260928T110000Z-0000bbbb"

// EVERY SAVED CHAT FROM BEFORE THE SPLIT IS COPIED INTO BOTH CLIENTS' FOLDERS,
// byte for byte, and the link to the live chat goes to the editor only (the live
// chat went there). Nothing is left in the old place.
//
// Neuter check: copy to the terminal only; copy the link to both; or skip the
// removal of the originals.
func TestMigrateLegacyArchives_CopiesToBothAndLinksTheEditor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	const ws = "/workspace/legacy"
	legacy := legacyArchiveAt(t, root, ws)
	pa := writeRawArchive(t, legacy, idA, archiveHeader{V: archiveVersion, Title: "first", Bookmarked: true}, nil)
	pb := writeRawArchive(t, legacy, idB, archiveHeader{V: archiveVersion, Title: "second"}, nil)
	if err := legacy.writeLink(idB, 2); err != nil {
		t.Fatal(err)
	}
	wantA, wantB := readFileOrFail(t, pa), readFileOrFail(t, pb)

	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatal(err)
	}

	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		dir := surfaceDirOf(root, ws, s)
		if !bytes.Equal(readFileOrFail(t, filepath.Join(dir, idA+archiveSuffix)), wantA) ||
			!bytes.Equal(readFileOrFail(t, filepath.Join(dir, idB+archiveSuffix)), wantB) {
			t.Errorf("%s's copies are not byte-identical to the originals", s)
		}
	}
	if _, err := os.Stat(filepath.Join(surfaceDirOf(root, ws, SurfaceVSCode), currentLinkName)); err != nil {
		t.Errorf("the editor has no link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(surfaceDirOf(root, ws, SurfaceTerminal), currentLinkName)); !os.IsNotExist(err) {
		t.Errorf("the terminal was given the editor's link: %v", err)
	}
	if _, err := os.Stat(surfaceDirOf(root, ws, SurfaceOther)); !os.IsNotExist(err) {
		t.Errorf("a folder was made for the isolated surface: %v", err)
	}
	for _, name := range []string{idA + archiveSuffix, idB + archiveSuffix, currentLinkName} {
		if _, err := os.Stat(filepath.Join(root, workspaceKey(ws), name)); !os.IsNotExist(err) {
			t.Errorf("the original %s was left in the old place: %v", name, err)
		}
	}
	// The bookmark is in the file, so it came along.
	if h, err := newChatArchive(root, scopeOn(ws, SurfaceTerminal)).list(); err != nil || len(h) != 2 {
		t.Errorf("the terminal lists %d chats (%v), want 2", len(h), err)
	}
}

// Nothing to migrate is nothing done: no folder is made for a workspace that
// never saved a chat, and running it again changes nothing.
func TestMigrateLegacyArchives_IsIdempotentAndQuietWhenThereIsNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	if err := migrateLegacyArchives(root, "/never/saved"); err != nil {
		t.Fatalf("no folder at all: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("migrating nothing made %s: %v", root, err)
	}

	const ws = "/workspace/twice"
	legacy := legacyArchiveAt(t, root, ws)
	writeRawArchive(t, legacy, idA, archiveHeader{V: archiveVersion, Title: "x"}, nil)
	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatal(err)
	}
	before := readFileOrFail(t, filepath.Join(surfaceDirOf(root, ws, SurfaceTerminal), idA+archiveSuffix))
	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatalf("the second run: %v", err)
	}
	if !bytes.Equal(before, readFileOrFail(t, filepath.Join(surfaceDirOf(root, ws, SurfaceTerminal), idA+archiveSuffix))) {
		t.Error("the second run changed a copy")
	}
	if got, _ := newChatArchive(root, scopeOn(ws, SurfaceVSCode)).list(); len(got) != 1 {
		t.Errorf("the editor lists %d chats after two runs, want 1 (no duplicates)", len(got))
	}
}

// A RUN THAT DIED PART-WAY IS FINISHED BY THE NEXT. The terminal already has one
// copy (and it is not overwritten: what is there is what the user may have
// since updated), the editor has none, and the originals are all still in place.
//
// Neuter check: make copyIfAbsent overwrite; or remove the originals before the
// copies.
func TestMigrateLegacyArchives_FinishesAnInterruptedRun(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	const ws = "/workspace/interrupted"
	legacy := legacyArchiveAt(t, root, ws)
	writeRawArchive(t, legacy, idA, archiveHeader{V: archiveVersion, Title: "a"}, nil)
	writeRawArchive(t, legacy, idB, archiveHeader{V: archiveVersion, Title: "b"}, nil)

	term := newChatArchive(root, scopeOn(ws, SurfaceTerminal))
	if err := term.ensureDir(); err != nil {
		t.Fatal(err)
	}
	marker := []byte("updated since by the terminal")
	if err := os.WriteFile(filepath.Join(term.dir, idA+archiveSuffix), marker, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatal(err)
	}
	if got := readFileOrFail(t, filepath.Join(term.dir, idA+archiveSuffix)); !bytes.Equal(got, marker) {
		t.Error("the terminal's existing copy was overwritten")
	}
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		for _, id := range []string{idA, idB} {
			if _, err := os.Stat(filepath.Join(surfaceDirOf(root, ws, s), id+archiveSuffix)); err != nil {
				t.Errorf("%s is missing %s after the finishing run: %v", s, id, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, workspaceKey(ws), idA+archiveSuffix)); !os.IsNotExist(err) {
		t.Errorf("the original survived: %v", err)
	}
}

// A saved chat an OLDER daemon writes into the old layout later is moved the
// next time -- the two can share a state folder. A symlink in the old place is
// never followed, and a temp file a dead save left is cleared.
func TestMigrateLegacyArchives_HealsLaterArrivalsAndNeverFollowsALink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	const ws = "/workspace/heal"
	legacy := legacyArchiveAt(t, root, ws)
	writeRawArchive(t, legacy, idA, archiveHeader{V: archiveVersion, Title: "a"}, nil)
	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatal(err)
	}

	// Later: an older daemon saves chat B the old way, a save dies, and someone
	// plants a link named like a chat.
	writeRawArchive(t, legacy, idB, archiveHeader{V: archiveVersion, Title: "b"}, nil)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("not a chat"), 0o600); err != nil {
		t.Fatal(err)
	}
	const idC = "20260929T100000Z-0000cccc"
	linkName := filepath.Join(root, workspaceKey(ws), idC+archiveSuffix)
	if err := os.Symlink(secret, linkName); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	stale := filepath.Join(root, workspaceKey(ws), ".tmp-12345")
	if err := os.WriteFile(stale, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyArchives(root, ws); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		if _, err := os.Stat(filepath.Join(surfaceDirOf(root, ws, s), idB+archiveSuffix)); err != nil {
			t.Errorf("%s did not get the chat an older daemon saved later: %v", s, err)
		}
		if _, err := os.Lstat(filepath.Join(surfaceDirOf(root, ws, s), idC+archiveSuffix)); !os.IsNotExist(err) {
			t.Errorf("the planted link was followed into %s: %v", s, err)
		}
	}
	if got := readFileOrFail(t, secret); string(got) != "not a chat" {
		t.Errorf("the file the link pointed at was changed: %q", got)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the dead save's temp file was left: %v", err)
	}
}

// THROUGH THE SERVER: the first time a daemon touches the archive it moves the
// legacy chats, and each client then lists them -- the isolated surface does not.
func TestSplit_TheServerMigratesSavedChatsOnFirstUse(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	srv := historyServer(t, mem)
	root, _ := historyRoot()
	legacy := legacyArchiveAt(t, root, srv.workspace)
	writeRawArchive(t, legacy, idA, archiveHeader{V: archiveVersion, Title: "from before the split"}, nil)

	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		list := askHistoryAs(t, srv, s, protocol.HistoryRequest{Action: protocol.HistoryList})
		if len(list.Entries) != 1 || list.Entries[0].Title != "from before the split" {
			t.Errorf("%s lists %+v, want the chat saved before the split", s, list.Entries)
		}
	}
	if list := askHistoryAs(t, srv, SurfaceOther, protocol.HistoryRequest{Action: protocol.HistoryList}); len(list.Entries) != 0 {
		t.Errorf("the isolated surface lists %d chats from before the split", len(list.Entries))
	}
}

// pruneHistory walks the surface folders (a flat walk found nothing there once
// the chats moved down a level and pruning silently stopped), and still prunes
// files left directly under a workspace's folder until they are migrated.
//
// Neuter check: make pruneHistory stop at the workspace folder.
func TestPruneHistory_ReachesSurfaceFoldersAndUnmigratedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "history")
	const ws = "/workspace/prune"
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		a := newChatArchive(root, scopeOn(ws, s))
		for i := 0; i < 4; i++ {
			if _, err := a.save(exchange(fmt.Sprintf("%s %d", s, i), "ok"), "", "", base.Add(time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacy := legacyArchiveAt(t, root, ws)
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("2026091%dT100000Z-0000dddd", i)
		writeRawArchive(t, legacy, id, archiveHeader{V: archiveVersion, Title: "old"}, nil)
	}
	// keep 2 per folder: two surface folders and the unmigrated one each lose 2.
	if n := pruneHistory(root, 2, 1<<30, time.Now()); n != 6 {
		t.Errorf("pruneHistory removed %d, want 6 (2 from each of the three folders)", n)
	}
	for _, s := range []Surface{SurfaceTerminal, SurfaceVSCode} {
		if got, _ := newChatArchive(root, scopeOn(ws, s)).list(); len(got) != 2 {
			t.Errorf("%s keeps %d chats, want 2", s, len(got))
		}
	}
}
