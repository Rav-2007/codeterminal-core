// Package protocol defines the wire messages exchanged between the
// CodeTerminal CLI client and the local daemon over a Unix domain socket.
//
// Framing: newline-delimited JSON. Each message is one JSON object followed
// by "\n". This keeps both sides to stdlib bufio/json with no extra
// dependency, and supports streaming naturally (write-and-flush per line).
package protocol

import (
	"os"
	"path/filepath"
)

// ProtocolVersion is the version implemented by this build. Bump it whenever
// a message shape changes in a way that isn't backward compatible.
const ProtocolVersion = 1

// Discovery convention shared by the daemon (which creates these paths) and
// every client (which must derive the identical paths to find it). Defined
// once here so the two sides can't drift out of sync.
const (
	serviceDirName = "codeterminal"
	socketFileName = "daemon.sock"
	lockFileName   = "daemon.lock"
)

// RuntimeDir returns the per-user directory used for the daemon's socket and
// lockfile: $XDG_RUNTIME_DIR if set, otherwise the OS temp dir.
func RuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// SocketDir returns the directory holding the daemon's socket and lockfile,
// creating it (owner-only) if it doesn't already exist.
func SocketDir() (string, error) {
	dir := filepath.Join(RuntimeDir(), serviceDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// SocketPath and LockPath return the conventional paths for the daemon's
// Unix domain socket and lockfile under SocketDir(), without creating
// anything on disk.
func SocketPath() string { return filepath.Join(RuntimeDir(), serviceDirName, socketFileName) }
func LockPath() string   { return filepath.Join(RuntimeDir(), serviceDirName, lockFileName) }

// HandshakeRequest is the first message a client sends after connecting.
type HandshakeRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	ClientName      string `json:"client_name"`
}

// HandshakeResponse is the daemon's reply to a HandshakeRequest. If Ok is
// false, the daemon closes the connection immediately after sending this;
// the client must not send further messages.
//
// PersistedHistory is the daemon's cross-session conversation memory for
// its own configured workspace (most recent turns, oldest first, already
// re-validated server-side) -- see daemon/memory.go. It is populated on
// EVERY handshake, since the wire protocol is one prompt per connection and
// the daemon has no way to distinguish "this is a fresh client session" at
// handshake time from "this is just the next prompt in an ongoing one".
// That distinction is the CLIENT's responsibility: only a client's own
// startup/preflight connection should hydrate its in-memory transcript from
// this field. A client that re-applied it on every connection (e.g. after
// every prompt during one running session) would duplicate turns it
// already has -- see clients/tui/main.go's runChat, which is the only
// caller that reads this field.
type HandshakeResponse struct {
	ProtocolVersion  int    `json:"protocol_version"`
	Ok               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	DaemonVersion    string `json:"daemon_version,omitempty"`
	PersistedHistory []Turn `json:"persisted_history,omitempty"`
}

// PromptRequest carries a single user prompt. Sent by the client only after
// a successful handshake. Workspace is optional and additive: when set,
// it's the client's own absolute path for the workspace it expects
// grounding against, sent purely so the daemon can report back whether
// that matches its own configured grounding workspace (see
// GroundingInfo.WorkspaceMismatch below). It never changes what the daemon
// actually retrieves from — that's fixed at daemon startup by the daemon's
// own --workspace flag. Older daemons that don't know this field simply
// ignore it.
//
// History is optional and additive: prior turns of this same conversation,
// oldest first, for the daemon to include in the model call ahead of the
// current Prompt. Older daemons that don't know this field simply ignore
// it (identical, one-shot behavior); older clients that don't send it get
// identical behavior to today since a nil/empty History is a no-op.
//
// Reset is optional and additive: when true, Prompt and History are
// ignored entirely -- the daemon clears its persisted cross-session memory
// (see daemon/memory.go) for its own workspace and replies with a single
// TokenResponse{Done: true}, without calling the model at all. A daemon
// built before this field existed doesn't recognize it and will decode
// Reset as its zero value, processing the request as an ordinary (wasted,
// empty-prompt) one-shot call instead of a reset -- harmless (no crash, no
// data corruption), just a discarded round-trip. Daemon and client
// binaries are expected to be rebuilt together, so this mismatch is a
// documented edge case rather than a version-gated one.
//
// PromptKind is optional and additive: an explicit, client-stated signal
// (e.g. the TUI's "/reason "/"/refactor " commands -- see chat.go's
// parsePromptKind) that requests reasoning-tier escalation, fed into
// daemon/router.go's Route() as RouteInput.PromptKind. It is never
// inferred from Prompt's content. The daemon only reacts to a closed,
// exact set of values (see reasoningPromptKinds in daemon/router.go) --
// any other value, including one an older or different client might send,
// is inert and behaves identically to leaving it empty. A daemon built
// before this field existed simply ignores it (routes as if it were
// empty), exactly like Workspace/History above.
type PromptRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Prompt          string `json:"prompt"`
	Workspace       string `json:"workspace,omitempty"`
	History         []Turn `json:"history,omitempty"`
	Reset           bool   `json:"reset,omitempty"`
	PromptKind      string `json:"prompt_kind,omitempty"`
}

// Turn is one prior message in a conversation, supplied by the client so
// the model can see conversation history. Role must be exactly "user" or
// "assistant" — the daemon drops any turn with a different role rather
// than passing it through, so a client can never use History to inject a
// message claiming system-level authority (see daemon/history.go).
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TokenResponse is one message in a streamed reply. The daemon sends, in
// order: at most one message carrying Grounding (before any tokens, once
// retrieval has been decided for this request), a sequence of messages
// with Token set, and exactly one final message with Done set to true
// (Error set instead if the stream failed). Grounding is additive: older
// clients that don't know this field simply ignore it.
//
// EditProposals is additive and carried only on the final (Done) message:
// SEARCH/REPLACE edit blocks the daemon parsed out of the just-completed
// response (see editapply.ParseEditBlocks), surfaced so a client can offer
// to review/apply them without reimplementing the parser itself. It is not
// an invitation to write anything — PrepareEdit's safety gates (exact-match,
// ambiguity-refuse, workspace confinement, secret-file refusal, syntax
// gate) still run only when the client actually sends an ApplyEditRequest
// for one of these. Older clients that don't know this field simply ignore
// it, exactly like Grounding.
//
// Redactions is additive and, like Grounding, carried on its own message
// sent before any tokens: the kinds of secret-shaped text the daemon's
// heuristic scrubber (see daemon/scrub.go) found and replaced in the user's
// prompt before it was sent to the model API, e.g. ["openai_key"]. Never the
// matched text itself. Empty/omitted means nothing was redacted (the common
// case) — not that scrubbing didn't run. Older clients that don't know this
// field simply ignore it.
// ErrorClass, when set alongside Error, names the KIND of failure in a stable
// machine-readable form, so a client can react rather than only display: one of
// "rate_limited", "quota_exceeded", "context_too_large", "auth",
// "upstream_unavailable", "privacy_refused", "unknown". Every upstream failure
// used to arrive as the same opaque "calling model API failed", which left a
// client unable to tell a transient blip from an exhausted quota from a
// conversation too long to ever send.
//
// It classifies without disclosing: the class is DERIVED from upstream detail
// (HTTP status, response body) and never carries it, so no host, URL, provider
// name, status line or raw error text rides along. Older clients that don't
// know this field simply ignore it and keep showing Error, exactly like
// Grounding.
type TokenResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	Token           string          `json:"token,omitempty"`
	Done            bool            `json:"done"`
	Error           string          `json:"error,omitempty"`
	ErrorClass      string          `json:"error_class,omitempty"`
	Grounding       *GroundingInfo  `json:"grounding,omitempty"`
	EditProposals   []EditBlockWire `json:"edit_proposals,omitempty"`
	Redactions      []string        `json:"redactions,omitempty"`
}

// EditBlockWire is the wire form of one parsed SEARCH/REPLACE edit block
// (see editapply.EditBlock) — a plain data mirror with json tags, since the
// daemon-only editapply package has no wire-format concerns of its own and
// protocol must not import it.
type EditBlockWire struct {
	FilePath string `json:"file_path"`
	Search   string `json:"search"`
	Replace  string `json:"replace"`
}

// ApplyEditRequest asks the daemon to apply one edit block through the
// existing editapply safety gates and write it to disk. Sent on its own
// fresh connection (after a HandshakeRequest, same as PromptRequest) —
// unlike PromptRequest, it carries the full edit block content rather than
// an ID, since the daemon keeps no state across connections and a client's
// only record of "which edit" is what it already received in a prior
// TokenResponse.EditProposals. Workspace is optional and additive, same
// convention as PromptRequest.Workspace: the daemon always resolves against
// its own configured workspace root regardless of what's sent here.
//
// BackupSessionDir is optional and additive: when set to a backup session
// directory this same daemon already returned via a prior
// ApplyEditResponse.BackupDir, the daemon reuses it instead of creating a
// fresh one — this is how a client applying several blocks from one
// response (see the VS Code extension's sequential edit review) shares a
// single backup session across all of them, so `edits undo` reverts the
// whole batch at once. Older clients that never send this field get today's
// behavior unchanged: a fresh backup dir per ApplyEditRequest.
type ApplyEditRequest struct {
	ProtocolVersion  int           `json:"protocol_version"`
	Workspace        string        `json:"workspace,omitempty"`
	Edit             EditBlockWire `json:"edit"`
	BackupSessionDir string        `json:"backup_session_dir,omitempty"`
}

// ApplyEditResponse is the daemon's reply to an ApplyEditRequest. Applied is
// false whenever any safety gate refuses the edit or the write itself
// fails; Error then holds that gate's refusal string verbatim (the same
// text editapply.PrepareEdit/Apply would produce for the CLI or TUI) so a
// client can show the identical reason without reimplementing any gate.
// BackupDir is set only when Applied is true.
type ApplyEditResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	Applied         bool   `json:"applied"`
	Error           string `json:"error,omitempty"`
	BackupDir       string `json:"backup_dir,omitempty"`
}

// UndoRequest asks the daemon to revert a backup session -- the same
// restore the CLI's `codeterminal-daemon edits undo` already performs (see
// daemon/apply_cmd.go's runUndoSession), reached over the socket instead of
// a terminal. Sent on its own fresh connection (after a HandshakeRequest,
// same as PromptRequest/ApplyEditRequest).
//
// Undo is the discriminator distinguishing this message from PromptRequest
// and ApplyEditRequest on the wire -- always true, deliberately without
// omitempty, so it's always present in the serialized JSON (mirroring how
// ApplyEditRequest.Edit is always present and non-nil, which is what
// isApplyEditRequest peeks for). Workspace is optional and additive, same
// convention as the other request types: the daemon always resolves
// against its own configured workspace root.
//
// BackupSessionDir is optional: a full path to a backup session directory
// (typically one a prior ApplyEditResponse.BackupDir already returned to
// this same client), confined to <workspace>/.codeterminal/backups and
// still existing on disk. When empty, the daemon reverts the MOST RECENT
// session instead (mirroring `edits undo`'s no --session default) -- but a
// client that already knows its own session dir (like the VS Code panel,
// which received it in a prior ApplyEditResponse) should always send it
// explicitly rather than relying on "most recent", since another client
// could have started a newer apply run against the same workspace in the
// meantime.
type UndoRequest struct {
	ProtocolVersion  int    `json:"protocol_version"`
	Undo             bool   `json:"undo"`
	Workspace        string `json:"workspace,omitempty"`
	BackupSessionDir string `json:"backup_session_dir,omitempty"`
}

// UndoResponse is the daemon's reply to an UndoRequest. Restored is the
// number of files actually reverted -- which includes files the apply run had
// CREATED and undo therefore DELETED (see runUndoSession: reverting a create
// means removing the file, not restoring an empty one). The wire field does not
// distinguish the two, since both answer the question a client asks it, "did
// anything change on disk"; a client that wants to say which files were deleted
// needs a field that does not exist yet. Guarded lists (workspace-relative)
// paths that were left untouched because their on-disk content no longer
// matched the apply run's post-apply snapshot (hand-edited, or otherwise
// changed, since the apply) -- a client MUST surface this list rather than
// imply every file reverted when some were guarded, since runUndoSession
// deliberately never force-overwrites those without an explicit force flag,
// which this request does not expose. SessionDir reports which session
// directory was actually restored, useful when BackupSessionDir was empty
// and the daemon picked "most recent".
//
// Error is set when the session directory couldn't be resolved or validated
// at all (a hard refusal, Restored/Guarded at their zero values), and also
// when the restore itself failed. In that second case Restored and Guarded
// are still populated and still true of disk: the daemon reverts a session as
// one all-or-nothing batch (see runUndoSession), so a failed restore normally
// reports Restored 0 with nothing on disk changed -- but if the batch failed
// midway through its commit phase, Restored is the real, non-zero number of
// files left reverted. A client MUST therefore read Restored even when Error
// is set; treating an error as "nothing happened" is exactly the wrong
// assumption this field exists to prevent.
type UndoResponse struct {
	ProtocolVersion int      `json:"protocol_version"`
	Restored        int      `json:"restored"`
	Guarded         []string `json:"guarded,omitempty"`
	SessionDir      string   `json:"session_dir,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// SearchRequest asks the daemon to run a lexical (FTS5, keyword/substring)
// search over its cross-session conversation memory for its own configured
// workspace — see daemon/search.go's MemoryStore.SearchTurns, the underlying
// implementation this just calls into with no logic of its own. Sent on its
// own fresh connection (after a HandshakeRequest, same as PromptRequest/
// ApplyEditRequest/UndoRequest). This is a purely lexical complement to the
// existing RAG/embedding retrieval — it searches what was actually said in
// past turns, not workspace code, and never touches the vector store.
//
// Search is the discriminator distinguishing this message from the other
// request types on the wire — always true, deliberately without omitempty,
// mirroring UndoRequest.Undo (see its doc comment for why: a real
// SearchRequest must always serialize this key so isSearchRequest's peek
// always catches it, and an older client that doesn't know this message
// only ever sends PromptRequest-shaped JSON with no "search" key, so it
// always falls through to the prompt path unaffected).
//
// Workspace is optional and additive, same convention as every other
// request type: the daemon always resolves against its own configured
// workspace root, never a client-supplied path — a client can't search
// another workspace's history by lying about which one it's asking for.
//
// Limit is optional: when omitted (zero or negative), the daemon applies
// its own default cap (see defaultSearchLimit in server.go), the same
// "client leaves it out, daemon picks a sensible default" shape
// UndoRequest.BackupSessionDir uses for "most recent session".
type SearchRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Search          bool   `json:"search"`
	Workspace       string `json:"workspace,omitempty"`
	Query           string `json:"query"`
	Limit           int    `json:"limit,omitempty"`
}

// SearchResult is one lexical match, ordered by relevance (FTS5's bm25
// rank, most relevant first — see SearchTurns) and carrying enough for a
// client to render an inline snippet: which turn said it (Role, "user" or
// "assistant"), the matched text with surrounding context (Snippet, already
// containing FTS5 snippet() markers a client can turn into highlighting),
// and when (CreatedAt). Deliberately does not carry a turn ID — mirrors
// daemon/search.go's SearchHit exactly (Role/Snippet/CreatedAt only), since
// nothing in this slice's UI plan needs to reference a specific turn back
// again after showing it.
type SearchResult struct {
	Role      string `json:"role"`
	Snippet   string `json:"snippet"`
	CreatedAt string `json:"created_at"`
}

// SearchResponse is the daemon's reply to a SearchRequest. Results is empty
// (nil/omitted) when the search legitimately found nothing — that is NOT an
// error, exactly like SearchTurns's own no-match contract. Error is set
// only for an actual failure: the search couldn't run at all (e.g.
// cross-session memory is disabled or unavailable for this daemon, or the
// underlying query failed) — a client must not conflate "no results" with
// "search failed" the way it must never conflate UndoResponse's guarded
// files with a full revert.
type SearchResponse struct {
	ProtocolVersion int            `json:"protocol_version"`
	Results         []SearchResult `json:"results,omitempty"`
	Error           string         `json:"error,omitempty"`
}

// GroundingInfo reports whether the daemon augmented THIS request with
// retrieved local context, and from where. It's purely a report of a
// decision already made server-side (see daemon/context.go's
// gatherContext) — it never carries retrieved content itself; that stays
// confined to the existing <retrieved_context> user-role delimiter.
type GroundingInfo struct {
	Grounded  bool   `json:"grounded"`
	Workspace string `json:"workspace,omitempty"` // this daemon's actual grounding workspace (absolute path)
	Reason    string `json:"reason,omitempty"`    // set when !Grounded, e.g. "no index found"
	Chunks    int    `json:"chunks,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// WorkspaceMismatch is set when PromptRequest.Workspace was non-empty
	// and didn't match Workspace above — the client expected grounding
	// against a different repo than this daemon instance actually uses.
	WorkspaceMismatch bool `json:"workspace_mismatch,omitempty"`
}

// LockFile is the JSON document the daemon writes on startup so clients can
// discover its socket path without guessing.
type LockFile struct {
	SocketPath string `json:"socket_path"`
	PID        int    `json:"pid"`
}
