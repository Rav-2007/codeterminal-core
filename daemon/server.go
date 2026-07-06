package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"

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
		ProtocolVersion: protocol.ProtocolVersion,
		Ok:              true,
		DaemonVersion:   daemonVersion,
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
	s.logger.Printf("received prompt (%d bytes), calling model API", len(promptReq.Prompt))

	decision := s.route()
	s.logger.Printf("route tier=%s slug=%s reason=%s", decision.Tier, decision.Slug, decision.Reason)

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

	var full strings.Builder
	err := streamCompletion(context.Background(), s.apiBase, s.apiKey, decision.Slug, s.systemPrompt, augmentedPrompt, func(token string) error {
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

	s.logEditBlocks(full.String())
}

// logEditBlocks parses the just-completed response for SEARCH/REPLACE edit
// blocks and logs a structured summary. Nothing is applied to disk here —
// this is parse-and-log only.
func (s *Server) logEditBlocks(response string) {
	blocks, err := ParseEditBlocks(response)
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
