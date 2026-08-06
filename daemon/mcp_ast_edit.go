package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeterminal/daemon/mcp"
	"codeterminal/editapply"
)

type lspRange struct {
	Start struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"start"`
	End struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"end"`
}

type documentSymbol struct {
	Name     string           `json:"name"`
	Range    lspRange         `json:"range"`
	Children []documentSymbol `json:"children"`
}

func findSymbol(symbols []documentSymbol, name string) *documentSymbol {
	for _, s := range symbols {
		if s.Name == name {
			return &s
		}
		if found := findSymbol(s.Children, name); found != nil {
			return found
		}
	}
	return nil
}

func extractLSPRange(content string, r lspRange) string {
	lines := strings.Split(content, "\n")
	startLine, startChar := r.Start.Line, r.Start.Character
	endLine, endChar := r.End.Line, r.End.Character

	if startLine >= len(lines) {
		return ""
	}
	if endLine >= len(lines) {
		endLine = len(lines) - 1
		endChar = len([]rune(lines[endLine]))
	}

	var b strings.Builder
	for i := startLine; i <= endLine; i++ {
		// LSP uses UTF-16 code units, but treating it as runes is generally close enough
		// for standard code ASCII and most unicode characters.
		runes := []rune(lines[i])
		s := 0
		e := len(runes)

		if i == startLine {
			s = startChar
		}
		if i == endLine {
			e = endChar
		}

		if s < 0 {
			s = 0
		}
		if e > len(runes) {
			e = len(runes)
		}
		if s > e {
			s = e
		}

		b.WriteString(string(runes[s:e]))
		if i < endLine {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (s *Server) builtinProposeASTEdit(_ context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Symbol  string `json:"symbol"`
		Replace string `json:"replace"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return toolError("no path was supplied")
	}
	if strings.TrimSpace(args.Symbol) == "" {
		return toolError("no symbol was supplied")
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
	}
	res, err := srv.Call("textDocument/documentSymbol", params)
	if err != nil {
		return toolError("LSP documentSymbol call failed: %v", err)
	}

	var symbols []documentSymbol
	if err := json.Unmarshal(res, &symbols); err != nil {
		return toolError("failed to parse documentSymbol response: %v", err)
	}

	sym := findSymbol(symbols, args.Symbol)
	if sym == nil {
		return toolError("symbol %q not found in %s", args.Symbol, args.Path)
	}

	contentBytes, err := os.ReadFile(full)
	if err != nil {
		return toolError("failed to read file %s: %v", args.Path, err)
	}

	extractedText := extractLSPRange(string(contentBytes), sym.Range)

	block := editapply.EditBlock{FilePath: args.Path, Search: extractedText, Replace: args.Replace}
	prepared, err := editapply.PrepareEdit(s.workspace, block)
	if err != nil {
		return toolError("that edit cannot be applied: %v", err)
	}

	proposals.add(block)

	note := "AST replaced symbol " + args.Symbol
	if prepared.MatchNote != "" {
		note += "; " + prepared.MatchNote
	}
	verb := "replaces"
	if prepared.Creates {
		verb = "creates"
	}
	return mcp.Result{Content: fmt.Sprintf(
		"Proposed: %s %s lines %d-%d (%s). NOT applied — the user will review this as a diff and "+
			"decide. Do not propose it again, and do not assume it has taken effect.",
		args.Path, verb, prepared.StartLine, prepared.EndLine, note)}, nil
}
