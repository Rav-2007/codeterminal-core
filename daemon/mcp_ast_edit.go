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
	Name  string   `json:"name"`
	Range lspRange `json:"range"`
	// SelectionRange is the symbol's NAME, where Range is its whole
	// declaration; rename_symbol needs the name (rename.go).
	SelectionRange lspRange         `json:"selectionRange"`
	Children       []documentSymbol `json:"children"`
}

// parseDocumentSymbols reads a documentSymbol answer in either shape the
// protocol allows: DocumentSymbol[] (hierarchical, with the name's own
// selectionRange) or the flat SymbolInformation[], whose range sits under
// "location" and covers the whole declaration. A server may send the flat one
// even when told the client takes the other.
func parseDocumentSymbols(raw []byte) ([]documentSymbol, error) {
	var items []struct {
		Name           string    `json:"name"`
		Range          *lspRange `json:"range"`
		SelectionRange *lspRange `json:"selectionRange"`
		Location       *struct {
			Range lspRange `json:"range"`
		} `json:"location"`
		Children json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]documentSymbol, 0, len(items))
	for _, it := range items {
		sym := documentSymbol{Name: it.Name}
		switch {
		case it.Range != nil:
			sym.Range = *it.Range
			if it.SelectionRange != nil {
				sym.SelectionRange = *it.SelectionRange
			}
		case it.Location != nil:
			sym.Range = it.Location.Range
		}
		if len(it.Children) > 0 && string(it.Children) != "null" {
			children, err := parseDocumentSymbols(it.Children)
			if err != nil {
				return nil, err
			}
			sym.Children = children
		}
		out = append(out, sym)
	}
	return out, nil
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

	// The language server reads the REAL file, so a file this turn already
	// changed in its working copy would be sliced at the wrong lines.
	if st, _ := proposals.workingCopy(); st != nil {
		if rel, ok := st.relFor(args.Path); ok && st.touched[rel] {
			return toolError("%s was already changed this turn; use propose_edit, taking the search "+
				"text from read_file", args.Path)
		}
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

	symbols, err := parseDocumentSymbols(res)
	if err != nil {
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

	if res, ok := applyInWorkingCopy(proposals, block); ok {
		return res, nil
	}

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
