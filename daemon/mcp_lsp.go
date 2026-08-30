package main

import (
	"context"
	"encoding/json"

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

	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("invalid path: %v", err)
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, args.Path)
	if err != nil {
		return toolError("invalid path: %v", err)
	}

	// One resolver, shared with every other language-server tool. The extension
	// switch that used to sit here -- byte-identical to the one in its sibling
	// file, and defaulting an unknown extension to Go -- is gone; see
	// lspServerForFile.
	srv, err := s.lspServerForFile(full)
	if err != nil {
		return toolError("%v", err)
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
