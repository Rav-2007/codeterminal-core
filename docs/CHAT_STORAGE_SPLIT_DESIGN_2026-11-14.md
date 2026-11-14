# Splitting chat storage by client (terminal, VS Code)

Status: **Built** on branch `feat/chat-storage-split`, 2026-11-14, from `ff9f900`.
This began as the design and is kept as the record of it; section 9 lists where
the build differs. Every statement about "today" in sections 1-2 was checked
against the files named beside it, at that commit.

Decided by the owner on 2026-11-14:

- **One database, one new column.** Keep `memory.db`; add a required `surface`
  column to `turns`. Saved chats get a folder per surface.
- **Existing chats:** the live (unsaved) chat becomes VS Code's. Every saved chat
  is **copied** into both clients' lists, so nothing anyone saved disappears
  from either.
- **Each terminal window has its own chat** (asked as section 8's first question,
  answered yes the same day). This adds one optional handshake field, `window_id`,
  and a `window_id` column beside `surface`. The editor's windows still share one
  chat. Section 3.8 is this part.

## 1. Why

The terminal and the VS Code panel read and write **one** conversation. The
daemon is one process per workspace, it serves every client of that folder, and
the chat is keyed by workspace alone.

That was a deliberate choice ([`OPEN_ITEMS.md`](OPEN_ITEMS.md) item 48 built the
catch-up machinery to make the sharing work), and it has a cost the terminal's
new behaviour exposes. Since the terminal now starts a fresh chat on every
launch (`clients/tui/freshchat.go`, in PR #106 at the time of writing), a terminal
launch, or leaving it with `q`, **erases the editor's unsaved chat too**, and
the daemon has no notion of whose chat it is. The owner's requirement is that
each client has its own chat, its own saved chats, and its own search.

## 2. What exists today

| Piece | Where | Keyed by |
|---|---|---|
| Live chat | `memory.db`, table `turns(id, workspace, role, content, created_at)`, index `(workspace, id)`, schema **v5** | `workspace` |
| Search over the chat | `turns_fts`, FTS5, external content (`content='turns'`), `workspace UNINDEXED`, trigram | `workspace` |
| Saved chats | `StateDir()/history/<workspaceKey>/<id>.jsonl.gz` and one `current.json` link | workspace folder |
| Revision | `epoch.generation.lastTurnID` ([`daemon/chatsync.go`](../daemon/chatsync.go)); one `chatGen` atomic and one `historyMu` mutex on the `Server` | the daemon |
| Retention | 1000 turns per workspace; 50 saved chats and 20 MiB per workspace folder | workspace |

All of [`daemon/memory.go`](../daemon/memory.go)'s methods take `workspace string`:
`AppendTurn`, `loadRecentTurnsAt`, `LoadAllTurns`, `turnsAfter`, `lastTurnID`,
`ReplaceWorkspace`, `ClearWorkspace`, `SearchTurns`. The history handler
([`daemon/chatarchive_handler.go`](../daemon/chatarchive_handler.go)) ignores
`HistoryRequest.Workspace` and always uses the daemon's own.

**How a client is told apart.** Only by the handshake's `client_name`. The
terminal connects as `"mochiii-tui"` (`terminalClientName` in
[`daemon/mcpplaces.go`](../daemon/mcpplaces.go), which already carries the name
into a turn's context). VS Code connects as `"mochiii-vscode"`
(`CLIENT_NAME` in [`clients/vscode/src/chatPanel.ts`](../clients/vscode/src/chatPanel.ts));
every chat, history, reset and preflight call in that file passes it. The one
other name the panel uses, a `'vscode'` fallback at the pages context, is for
the MCP pages and never reaches a chat call.

## 3. Design

### 3.1 Surface

A closed type with three values:

| Surface | Which clients |
|---|---|
| `terminal` | `client_name == "mochiii-tui"` (the interactive app and `mochiii -prompt`) |
| `vscode` | `client_name == "mochiii-vscode"` |
| `other` | any other name: its own isolated chat |

`other` exists so that an unknown or future client can **never** read or write
either real chat. It is not a fallback onto one of them.

The surface is **derived by the daemon from the handshake**'s `client_name`: no
protocol field, no client change for the split between the terminal and the editor.
(Per-window chats, 3.8, add one optional field; there is still no version bump.)
A `chatScope{Workspace, Surface, Window}` is built once per connection and passed
to every chat and storage call.

### 3.2 Schema v6 (`memory.db`)

```sql
CREATE TABLE turns (
    id         INTEGER PRIMARY KEY,
    workspace  TEXT NOT NULL,
    surface    TEXT NOT NULL,          -- no DEFAULT, on purpose (3.7)
    window_id  TEXT NOT NULL,          -- '' = the surface's shared chat (3.8)
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_turns_scope_id ON turns(workspace, surface, window_id, id);

CREATE VIRTUAL TABLE turns_fts USING fts5(
    content,
    workspace UNINDEXED,
    surface   UNINDEXED,               -- search filters inside the scan, like workspace
    window_id UNINDEXED,
    tokenize='trigram', content='turns', content_rowid='id'
);
-- turns_ai / turns_ad triggers carry surface (the delete trigger must hand the
-- external-content index the same values it indexed).
```

- **No `CHECK` on the value set.** The closed set is enforced in Go. A `CHECK`
  would make adding a client later a table rebuild.
- **No `DEFAULT`.** See 3.7: a writer that forgets the column must fail, not
  land in somebody's chat.
- `turns.id` stays a global rowid. Within one `(workspace, surface)` it is still
  strictly increasing, which is all the revision scheme needs.

**Migration v5 → v6**, one transaction, keeping `ensureMemorySchema`'s
sequential-`if` shape. The transaction is `BEGIN IMMEDIATE` and **re-reads
`schema_meta` inside it**: the daemons of different workspaces share this one
file and can start together, and the check-then-migrate in `ensureMemorySchema`
today is not inside a transaction, so two of them could both see v5. The loser
waits on `busy_timeout`, then sees v6 and does nothing. Steps:

1. Create `turns_v6` as above.
2. `INSERT INTO turns_v6 (id, workspace, surface, role, content, created_at) SELECT id, workspace, 'vscode', role, content, created_at FROM turns` (ids preserved).
3. Drop the old triggers, `turns_fts` and `turns`; rename `turns_v6` to `turns`; create the new index.
4. Rebuild the search index: this is the existing v5 block
   (`DROP TRIGGER`, `DROP TABLE turns_fts`, `createSearchIndex`,
   `backfillSearchIndex`) with its guard widened from `version < 5` to
   `version < 6`.
5. `schema_meta` = 6.

The v4 purge (`version < 4`) must not run again; a test pins that.

**Retention** becomes per `(workspace, surface)`. `memory.go` already argues
the point at workspace level ("a global cap lets one busy workspace evict
another's history"); it holds one level down: a busy terminal must not evict
VS Code's history.

### 3.3 The compiler enforces the split

Every `MemoryStore` method that takes `workspace string` takes `chatScope`
instead, and the store returns an error for an empty or unknown surface. A
caller that forgets the surface does not compile. Call sites to convert:
`server.go` (`persistTurn`, `resetPersistedHistory`, `loadPersistedHistoryAt`,
search, the save path), `chatarchive_handler.go`, `chatcompact.go`,
`chatsync.go`. The store takes the scope as an argument, so the zero value of a surface can
never reach a query by omission. (The connection's own scope is read once from
its context, by `Server.scope`; see section 9 for why that is not an argument
all the way down.)

### 3.4 Revision and locking, per surface

Today one `chatGen` and one `historyMu` serve the whole daemon. Left that way, a
`/clear` in the terminal would bump the generation the VS Code panel is
comparing against, and the panel would redraw its chat for no reason; and
`/compact`, which holds `historyMu` across a model call, would stop the other
client's turn from being saved.

- `chatGen` and `historyMu` become one pair per chat (a `sync.Map` keyed by surface
  and window, made on first use -- the design said a fixed array per surface, until
  windows made the number of chats unbounded).
- The revision's **epoch carries the surface and the window** (`<epoch>-t`,
  `<epoch>-v`, `<epoch>-o`, and `<epoch>-t-<window>` for a terminal window). A revision from the other surface fails the existing epoch check
  and parses as "not this chat's", so the client is sent the whole chat, never
  an append onto the wrong one. This is the same protection the epoch already
  gives against a restarted daemon.
- `lastTurnID` and `turnsAfter` filter by `(workspace, surface)`.

### 3.5 Saved chats

```
history/<workspaceKey>/
  terminal/
    current.json            which saved chat the TERMINAL's live chat is
    <id>.jsonl.gz
  vscode/
    current.json
    <id>.jsonl.gz
  other/
```

`newChatArchive(root, scope)`. The 50-chat bound applies per surface folder, because
each folder is a separate list the user manages. The link is per LIVE chat, because
"which saved chat is the live chat" is a property of a live chat: `current.json`
for a surface's shared chat and `current-<window>.json` for a terminal window, in
the surface's folder. The saved chats themselves are the surface's, shared by its
windows. Bookmarks live in each file's header, so after the copy in 3.6 a
chat bookmarked in one client is not bookmarked in the other.

### 3.6 Migrating what exists

Run the first time a daemon process uses the archive (section 9). Each workspace
has its own folder and its own single daemon, so unlike the database migration
(3.2) there is no concurrent migrator.

1. For every legacy `history/<key>/<id>.jsonl.gz`: copy it into `terminal/` and
   into `vscode/`, atomically (a temp file, then a rename), skipping a name that
   is already there.
2. Legacy `current.json` copies to `vscode/` only: it describes the live chat,
   which is now VS Code's.
3. Remove the legacy files, last.

**There is no marker file.** What is legacy is whatever still sits directly under
`history/<key>/` as a saved chat, and step 3 removes it, so the state is its own
record: a crash anywhere leaves a state the next run completes, and a run that
finds nothing does nothing. It also heals: a chat an *older* daemon saves into the
old layout later (the two can share a state folder) is moved the next time. A
symlink in the old place is never followed.

Disk use for saved chats roughly doubles in the worst case. The byte bound
(20 MiB) is the whole history folder's, as it always was (`pruneHistory`), so a
user already near it would see the oldest chats pruned after the copy; the count
bound (50) is per saved-chat folder. Measured on the author's real history: three
workspaces, 140 KB in total, nowhere near either.

The live chat needs no step of its own: it is the `'vscode'` in the table
migration.

### 3.7 Mixed-version installs

There is one daemon per workspace (its lockfile is named from the workspace,
`protocol.WorkspaceTag`), and every client of that folder uses it. But the daemons
of *different* workspaces share one `memory.db`, and which binary started a given
daemon depends on who opened the folder first: the terminal's installed binary or
the VS Code extension's bundled one. So two daemons on one machine can be
different versions of the same file.

- **An older daemon opening a v6 database** refuses it
  (`version > memorySchemaVersion`) and runs with conversation memory off. The
  daemon logs why. This is `setupMemoryStore`'s existing graceful degradation,
  and the same cost the v4 → v5 bump had.
- **An older daemon that is already running** (for another workspace) when a
  newer one migrates the file keeps reading, but its inserts omit `surface` and fail the `NOT NULL`
  constraint: its turns are not saved, loudly, until it restarts. With a
  `DEFAULT 'vscode'` they would have succeeded and silently landed in the
  editor's chat. Failing is the better of the two, and it is why the column has
  no default.
- To verify before release: whether the terminal's existing restart-on-upgrade
  path ([`clients/tui/upgraderestart_test.go`](../clients/tui/upgraderestart_test.go))
  covers a daemon that is older than the client, which would shorten that window.

### 3.8 A chat for each terminal window

**The id.** The terminal app makes 8 random bytes (16 lowercase hex digits) when it
starts and sends them as `window_id` in every handshake it makes
(`clients/tui/window.go`, `connectToDaemon`). It is never written to disk. A new
launch is a new window. `mochiii -prompt` sends none.

**What the daemon accepts.** `windowFor(surface, id)`: the id if the surface is the
terminal and the id is exactly 8 to 32 lowercase hex digits, otherwise `""`, the
surface's shared chat. A bad id is ignored, not refused, so a client's bug cannot
become a handshake failure; the store and the archive both refuse a scope whose
window is malformed or on a surface with none (`errBadWindow`). The pattern
matters because the id becomes part of a file name (`current-<window>.json`).
Only the terminal has windows: the editor's windows share one chat on purpose.

**Everything keyed by the chat takes the window:** the turns, the retention cap (per
window), the search index, the revision's epoch, the lock and the replacement count,
and the link file. The saved chats do not: they are the terminal's, in one folder.

**Windows that were closed.** The daemon is never told a window closed (a connection
lasts one request), so a closed window's chat would stay forever, one per launch. A
sweep (`chatwindow.go`), at most once an hour and on the first connection a daemon
sees, deletes the turns of a terminal window whose newest turn is older than seven
days, and the link file of a window with no chat. Two guards: the shared chats are
never swept, and a window this daemon process has heard from within the TTL is not,
whatever its turns' age -- a resumed month-old chat keeps its turns' original times,
so the window that just resumed one is idle by that clock and is the one in use.
Seven days because the cost of being wrong is a live chat disappearing under an
open window.

**Migration.** Every row that exists becomes `window_id = ''`; there is nothing to
migrate in the archive.

## 4. Retrieval, per surface

| Path | After the split |
|---|---|
| Handshake `PersistedHistory` and `chat_revision` | this surface's recent turns and revision |
| `HistoryCurrent` catch-up, and the stale-client backstop ("a prompt from a copy that is behind is answered from the stored chat") | this surface's chat |
| `/history` list, save, resume, delete, bookmark, compact | this surface's folder and live chat |
| `/search` (`turns_fts`) | this surface's turns |
| History sent to the model with a prompt | this surface's chat |

**Not split, on purpose:** the code index (`chunk_index`, the lexical and vector
stores) describes the repository, not a conversation; user rules (`/remember`),
the spend ledger, task ledgers and specs are project-level. Both clients should
see the same ones.

Clients need no change. The terminal's catch-up on focus and VS Code's catch-up
on focus stay; each now sees only its own surface's changes, which still covers
two VS Code windows on one folder.

## 5. What this reverses, and what it leaves

- **Reverses** the premise of `OPEN_ITEMS.md` item 48 ("one chat per workspace,
  shared by every client"), on purpose. That item's catch-up code stays, scoped
  to a surface. The implementation PR rewrites the "Conversation memory" and
  "Chat history" passages of [`REFERENCE.md`](REFERENCE.md) that describe a
  shared chat, and marks item 48 as superseded.
- **Removes** the cross-client cost of the terminal's fresh-start (section 1).
- **Gives** each terminal window its own chat (3.8), so two terminals on one folder
  no longer share one. The editor's windows still do, and need the catch-up
  machinery for it.
- **Changes** what a terminal relaunch is: a new window has a new id, so a
  restarted terminal opens an empty chat and does not resume the one before it.
  The chat it left is swept after seven days, and `/save` and `/resume` are how
  a chat survives. That is the fresh start the terminal asked for, delivered by
  the storage rather than by erasing.

## 6. Test plan (for the implementation PR)

- **Isolation, per method.** Rows written as `terminal` are invisible to
  `vscode` and the reverse through `loadRecentTurnsAt`, `LoadAllTurns`,
  `turnsAfter`, `lastTurnID`, `SearchTurns`; `ReplaceWorkspace` and
  `ClearWorkspace` on one leave the other intact.
- **Retention** is per scope: filling the terminal's cap evicts none of VS Code's.
- **Migration.** Seed a real v5 file: rows become `vscode` with ids preserved;
  search still finds them after the rebuild; the old index and triggers are gone;
  the v4 purge does not re-run; a fresh database is created at v6.
- **Rejection.** An empty or unknown surface is an error from every method.
- **Revision and lock.** A reset in one surface does not change the other's
  generation; a revision from the other surface is not accepted; a held
  `historyMu` for one surface does not block the other's `persistTurn`.
- **Archives.** Separate folders; `current.json` per surface; the legacy
  migration (copied to both, link to `vscode`, idempotent, interrupted after
  each step, bounds respected).
- **Real daemon, two handshakes** (`mochiii-tui`, `mochiii-vscode`) on one
  workspace: a turn written by one, then read by the other through the handshake,
  `/history`, and `/search`, is empty.
- **Neuter each rule** on a scratch copy (never in place): drop the surface
  filter from one query at a time, drop the epoch suffix, share the mutex, skip
  the legacy copy, write the marker first.
- **Gates**: daemon lint, errcheck ceiling (the daemon sits at its ceiling, so no
  new unchecked error), the coverage floors, `GOOS=windows go vet`, the docs gates.

## 7. Shape of the change

One PR, from `main`, in this order of commits:

1. Schema v6, `chatScope`, the store methods and their tests.
2. Server wiring: scope per connection, per-surface revision and lock.
3. Saved-chat folders, the migration, the docs and the OPEN_ITEMS note.

Daemon only. The terminal and VS Code clients, and the wire protocol, are
unchanged.

## 8. Open questions (unchanged by the build)

1. ~~Should two terminal windows in one folder share a chat?~~ Answered by the
   owner, 2026-11-14: each has its own. Built (3.8).
2. `other` is persisted and bounded like the others. Is an isolated, bounded
   chat the right thing for an unknown client, or should it be memoryless?
3. Release notes need a line on mixed versions (3.7). Is a minimum-daemon check
   in the extension and the installer worth adding for this release?

## 9. What the build changed from this design

1. **The connection's surface rides in its context.** `serveConn` sets it once from
   the handshake (`withSurface`), and `Server.scope(ctx)` reads it. Threading a
   scope argument through `runAgentTurn` and the handler chain would have changed
   every test that calls them for no gain: those paths already carry the client
   name in the context. A context with no surface is `other` (never a real chat),
   and the store itself still takes a `chatScope` and refuses a bad surface.
2. **The saved-chat migration runs on first use, not at daemon start**, once per
   process (`sync.Once` in `archiveForScope`), and needs no marker (3.6).
3. **`pruneHistory` had to change, and the design missed it.** It walked one level
   below the history root, so once chats sat two levels down it would have pruned
   nothing, silently. The tests caught it. It now treats each surface folder, and
   any unmigrated files directly under a workspace folder, as a unit.
4. **v1-v4 migration steps collapsed into the v6 rebuild.** The v4 purge still runs
   first (it is `version < 4`); the v2, v3 and v5 index steps are gone, because the
   v6 rebuild drops and recreates the table and the index from the current
   definitions and an index built one statement earlier would name a column the
   old table lacks.
5. **A pre-existing test leaked the developer's real home.**
   `TestInstallPathDaemonStartsWithNoShellEnvironment` starts the daemon, built from
   the source under test, with `HOME` set to the real one. On a schema bump that
   daemon opens the developer's real `memory.db` and migrates it. It did, on the
   author's machine, during this build; the database was rolled back (all 256 rows
   kept) and the test now gets a temporary home. Any test that starts a real daemon
   must be handed a state folder of its own.
6. **Measured on a copy of the author's real database** (v5, 256 turns in 56
   workspaces): every turn kept with its id, the search index rebuilt and passing
   its integrity check, and an insert in the 1.5.1 daemon's shape (no `surface`)
   refused by the new table, as 3.7 predicts.
7. **Windows (3.8) came after the first build and changed it in four places.** The
   scope is `(workspace, surface, window)`; `chatState` moved from a fixed array
   to a map; `chatEpochFor` takes the scope; and the schema is still v6 -- the
   `window_id` column was folded into the same migration, because v6 had not shipped.
8. **The window field is named `window_id`, not `session_id`:** `PromptRequest.Session`
   already means a spending session.

