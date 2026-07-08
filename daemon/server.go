package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// daemonVersion is reported to clients in the handshake response.
const daemonVersion = "0.1.0-skeleton"

// Server accepts client connections on the UDS listener, performs the
// version handshake, and proxies one prompt per connection to the model API.
type Server struct {
	apiBase       string
	apiKey        string
	cfg           *Config
	modelOverride string // optional testing override; bypasses the router when set
	systemPrompt  string
	logger        *log.Logger

	// embedder and store are both nil when retrieval is disabled or
	// unavailable (see setupRetrieval in retrieval_setup.go) — every use
	// of them (gatherContext, in context.go) must handle that.
	embedder           Embedder
	store              VectorStore
	retrievalTopK      int
	contextBudgetChars int
	debugContext       bool // --debug-context: log full retrieved chunk content
	rerankDisabled     bool // --no-rerank / retrieval.rerank_disabled: raw similarity order, no class weighting

	// workspace is this daemon's own resolved (absolute) grounding
	// workspace — set once at startup regardless of whether retrieval is
	// actually enabled, purely so it can be reported back to clients via
	// GroundingInfo (see buildGroundingInfo in context.go).
	workspace string

	// memory is the cross-session conversation-memory store, keyed by
	// workspace (see daemon/memory.go). nil when unavailable (missing
	// state dir, unwritable, or corrupt DB) — every use must handle that,
	// degrading to "no cross-session memory" rather than failing the
	// request, exactly like embedder/store above.
	memory *MemoryStore
}

// route decides which tier handles the next request. Today's request path
// never carries a real exit signal (capturing one is a later, client/UX-side
// phase), so this always resolves to the config's default tier — the
// reasoning escalation in Route is wired but stays gated off in practice.
func (s *Server) route() RouteDecision {
	if s.modelOverride != "" {
		return RouteDecision{Tier: "override", Slug: s.modelOverride, Reason: "manual override via --model flag"}
	}
	return Route(s.cfg, RouteInput{HasExitSignal: false})
}

// Serve accepts connections until the listener is closed. Each connection is
// handled in its own goroutine so slow or streaming clients never block
// others.
func (s *Server) Serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Printf("accept error: %v", err)
			}
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	s.logger.Print("client connected")

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var hsReq protocol.HandshakeRequest
	if err := dec.Decode(&hsReq); err != nil {
		s.logger.Printf("handshake read error: %v", err)
		return
	}

	if hsReq.ProtocolVersion != protocol.ProtocolVersion {
		s.logger.Printf("rejecting client %q: protocol version %d != %d", hsReq.ClientName, hsReq.ProtocolVersion, protocol.ProtocolVersion)
		enc.Encode(protocol.HandshakeResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Ok:              false,
			Error: fmt.Sprintf("protocol version mismatch: daemon speaks v%d, client speaks v%d",
				protocol.ProtocolVersion, hsReq.ProtocolVersion),
		})
		return
	}

	if err := enc.Encode(protocol.HandshakeResponse{
		ProtocolVersion:  protocol.ProtocolVersion,
		Ok:               true,
		DaemonVersion:    daemonVersion,
		PersistedHistory: s.loadPersistedHistory(),
	}); err != nil {
		s.logger.Printf("handshake write error: %v", err)
		return
	}
	s.logger.Printf("handshake ok with client %q", hsReq.ClientName)

	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		s.logger.Printf("request read error: %v", err)
		return
	}

	if isApplyEditRequest(raw) {
		var applyReq protocol.ApplyEditRequest
		if err := json.Unmarshal(raw, &applyReq); err != nil {
			s.logger.Printf("apply-edit request decode error: %v", err)
			return
		}
		s.handleApplyEdit(enc, applyReq)
		return
	}

	if isUndoRequest(raw) {
		var undoReq protocol.UndoRequest
		if err := json.Unmarshal(raw, &undoReq); err != nil {
			s.logger.Printf("undo request decode error: %v", err)
			return
		}
		s.handleUndo(enc, undoReq)
		return
	}

	var promptReq protocol.PromptRequest
	if err := json.Unmarshal(raw, &promptReq); err != nil {
		s.logger.Printf("prompt read error: %v", err)
		return
	}

	if promptReq.Reset {
		s.resetPersistedHistory()
		enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
		return
	}

	s.logger.Printf("received prompt (%d bytes), calling model API", len(promptReq.Prompt))

	decision := s.route()
	s.logger.Printf("route tier=%s slug=%s reason=%s", decision.Tier, decision.Slug, decision.Reason)

	historyOutcome := prepareHistory(promptReq.History)
	s.logHistory(historyOutcome)

	outcome := s.gatherContext(context.Background(), promptReq.Prompt)
	s.logRetrieval(outcome)

	grounding := buildGroundingInfo(outcome, s.workspace, promptReq.Workspace)
	if grounding.WorkspaceMismatch {
		s.logger.Printf("workspace mismatch: client expected %s, this daemon is grounding against %s", promptReq.Workspace, s.workspace)
	}
	// Sent before any tokens, as its own message, so a client can show
	// grounding status as soon as the turn starts rather than waiting for
	// the whole answer to finish streaming.
	if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Grounding: grounding}); err != nil {
		s.logger.Printf("grounding info write error: %v", err)
		return
	}

	augmentedPrompt := promptReq.Prompt
	if !outcome.Skipped {
		augmentedPrompt = buildAugmentedUserMessage(promptReq.Prompt, outcome.Chunks)
	}

	// history/completion note: historyOutcome.Messages (built above from
	// the CLIENT-sent promptReq.History) is the ONLY history fed to the
	// model call. s.memory is a separate write-path-plus-hydration store —
	// it is never merged into this live context, only appended to below
	// (on success) and read back at the next connection's handshake (see
	// loadPersistedHistory). Merging it in here too would double the
	// conversation the model sees.
	var full strings.Builder
	err := streamCompletion(context.Background(), s.apiBase, s.apiKey, decision.Slug, s.systemPrompt, historyOutcome.Messages, augmentedPrompt, func(token string) error {
		full.WriteString(token)
		return enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: token})
	})
	if err != nil {
		s.logger.Printf("model API error: %v", err)
		enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true, Error: err.Error()})
		return
	}

	blocks := s.parseAndLogEditBlocks(full.String())
	enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true, EditProposals: editProposalsFromBlocks(blocks)})
	s.logger.Print("stream complete")

	s.persistTurn(promptReq.Prompt, full.String())
}

// isApplyEditRequest sniffs whether raw is an ApplyEditRequest (identified
// by the presence of its "edit" key) rather than a PromptRequest, so one
// connection can carry either message right after a successful handshake
// with no wire-level discriminator field or protocol version bump — older
// clients only ever send PromptRequest-shaped JSON, which has no "edit"
// key, so this always falls through to the existing prompt path for them.
func isApplyEditRequest(raw json.RawMessage) bool {
	var peek struct {
		Edit *protocol.EditBlockWire `json:"edit"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return false
	}
	return peek.Edit != nil
}

// handleApplyEdit runs an ApplyEditRequest through the same editapply core
// the CLI's `edits apply` and the TUI's edit-review flow use — PrepareEdit
// (exact-match, ambiguity-refuse, workspace confinement, secret-file
// refusal, syntax gate), then Apply (backup + write) only if PrepareEdit
// succeeds. Workspace resolution always uses this daemon's own configured
// s.workspace, never req.Workspace (same convention as PromptRequest —
// see its doc comment), so a client can't redirect writes elsewhere by
// lying about its workspace. Any failure at any stage is reported as
// Applied: false with that stage's error verbatim — the identical string
// PrepareEdit/Apply would produce for the CLI or TUI — and no file is
// written, since Apply is only called after PrepareEdit has already
// succeeded.
func (s *Server) handleApplyEdit(enc *json.Encoder, req protocol.ApplyEditRequest) {
	realRoot, err := editapply.ResolveRealWorkspaceRoot(s.workspace)
	if err != nil {
		s.logger.Printf("apply-edit: resolving workspace root: %v", err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: err.Error()})
		return
	}

	block := editapply.EditBlock{FilePath: req.Edit.FilePath, Search: req.Edit.Search, Replace: req.Edit.Replace}
	prepared, err := editapply.PrepareEdit(realRoot, block)
	if err != nil {
		s.logger.Printf("apply-edit: refused %s: %v", block.FilePath, err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: err.Error()})
		return
	}

	backupDir, err := resolveBackupSessionDir(realRoot, req.BackupSessionDir)
	if err != nil {
		s.logger.Printf("apply-edit: creating backup dir: %v", err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: err.Error()})
		return
	}

	if err := editapply.Apply(realRoot, prepared, backupDir); err != nil {
		s.logger.Printf("apply-edit: failed %s: %v", block.FilePath, err)
		enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: false, Error: err.Error()})
		return
	}

	s.logger.Printf("apply-edit: applied %s (backup: %s)", block.FilePath, backupDir)
	enc.Encode(protocol.ApplyEditResponse{ProtocolVersion: protocol.ProtocolVersion, Applied: true, BackupDir: backupDir})
}

// resolveBackupSessionDir returns the backup session directory Apply should
// use for this request: existing, if it names a directory this daemon
// already created for realWorkspaceRoot (i.e. a BackupDir a prior
// ApplyEditResponse returned to the same client, echoed back via
// ApplyEditRequest.BackupSessionDir) — this is how several blocks applied
// over separate connections in one client-side review share a single
// backup session, matching the CLI/TUI's in-process backupDir reuse (see
// editapply.BackupOriginal's idempotency doc). Otherwise (empty, or a value
// that doesn't check out) a fresh session directory is created, identical
// to the pre-existing per-request behavior.
func resolveBackupSessionDir(realWorkspaceRoot, existing string) (string, error) {
	if existing != "" && isWorkspaceBackupSessionDir(realWorkspaceRoot, existing) {
		return existing, nil
	}
	return editapply.NewBackupSessionDir(realWorkspaceRoot)
}

// isWorkspaceBackupSessionDir reports whether dir is an existing directory
// confined to realWorkspaceRoot/.codeterminal/backups. A well-behaved
// client only ever echoes back a path this daemon itself issued, but this
// check is still applied so a stale, malformed, or crafted value can never
// redirect backup writes outside the intended tree — the same confinement
// discipline ResolveSafeTargetPath applies to edit targets.
func isWorkspaceBackupSessionDir(realWorkspaceRoot, dir string) bool {
	backupsRoot := filepath.Join(realWorkspaceRoot, ".codeterminal", "backups")
	rel, err := filepath.Rel(backupsRoot, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// isUndoRequest sniffs whether raw is an UndoRequest (identified by the
// presence of its "undo" key, mirroring isApplyEditRequest's peek-for-"edit"
// approach) rather than a PromptRequest. UndoRequest always serializes
// "undo" (no omitempty, see its doc comment), so a real one is always
// caught here; older clients that don't know this message only ever send
// PromptRequest-shaped JSON, which has no "undo" key, so they always fall
// through to the prompt path unaffected.
func isUndoRequest(raw json.RawMessage) bool {
	var peek struct {
		Undo *bool `json:"undo"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return false
	}
	return peek.Undo != nil
}

// handleUndo runs an UndoRequest through the exact same runUndoSession the
// CLI's `edits undo` already uses — no restore logic is reimplemented here.
// Workspace resolution always uses this daemon's own configured s.workspace,
// never req.Workspace, same convention as every other request type.
//
// When req.BackupSessionDir is supplied, it must be a directory this daemon
// itself could have issued (confined to the workspace's backups tree, still
// present on disk) — reusing isWorkspaceBackupSessionDir verbatim, the same
// check ApplyEditRequest's BackupSessionDir goes through. Unlike the apply
// path, a value that fails this check is a hard refusal, not a fallback: an
// unrecognized session has no sensible "undo something else instead"
// behavior. An empty BackupSessionDir instead resolves to the most recent
// session, via the same resolveBackupSession the CLI uses.
//
// force is always false and in is always empty here: a socket client has no
// controlling terminal to answer runUndoSession's "overwrite anyway? [y/N]"
// prompt, so a file that changed since the apply run is always left guarded
// rather than silently forced — the same safe default the CLI gets by just
// pressing enter. Guarded files are returned to the caller, never hidden.
func (s *Server) handleUndo(enc *json.Encoder, req protocol.UndoRequest) {
	realRoot, err := editapply.ResolveRealWorkspaceRoot(s.workspace)
	if err != nil {
		s.logger.Printf("undo: resolving workspace root: %v", err)
		enc.Encode(protocol.UndoResponse{ProtocolVersion: protocol.ProtocolVersion, Error: err.Error()})
		return
	}

	backupsRoot := filepath.Join(realRoot, ".codeterminal", "backups")

	var sessionDir string
	if req.BackupSessionDir != "" {
		if !isWorkspaceBackupSessionDir(realRoot, req.BackupSessionDir) {
			s.logger.Printf("undo: refused unrecognized session dir %q", req.BackupSessionDir)
			enc.Encode(protocol.UndoResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Error:           fmt.Sprintf("backup session %q not found under %s", req.BackupSessionDir, backupsRoot),
			})
			return
		}
		sessionDir = req.BackupSessionDir
	} else {
		sessionDir, err = resolveBackupSession(backupsRoot, "")
		if err != nil {
			s.logger.Printf("undo: resolving latest session: %v", err)
			enc.Encode(protocol.UndoResponse{ProtocolVersion: protocol.ProtocolVersion, Error: err.Error()})
			return
		}
	}

	restored, guarded, err := runUndoSession(realRoot, sessionDir, false, strings.NewReader(""), io.Discard, s.logger)
	if err != nil {
		s.logger.Printf("undo: restoring %s: %v", sessionDir, err)
		enc.Encode(protocol.UndoResponse{ProtocolVersion: protocol.ProtocolVersion, Error: err.Error()})
		return
	}

	s.logger.Printf("undo: restored %d file(s) from %s (%d guarded)", restored, sessionDir, len(guarded))
	enc.Encode(protocol.UndoResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Restored:        restored,
		Guarded:         guarded,
		SessionDir:      sessionDir,
	})
}

// loadPersistedHistory returns this daemon's cross-session conversation
// memory for its own workspace (most recent maxHistoryTurns, oldest
// first), or nil if memory is unavailable or empty. Included in every
// HandshakeResponse — see the doc comment on
// protocol.HandshakeResponse.PersistedHistory for who's actually meant to
// consume it (only a client's own startup/preflight connection).
func (s *Server) loadPersistedHistory() []protocol.Turn {
	if s.memory == nil {
		return nil
	}
	turns, err := s.memory.LoadRecentTurns(context.Background(), s.workspace, maxHistoryTurns)
	if err != nil {
		s.logger.Printf("loading persisted history: %v", err)
		return nil
	}
	return turns
}

// persistTurn appends the just-completed exchange (the user's raw prompt —
// never the grounding-augmented version, since retrieved context is
// re-derived fresh every turn, not something to remember — and the
// assistant's full answer) to cross-session memory, write-through. Called
// only after streamCompletion has already returned successfully, so a
// mid-stream failure (including a client disconnecting before the answer
// finished) never persists a truncated answer as if it were complete.
func (s *Server) persistTurn(prompt, answer string) {
	if s.memory == nil {
		return
	}
	ctx := context.Background()
	if err := s.memory.AppendTurn(ctx, s.workspace, "user", prompt); err != nil {
		s.logger.Printf("persisting user turn: %v", err)
		return
	}
	if err := s.memory.AppendTurn(ctx, s.workspace, "assistant", answer); err != nil {
		s.logger.Printf("persisting assistant turn: %v", err)
	}
}

// resetPersistedHistory clears this daemon's cross-session memory for its
// own workspace — the server-side half of ctrl+n (see PromptRequest.Reset).
func (s *Server) resetPersistedHistory() {
	if s.memory == nil {
		return
	}
	if err := s.memory.ClearWorkspace(context.Background(), s.workspace); err != nil {
		s.logger.Printf("clearing persisted history: %v", err)
		return
	}
	s.logger.Print("persisted history cleared")
}

// parseAndLogEditBlocks parses the just-completed response for SEARCH/REPLACE
// edit blocks and logs a structured summary, returning whatever it found (nil
// on a parse error or a response with no blocks). Nothing is applied to disk
// here or by the caller sending EditProposals on — this is parse-only;
// PrepareEdit's safety gates run only later, when a client actually sends an
// ApplyEditRequest for one of these.
func (s *Server) parseAndLogEditBlocks(response string) []editapply.EditBlock {
	blocks, err := editapply.ParseEditBlocks(response)
	if err != nil {
		s.logger.Printf("edit block parse error: %v", err)
		return nil
	}

	s.logger.Printf("parsed %d edit block(s)", len(blocks))
	for i, b := range blocks {
		s.logger.Printf("  block %d: path=%s search_lines=%d replace_lines=%d",
			i+1, b.FilePath, lineCount(b.Search), lineCount(b.Replace))
	}
	return blocks
}

// editProposalsFromBlocks converts parsed edit blocks to their wire form for
// TokenResponse.EditProposals. A nil/empty input returns nil, so the
// json:"...,omitempty" tag drops the field entirely for plain-text answers.
func editProposalsFromBlocks(blocks []editapply.EditBlock) []protocol.EditBlockWire {
	if len(blocks) == 0 {
		return nil
	}
	wire := make([]protocol.EditBlockWire, len(blocks))
	for i, b := range blocks {
		wire[i] = protocol.EditBlockWire{FilePath: b.FilePath, Search: b.Search, Replace: b.Replace}
	}
	return wire
}

// lineCount returns the number of lines in s, treating an empty string as
// zero lines rather than one.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
