package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// rename_symbol: a name changed everywhere it is used, by the language server
// that knows the code -- the refactor step that grep and propose_edit can only
// approximate (a name shadowed in one scope, a field of the same name on
// another type, a method reached through an interface).
//
// THE LANGUAGE SERVER READS THE PROJECT'S FILES, not the working copy, so its
// edits are positions in the project's text. They are applied only when every
// file they touch is exactly as the project has it -- unchanged in the working
// copy -- and then ALL OR NONE: a rename half-applied is a broken build with no
// one to blame. propose_ast_edit takes the same position for the same reason.
// So: rename first, then edit.
//
// POSITIONS ARE UTF-16 CODE UNITS, the protocol's default and what the bridge
// negotiates (it declares no other encoding). A rename splices at exactly the
// position the server named, so the conversion is exact rather than the rune
// approximation extractLSPRange makes for display.

// renameIdentifier is a name rename_symbol will write: one identifier, in any
// script, never anything that could change the code's shape.
var renameIdentifier = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]{0,99}$`)

// lspTextEdit is one edit of a WorkspaceEdit.
type lspTextEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

func (s *Server) builtinRenameSymbol(ctx context.Context, raw json.RawMessage, p *proposalSink) (mcp.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Symbol  string `json:"symbol"`
		NewName string `json:"new_name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" || strings.TrimSpace(args.Symbol) == "" {
		return toolError("give path (the file that declares it) and symbol (its name)")
	}
	if !renameIdentifier.MatchString(args.NewName) {
		return toolError("new_name must be one identifier: letters, digits and _, not starting with a digit")
	}
	st, err := p.workingCopy()
	if err != nil || st == nil {
		return toolError("rename_symbol needs the task's working copy, and this project has none")
	}
	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("invalid path: %v", err)
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, args.Path)
	if err != nil {
		return toolError("invalid path: %v", err)
	}
	if rel, err := filepath.Rel(realRoot, full); err == nil && st.touched[rel] {
		return toolError("%s was already changed in this task. rename_symbol works from the project's own text: "+
			"rename before editing, or change the name with grep and propose_edit", args.Path)
	}
	content, size, err := readBoundedFile(full, maxFileSize)
	if err != nil || size > maxFileSize {
		return toolError("cannot read %s for the rename", args.Path)
	}

	srv, err := s.lspServerForFile(ctx, full)
	if err != nil {
		return toolError("%v", err)
	}
	res, err := srv.CallContext(ctx, "textDocument/documentSymbol",
		map[string]any{"textDocument": map[string]any{"uri": fileURI(full)}})
	if err != nil {
		return toolError("the language server could not list %s's symbols: %v", args.Path, err)
	}
	symbols, err := parseDocumentSymbols(res)
	if err != nil {
		return toolError("the language server's symbol list could not be read: %v", err)
	}
	sym := findSymbol(symbols, args.Symbol)
	if sym == nil {
		return toolError("symbol %q is not declared in %s", args.Symbol, args.Path)
	}
	pos, ok := symbolNamePosition(string(content), *sym, args.Symbol)
	if !ok {
		return toolError("could not find %q's name in its declaration in %s", args.Symbol, args.Path)
	}
	res, err = srv.CallContext(ctx, "textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": fileURI(full)},
		"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		"newName":      args.NewName,
	})
	if err != nil {
		return toolError("the language server refused the rename: %v", err)
	}
	edits, err := parseWorkspaceEdit(res)
	if err != nil {
		return toolError("%v", err)
	}
	return applyRename(st, realRoot, edits, args.Symbol, args.NewName)
}

// lspPos is a protocol position: a line, and a column in UTF-16 code units.
type lspPos struct{ Line, Character int }

// symbolNamePosition is where a declaration's NAME starts: the server's
// selectionRange when it sent one, or else the first whole-word occurrence of
// the name inside the declaration's range (a range starts at `func`, or at the
// doc comment, where a rename finds no identifier).
func symbolNamePosition(content string, sym documentSymbol, name string) (lspPos, bool) {
	if sel := sym.SelectionRange; sel.Start.Line != 0 || sel.Start.Character != 0 || sel.End.Character != 0 {
		return lspPos{sel.Start.Line, sel.Start.Character}, true
	}
	lines := strings.Split(content, "\n")
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	for l := sym.Range.Start.Line; l < len(lines) && l <= sym.Range.End.Line; l++ {
		if loc := word.FindStringIndex(lines[l]); loc != nil {
			return lspPos{l, utf16Len(lines[l][:loc[0]])}, true
		}
	}
	return lspPos{}, false
}

// parseWorkspaceEdit reads a WorkspaceEdit's text edits by document URI, in
// either of its shapes. Creating, renaming or deleting a FILE is refused: a
// symbol rename has no business moving files, and the review cannot show it.
func parseWorkspaceEdit(raw []byte) (map[string][]lspTextEdit, error) {
	if string(raw) == "null" || len(raw) == 0 {
		return nil, fmt.Errorf("the language server found nothing to rename there")
	}
	var we struct {
		Changes         map[string][]lspTextEdit `json:"changes"`
		DocumentChanges []json.RawMessage        `json:"documentChanges"`
	}
	if err := json.Unmarshal(raw, &we); err != nil {
		return nil, fmt.Errorf("the language server's edit could not be read: %v", err)
	}
	out := map[string][]lspTextEdit{}
	for uri, edits := range we.Changes {
		out[uri] = append(out[uri], edits...)
	}
	for _, dc := range we.DocumentChanges {
		var change struct {
			Kind         string `json:"kind"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []lspTextEdit `json:"edits"`
		}
		if err := json.Unmarshal(dc, &change); err != nil {
			return nil, fmt.Errorf("the language server's edit could not be read: %v", err)
		}
		if change.Kind != "" {
			return nil, fmt.Errorf("the rename would %s a file, which rename_symbol does not do", change.Kind)
		}
		out[change.TextDocument.URI] = append(out[change.TextDocument.URI], change.Edits...)
	}
	return out, nil
}

// renameChange is one file of a rename: the project's text, and the result.
type renameChange struct {
	rel           string
	before, after string
	edits         int
}

// applyRename applies a WorkspaceEdit to the working copy, all or none.
func applyRename(st *stagedWorkspace, realRoot string, edits map[string][]lspTextEdit, from, to string) (mcp.Result, error) {
	uris := make([]string, 0, len(edits))
	for uri := range edits {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	var changes []renameChange
	for _, uri := range uris {
		path, err := uriPath(uri)
		if err != nil {
			return toolError("the rename named %q: %v", uri, err)
		}
		rel, err := filepath.Rel(realRoot, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return toolError("the rename reaches outside the project (%s); nothing was changed", path)
		}
		full, err := editapply.ResolveSafeTargetPath(realRoot, rel)
		if err != nil {
			return toolError("the rename reaches %s, which cannot be edited (%v); nothing was changed", filepath.ToSlash(rel), err)
		}
		if st.touched[rel] {
			return toolError("the rename reaches %s, which this task already changed; rename_symbol works from the "+
				"project's own text, so nothing was changed. Rename before editing, or change the name with grep "+
				"and propose_edit", filepath.ToSlash(rel))
		}
		before, size, err := readBoundedFile(full, maxFileSize)
		if err != nil || size > maxFileSize {
			return toolError("cannot read %s for the rename; nothing was changed", filepath.ToSlash(rel))
		}
		// Untouched by an edit is not enough: a command may have changed it.
		if copyText, err := st.readStageFile(rel); err != nil || copyText == nil || *copyText != string(before) {
			return toolError("%s differs in the working copy from the project, so the language server's positions "+
				"would land wrong; nothing was changed", filepath.ToSlash(rel))
		}
		after, err := applyTextEdits(string(before), edits[uri])
		if err != nil {
			return toolError("the rename's edit to %s does not fit the file (%v); nothing was changed", filepath.ToSlash(rel), err)
		}
		if after != string(before) {
			changes = append(changes, renameChange{rel: rel, before: string(before), after: after, edits: len(edits[uri])})
		}
	}
	if len(changes) == 0 {
		return toolError("the language server found nothing to rename")
	}

	// ALL OR NONE: a file that will not take its edit undoes the ones before it.
	var done []renameChange
	for _, c := range changes {
		if _, err := st.apply(editapply.EditBlock{FilePath: c.rel, Search: c.before, Replace: c.after}); err != nil {
			for i := len(done) - 1; i >= 0; i-- {
				_, _ = st.apply(editapply.EditBlock{FilePath: done[i].rel, Search: done[i].after, Replace: done[i].before})
			}
			return toolError("the rename was not applied -- %s refused its edit (%v), so every file is as it was",
				filepath.ToSlash(c.rel), err)
		}
		done = append(done, c)
	}
	total, files := 0, make([]string, 0, len(done))
	for _, c := range done {
		total += c.edits
		files = append(files, filepath.ToSlash(c.rel))
	}
	return mcp.Result{Content: fmt.Sprintf("Renamed %s to %s: %d edit(s) in %d file(s): %s. Build and test to confirm.",
		from, to, total, len(done), strings.Join(files, ", "))}, nil
}

// uriPath turns a file:// URI into a path.
func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI")
	}
	return filepath.FromSlash(u.Path), nil
}

// applyTextEdits splices edits into content at their exact UTF-16 positions,
// last first, refusing edits that overlap or fall outside the text.
func applyTextEdits(content string, edits []lspTextEdit) (string, error) {
	type span struct {
		start, end int
		text       string
	}
	lineStarts := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	offset := func(line, char int) (int, error) {
		if line < 0 || line >= len(lineStarts) {
			return 0, fmt.Errorf("line %d is past the end", line+1)
		}
		end := len(content)
		if line+1 < len(lineStarts) {
			end = lineStarts[line+1] - 1 // the line's own text, without its newline
		}
		at, units := lineStarts[line], 0
		for units < char {
			if at >= end {
				return 0, fmt.Errorf("column %d is past the end of line %d", char, line+1)
			}
			r, size := utf8.DecodeRuneInString(content[at:end])
			units += utf16RuneLen(r)
			at += size
		}
		if units != char {
			return 0, fmt.Errorf("column %d falls inside a character on line %d", char, line+1)
		}
		return at, nil
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		start, err := offset(e.Range.Start.Line, e.Range.Start.Character)
		if err != nil {
			return "", err
		}
		end, err := offset(e.Range.End.Line, e.Range.End.Character)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("an edit ends before it starts")
		}
		spans = append(spans, span{start, end, e.NewText})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := content
	for i, sp := range spans {
		if i > 0 && sp.end > spans[i-1].start {
			return "", fmt.Errorf("two edits overlap")
		}
		out = out[:sp.start] + sp.text + out[sp.end:]
	}
	return out, nil
}

// utf16RuneLen is how many UTF-16 code units r takes: two past the Basic
// Multilingual Plane, one otherwise.
func utf16RuneLen(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// utf16Len is s's length in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16RuneLen(r)
	}
	return n
}

// renameSymbolTool is the built-in, offered in the long-task modes.
func (s *Server) renameSymbolTool(p *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "rename_symbol",
			Description: "Rename a function, type, method, field or variable everywhere it is used, through the " +
				"language server (e.g. gopls), in your working copy. Give the file that declares it, its current " +
				"name and the new one. All or nothing. It works from the project's own text, so rename before " +
				"you edit the files involved. Build and test afterwards.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Workspace-relative path of the file that declares the symbol."},
					"symbol":{"type":"string","description":"The symbol's current name, e.g. CalcTotal."},
					"new_name":{"type":"string","description":"The new name."}
				},
				"required":["path","symbol","new_name"],
				"additionalProperties":false
			}`),
			// It changes the working copy, never the project.
			ReadOnlyHint: true,
			// It starts the language server, as query_compiler_* and
			// propose_ast_edit do.
			LaunchesSubprocess: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinRenameSymbol(ctx, raw, p)
		},
		Launch: s.lspLaunchPlan,
	}
}
