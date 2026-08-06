package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"codeterminal/daemon/mcp"
	"codeterminal/editapply"
)

func (s *Server) builtinLSPDefinition(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	return s.handleLSPQuery("textDocument/definition", raw)
}

func (s *Server) builtinLSPReferences(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	return s.handleLSPQuery("textDocument/references", raw)
}

func (s *Server) handleLSPQuery(method string, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("arguments were not a valid JSON object: %v", err)
	}

	full, err := editapply.ResolveSafeTargetPath(s.workspace, args.Path)
	if err != nil {
		return toolError("invalid path: %v", err)
	}

	lang := "go"
	ext := strings.ToLower(filepath.Ext(full))
	switch ext {
	case ".ts", ".js", ".tsx", ".jsx":
		lang = "typescript"
	case ".py":
		lang = "python"
	}

	// A nil bridge is a configuration state, not a crash. main.go always sets
	// one, but nothing enforced that: every other Server construction -- a
	// one-shot subcommand, a test, whatever is written next -- reached
	// GetServer on a nil pointer and took the daemon's goroutine down with it.
	// handleConn's recover() would have contained it, at the cost of the user's
	// turn and a counted panic.
	if s.lspBridge == nil {
		return toolError("language-server support is not available in this daemon")
	}
	srv, err := s.lspBridge.GetServer(lang)
	if err != nil {
		return toolError("failed to get language server for %s: %v", lang, err)
	}

	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(full)},
		"position": map[string]any{
			"line":      args.Line,
			"character": args.Character,
		},
	}
	if method == "textDocument/references" {
		params["context"] = map[string]any{"includeDeclaration": true}
	}

	res, err := srv.Call(method, params)
	if err != nil {
		return toolError("LSP call failed: %v", err)
	}

	return mcp.Result{Content: string(res)}, nil
}
