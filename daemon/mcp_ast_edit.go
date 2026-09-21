package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
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

func (s *Server) builtinProposeASTEdit(ctx context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
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
	srv, err := s.lspServerForFile(ctx, full)
	if err != nil {
		return toolError("%v", err)
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

	// REFUSED, NOT TRUNCATED, and that is the difference from read_file.
	//
	// This handler does not display the file; it slices a symbol's range out of
	// it and uses that text as an edit's Search string. Truncating the content
	// would not merely show the model less -- extractLSPRange would slice
	// against a shortened buffer and produce the WRONG search text, which then
	// either fails to match or matches somewhere unintended. A memory bound
	// must not become a correctness bug, so an oversized file is refused.
	//
	// maxFileSize is the indexer's own ceiling (chunker.go), reused rather than
	// invented: fileref.go's readReferencedSpan takes the same position, that a
	// file the indexer will not read is not one this surface should read either.
	contentBytes, fullSize, err := readBoundedFile(full, maxFileSize)
	if err != nil {
		return toolError("failed to read file %s: %v", args.Path, err)
	}
	if fullSize > maxFileSize {
		return toolError("cannot edit %s: it is %d bytes, above the %d-byte limit for "+
			"symbol-level edits; edit it with propose_edit instead", args.Path, fullSize, maxFileSize)
	}

	extractedText := extractLSPRange(string(contentBytes), sym.Range)

	block := editapply.EditBlock{FilePath: args.Path, Search: extractedText, Replace: args.Replace}
	// Resolved, not s.workspace: see realWorkspaceRoot. Passing the unresolved
	// root refuses every edit whenever the workspace is reached through a
	// symlink or an 8.3 short name.

	prepared, err := editapply.PrepareEdit(realRoot, block)
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
