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
// History, when set, rides on the same pre-token message as Grounding and
// reports what the daemon did to the conversation turns the client sent (see
// HistoryInfo). Additive: older clients that don't know this field ignore it.
//
// Reasoning carries a reasoning-tier model's thinking tokens, streamed as they
// arrive on messages of their own. It is a SEPARATE field from Token, not a
// flavour of it: Token accumulates into the answer that gets parsed for edit
// blocks and written to conversation memory, and thinking must never enter
// that. A client may render it (a "thinking" area), collapse it, or ignore it
// entirely; what it must NOT do is append it to the reply text. Before this
// existed the daemon read only delta.content, so reasoning tokens were decoded
// and discarded and the user watched an empty screen while the model thought.
// Additive: older clients that don't know this field ignore it, which is the
// pre-existing behaviour.
// Degraded, when set, rides on the same pre-token message as Grounding and
// names every subsystem currently running in a REDUCED mode (see Degradation).
// It is the wire half of a pattern this daemon had systematically: internal
// state degraded gracefully and was reported honestly to stderr, while the
// response on the socket stayed byte-identical to a healthy one. A client --
// and therefore a user -- had no way to learn that hybrid retrieval had fallen
// back to semantic-only, or that conversation memory had stopped persisting,
// short of tailing the daemon's log and knowing what to look for.
//
// Empty/omitted means nothing is degraded, which is the common case. Additive:
// older clients that don't know this field simply ignore it, exactly like
// Grounding and Redactions.
//
// Incomplete, when set on the final (Done) message, reports that the model's
// answer was CUT OFF rather than finishing on its own -- the daemon observed a
// terminal SSE finish_reason that was not a natural "stop" (e.g. "length", the
// model hitting its output-token ceiling mid-sentence). Before this existed the
// daemon decoded only delta.content and never looked at finish_reason at all, so
// a truncated answer returned a Done message byte-identical to a complete one:
// the client rendered a sentence that stops mid-word as if it were the whole
// reply, with nothing to say it had been cut off. A client MUST render its
// presence as a visibly incomplete state (see IncompleteInfo), distinct from a
// finished answer.
//
// It covers ONLY the truncation the daemon can actually see -- an upstream-side
// output limit reported in the stream. A connection drop or a mid-stream daemon
// exit surfaces to the client as a transport close, not as this field (the
// daemon that died cannot annotate its own final message); those remain the
// client's to distinguish, and are a separate concern from this one. Empty/
// omitted is the common case (a natural "stop"). Additive: older clients that
// don't know this field simply ignore it, exactly like Grounding and Provider.
//
// Provider names the upstream provider OpenRouter reports as having served this
// turn (e.g. "DeepInfra"), surfaced from the daemon's own log-only observation
// (see daemon/provider.go's chatCompletionChunk.Provider / onProvider). Unlike
// Grounding, it is NOT known before tokens: the value only exists once the
// model response begins, so it rides on its OWN message, sent as soon as the
// provider is first observed in the stream (which in practice is at or before
// the first token). It is a plain factual "served by X" — deliberately NOT a
// judgement about ZDR status (that depends on the still-open provider allow-list
// and outreach) and deliberately NOT a claim about whether a fallback occurred
// (OpenRouter reports who served a request, not whether that was primary or a
// fallback — see BACKLOG's fallback-provider ZDR posture entry). Empty/omitted
// is a legitimate, non-error state: OpenRouter does not formally guarantee the
// field on every response (daemon/provider.go:67-73), so a turn may simply have
// no provider to show — clients must render its absence as nothing, never as a
// degraded/error state. Additive: older clients that don't know this field
// simply ignore it, exactly like Grounding and Redactions.
type TokenResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	Token           string          `json:"token,omitempty"`
	Done            bool            `json:"done"`
	Error           string          `json:"error,omitempty"`
	ErrorClass      string          `json:"error_class,omitempty"`
	Reasoning       string          `json:"reasoning,omitempty"`
	Grounding       *GroundingInfo  `json:"grounding,omitempty"`
	History         *HistoryInfo    `json:"history,omitempty"`
	EditProposals   []EditBlockWire `json:"edit_proposals,omitempty"`
	Redactions      []string        `json:"redactions,omitempty"`
	Degraded        []Degradation   `json:"degraded,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	Incomplete      *IncompleteInfo `json:"incomplete,omitempty"`
}

// Stable, machine-readable reason slugs for IncompleteInfo.Reason. Named for
// the same reason ErrorClass and the Degraded* constants are: a client should be
// able to branch on WHICH kind of truncation happened (to word it, style it, or
// offer the specific remedy -- "ask me to continue" for a length cutoff) without
// string-matching prose that may later be reworded. The values are the OpenAI/
// OpenRouter finish_reason vocabulary, which is provider-neutral (it names the
// model's stopping condition, never a host, URL, or account) so surfacing it
// raises no Gate-7 disclosure concern.
const (
	// IncompleteLength: the model stopped because it hit its output-token
	// ceiling, not because it was done -- the answer is cut off mid-generation.
	IncompleteLength = "length"

	// IncompleteContentFilter: the provider's content filter halted generation
	// mid-stream, so the answer is partial.
	IncompleteContentFilter = "content_filter"
)

// IncompleteInfo reports that a streamed answer ended early rather than
// naturally, in the same report-a-decision-already-made spirit as GroundingInfo
// and HistoryInfo: a conclusion (Reason) plus a client-safe, human-readable
// explanation (Detail), never the internal detail behind it. Detail follows the
// Fix 8 / Degradation discipline -- it names WHAT happened and what it means for
// the user, and never a path, host, provider name, or raw upstream error string.
type IncompleteInfo struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// Stable, machine-readable component identifiers for Degradation.Component.
// Named constants for the same reason TokenResponse.ErrorClass has them: a
// client should be able to branch on WHICH subsystem is degraded (to style it,
// suppress it, or offer the specific remedy) without string-matching prose
// that may be reworded.
const (
	// DegradedLexicalRetrieval: the FTS5 keyword tier is unavailable while the
	// semantic tier still works. Retrieval continues, at reduced quality --
	// hybrid vector+lexical fusion collapses to semantic-only, which is exactly
	// the case terse, identifier-heavy code is worst served by.
	DegradedLexicalRetrieval = "lexical_retrieval"

	// DegradedMemory: cross-session conversation memory is unavailable. The
	// current conversation still works in full; nothing about it will survive
	// the client closing. Note this is only invisible on the PROMPT path --
	// SearchResponse.Error has always reported it correctly for searches.
	DegradedMemory = "memory"

	// DegradedProviderRouting: the configured provider-routing constraints are
	// weaker than the secure default (see ZDRConfig), so a request may be
	// served by an endpoint outside the zero-data-retention guarantee.
	//
	// Deliberately derived from CONFIGURATION, not from any given response:
	// OpenRouter reports which provider served a request but does NOT report
	// whether that provider was reached via a fallback, so a per-request "this
	// one fell back" signal would be fabricated. This says the honest, weaker,
	// checkable thing: fallbacks are PERMITTED for this daemon.
	DegradedProviderRouting = "provider_routing"
)

// Degradation names one subsystem running in a reduced mode, in the same
// report-a-decision-already-made spirit as GroundingInfo and HistoryInfo: it
// carries a conclusion, never the internal detail behind it.
//
// Component is a stable slug from the Degraded* constants above. Detail is a
// client-safe, human-readable explanation of what is reduced AND what that
// costs the user -- it deliberately follows the Fix 8 discipline established
// for retrieval's DisabledReason: it names WHAT is wrong and what it means,
// and never a path, host, or internal error string. Those stay in the daemon
// log, which keeps the full diagnostic.
type Degradation struct {
	Component string `json:"component"`
	Detail    string `json:"detail"`
}

// HistoryInfo reports what the daemon did with the conversation turns a
// client sent in PromptRequest.History — the history-side counterpart of
// GroundingInfo, and deliberately the same shape of report: a decision already
// made server-side (see daemon/history.go's prepareHistory), never the turn
// content itself.
//
// Truncated is the field this type exists for. The daemon caps history by turn
// count AND by total bytes, dropping the oldest turns first; before this flag
// a client had no way to know that had happened, so "what was my first
// question?" could be answered confidently and wrongly from a conversation
// whose beginning had been silently dropped on the way out. It mirrors
// GroundingInfo.Truncated exactly, so the two asymmetric halves of one request
// now report the same way.
//
// Turns is how many turns actually reached the model. DroppedInvalid counts
// turns refused outright — a role other than "user"/"assistant" (which a
// client must never be able to use to claim system authority) or empty
// content.
type HistoryInfo struct {
	Turns          int  `json:"turns"`
	Truncated      bool `json:"truncated,omitempty"`
	DroppedInvalid int  `json:"dropped_invalid,omitempty"`
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

// StatusRequest asks the daemon to describe its own current state. It is the
// operator-facing counterpart to the per-request Degraded signal: that one is
// pushed to whoever happens to be prompting, this one can be pulled at any
// time by anyone who wants to know whether the daemon is healthy.
//
// It exists because there was previously NO way to ask. The daemon had zero
// metrics or health endpoints and logged only to stderr, so establishing
// whether a running daemon was degraded meant finding its stderr, having kept
// it, and knowing which lines mattered. `{"status":true}` before this existed
// fell through to the prompt path and came back "prompt is empty".
//
// Deliberately carried on the EXISTING Unix socket rather than an HTTP port.
// The daemon's defining constraint is that it never listens on a network port
// (see daemon/main.go); the socket is 0600 and SO_PEERCRED peer-authenticated
// (FAIL-3 Gate 3), and a localhost HTTP endpoint would have neither property.
// A health surface is not worth weakening the thing whose health it reports.
//
// Status is the discriminator, always true and deliberately without omitempty,
// exactly like UndoRequest.Undo and SearchRequest.Search -- see those for why:
// a real StatusRequest must always serialize this key so the server's peek
// catches it, and older clients never send it, so they are unaffected.
type StatusRequest struct {
	ProtocolVersion int  `json:"protocol_version"`
	Status          bool `json:"status"`
}

// StatusRetrieval is the retrieval half of a StatusResponse. It reports the
// two tiers SEPARATELY, which is the entire point: the semantic tier being up
// while the lexical tier is down was the state that used to be invisible, and
// a single "retrieval: ok" boolean would hide it again.
//
// Reason is set only when Enabled is false, and carries the same client-safe
// explanation GroundingInfo.Reason does (see retrieval_setup.go's reason
// constants) -- no paths, no internal error text.
type StatusRetrieval struct {
	Enabled            bool   `json:"enabled"`
	Reason             string `json:"reason,omitempty"`
	Lexical            bool   `json:"lexical"`
	TopK               int    `json:"top_k,omitempty"`
	ContextBudgetChars int    `json:"context_budget_chars,omitempty"`
	IndexedChunks      int    `json:"indexed_chunks,omitempty"`
}

// StatusResponse is the daemon's account of itself.
//
// It deliberately does NOT carry the model API base URL. Gate 7 generalized
// upstream errors specifically so a provider host never crosses the socket,
// and a status surface is no reason to reintroduce what an audit removed.
// APIKeyConfigured reports the operationally useful part (whether requests
// will carry an Authorization header at all) without the host. Workspace IS
// included, since GroundingInfo has always reported it.
//
// ConfigWarnings is the same list logged at startup (see Config.Warnings):
// unrecognized config_version, unknown/misspelled keys, clamped values. An
// operator who missed the startup log can still ask why a setting they wrote
// is not in effect.
//
// Degraded is the identical type and content the prompt path reports, so the
// pushed and pulled views of the daemon's health can never disagree.
type StatusResponse struct {
	ProtocolVersion  int             `json:"protocol_version"`
	DaemonVersion    string          `json:"daemon_version"`
	PID              int             `json:"pid"`
	UptimeSeconds    int64           `json:"uptime_seconds"`
	Workspace        string          `json:"workspace,omitempty"`
	Tier             string          `json:"tier,omitempty"`
	Model            string          `json:"model,omitempty"`
	Retrieval        StatusRetrieval `json:"retrieval"`
	MemoryAvailable  bool            `json:"memory_available"`
	APIKeyConfigured bool            `json:"api_key_configured"`
	ConfigVersion    int             `json:"config_version,omitempty"`
	ConfigWarnings   []string        `json:"config_warnings,omitempty"`
	Degraded         []Degradation   `json:"degraded,omitempty"`
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
