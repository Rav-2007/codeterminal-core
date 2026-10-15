package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mochiii/protocol"
)

// A GitHubRequest: a client asking, for the user, what is on GitHub -- or
// handing over the token to keep. It comes through serveConn's connect branch
// (serverextras.go), which never logs a payload: this one can carry a token.

// isGitHubRequest recognises the request by its discriminator, like every
// other typed request on this socket.
func isGitHubRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "github")
}

// handleGitHub answers a GitHubRequest.
func (s *Server) handleGitHub(ctx context.Context, enc *json.Encoder, req protocol.GitHubRequest) {
	resp := s.githubResult(ctx, req)
	resp.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("github response encode error: %v", err)
	}
}

// githubResult is the whole decision, apart from the encoding.
func (s *Server) githubResult(ctx context.Context, req protocol.GitHubRequest) protocol.GitHubResponse {
	switch req.Action {
	case "", protocol.GitHubActionStatus:
		return s.githubStatus(ctx)
	case protocol.GitHubActionConnect:
		return s.githubConnect(ctx, req.Token)
	case protocol.GitHubActionUse:
		return s.githubUse(ctx)
	case protocol.GitHubActionDisconnect:
		return s.githubDisconnect()
	case protocol.GitHubActionIssue, protocol.GitHubActionIssues, protocol.GitHubActionPR,
		protocol.GitHubActionPRs, protocol.GitHubActionLog:
	default:
		return protocol.GitHubResponse{Error: fmt.Sprintf("unknown GitHub action %q", req.Action)}
	}

	token, repo, err := s.githubToolState()
	if err != nil {
		return protocol.GitHubResponse{Error: err.Error()}
	}
	needsNumber := req.Action == protocol.GitHubActionIssue || req.Action == protocol.GitHubActionPR ||
		req.Action == protocol.GitHubActionLog
	if needsNumber && req.Number < 1 {
		example := map[string]string{protocol.GitHubActionIssue: "/issue 104", protocol.GitHubActionPR: "/pr 31",
			protocol.GitHubActionLog: "/pr 31 log"}[req.Action]
		return protocol.GitHubResponse{Error: "give the number, like " + example}
	}
	label := strings.TrimSpace(req.Label)
	if label != "" && !githubLabelPattern.MatchString(label) {
		return protocol.GitHubResponse{Error: fmt.Sprintf("%q is not a label: letters, digits, spaces and . : / + - only", label)}
	}

	resp := protocol.GitHubResponse{Connected: true, Repo: repo}
	var text string
	switch req.Action {
	case protocol.GitHubActionIssue:
		text, err = githubIssueText(ctx, token, repo, req.Number)
		resp.Attach = true
	case protocol.GitHubActionIssues:
		text, err = githubIssuesText(ctx, token, repo, label)
	case protocol.GitHubActionPR:
		text, err = githubPRText(ctx, token, repo, req.Number)
		resp.Attach = true
	case protocol.GitHubActionPRs:
		text, err = githubPRsText(ctx, token, repo)
	case protocol.GitHubActionLog:
		text, resp.Attach, err = githubLogText(ctx, token, repo, req.Number)
	}
	if err != nil {
		return protocol.GitHubResponse{Connected: true, Repo: repo, Error: err.Error()}
	}
	resp.Text, _ = s.credRedact(text)
	return resp
}
