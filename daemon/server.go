package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
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

	var promptReq protocol.PromptRequest
	if err := dec.Decode(&promptReq); err != nil {
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

	enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
	s.logger.Print("stream complete")

	s.persistTurn(promptReq.Prompt, full.String())
	s.logEditBlocks(full.String())
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

// logEditBlocks parses the just-completed response for SEARCH/REPLACE edit
// blocks and logs a structured summary. Nothing is applied to disk here —
// this is parse-and-log only.
func (s *Server) logEditBlocks(response string) {
	blocks, err := editapply.ParseEditBlocks(response)
	if err != nil {
		s.logger.Printf("edit block parse error: %v", err)
		return
	}

	s.logger.Printf("parsed %d edit block(s)", len(blocks))
	for i, b := range blocks {
		s.logger.Printf("  block %d: path=%s search_lines=%d replace_lines=%d",
			i+1, b.FilePath, lineCount(b.Search), lineCount(b.Replace))
	}
}

// lineCount returns the number of lines in s, treating an empty string as
// zero lines rather than one.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
