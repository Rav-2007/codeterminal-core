package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codeterminal/daemon/mcp"
	"codeterminal/editapply"
)

// Lane A: the built-in tools. Go functions in this daemon, not subprocesses.
//
// TWO PROPERTIES MAKE THESE "CONFINED", AND BOTH ARE STRUCTURAL RATHER THAN
// PROMISED:
//
//  1. There is no subprocess. Every path here runs inside the daemon, so the
//     confinement that already governs the daemon governs these too -- there is
//     nothing to escape from.
//
//  2. NONE OF THEM WRITES TO THE FILESYSTEM. Not one. The write tool is
//     propose_edit, and it does exactly what the model already does by emitting
//     a SEARCH/REPLACE block: it produces a PROPOSAL that flows into the
//     existing human review, with its diff, its exact-match gate, its backup
//     session and its undo. A write_file tool that called editapply.Apply
//     directly would have replaced a ±diff the user reads with a JSON argument
//     blob they skim -- a strictly worse gate wearing the same word, "approved".
//
// The second property is why an agent turn cannot damage a workspace even if
// consent is misconfigured: the worst a fully-allowed built-in tool can do is
// read, and propose.
//
// Every path argument goes through editapply.ResolveSafeTargetPath, the same
// resolver that gates model-proposed edits: absolute paths, "..", protected
// directories (.git, .ssh, .aws) and secret-shaped filenames are refused, and
// the check runs again on the symlink-resolved path so an in-tree symlink
// cannot launder a read.

// maxBuiltinReadBytes bounds one read_file result before the loop's own
// per-result cap applies. A tool that can return a 400 MB file into a model
// request is a denial-of-wallet, not a feature.
const maxBuiltinReadBytes = 64 * 1024

// maxBuiltinListEntries bounds a directory listing.
const maxBuiltinListEntries = 500

// builtinTools returns the Lane A tools, closed over this Server's state.
// Registered by buildRegistry, which forces their lane and confinement so a
// tool here cannot misreport itself.
func (s *Server) builtinTools() []mcp.Builtin {
	return []mcp.Builtin{
		{
			Tool: mcp.Tool{
				Name:        "read_file",
				Description: "Read a UTF-8 text file from the workspace. The path must be workspace-relative.",
				Schema: schema(`{
					"type":"object",
					"properties":{"path":{"type":"string","description":"Workspace-relative path."}},
					"required":["path"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: s.builtinReadFile,
		},
		{
			Tool: mcp.Tool{
				Name:        "list_directory",
				Description: "List the entries of a workspace directory. The path must be workspace-relative.",
				Schema: schema(`{
					"type":"object",
					"properties":{"path":{"type":"string","description":"Workspace-relative directory, or \".\" for the root."}},
					"required":["path"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: s.builtinListDirectory,
		},
		{
			Tool: mcp.Tool{
				Name: "search_code",
				Description: "Search the indexed workspace for code relevant to a natural-language query. " +
					"Returns the most relevant chunks with their file paths.",
				Schema: schema(`{
					"type":"object",
					"properties":{"query":{"type":"string","description":"What to look for."}},
					"required":["query"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: s.builtinSearchCode,
		},
	}
}

func schema(s string) json.RawMessage {
	// Compacted so the advertised tool list is not padded with the indentation
	// of this file, which the model pays for by the token.
	var buf json.RawMessage
	if err := json.Unmarshal([]byte(s), new(any)); err != nil {
		panic("built-in tool schema is not valid JSON: " + err.Error())
	}
	compact := strings.Join(strings.Fields(s), " ")
	compact = strings.ReplaceAll(compact, ": ", ":")
	compact = strings.ReplaceAll(compact, ", ", ",")
	buf = json.RawMessage(compact)
	return buf
}

// toolError returns a result the MODEL can read and act on, rather than a Go
// error that would end the turn. A model told "that path is outside the
// workspace" can try a different path; one told nothing repeats the call.
//
// The text follows the same disclosure discipline as socketSafeError: it names
// what was refused and why, never an absolute path or an internal error string.
func toolError(format string, args ...any) (mcp.Result, error) {
	return mcp.Result{Content: fmt.Sprintf(format, args...), IsError: true}, nil
}

func (s *Server) builtinReadFile(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return toolError("no path was supplied")
	}

	full, err := editapply.ResolveSafeTargetPath(s.workspace, args.Path)
	if err != nil {
		// The resolver's message is already written for a human and carries no
		// absolute path -- it is the same text the edit pipeline shows.
		return toolError("cannot read %s: %v", args.Path, err)
	}

	info, err := os.Stat(full)
	if err != nil {
		return toolError("cannot read %s: no such file", args.Path)
	}
	if info.IsDir() {
		return toolError("%s is a directory; use list_directory", args.Path)
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return toolError("cannot read %s", args.Path)
	}

	truncated := ""
	if len(data) > maxBuiltinReadBytes {
		// Truncation is ANNOUNCED. A model given a silently clipped file will
		// reason about the part it cannot see as if it were absent.
		truncated = fmt.Sprintf("\n\n[... truncated: %s is %d bytes, showing the first %d ...]",
			args.Path, len(data), maxBuiltinReadBytes)
		data = data[:maxBuiltinReadBytes]
	}
	return mcp.Result{Content: string(data) + truncated}, nil
}

func (s *Server) builtinListDirectory(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}

	full, err := editapply.ResolveSafeTargetPath(s.workspace, args.Path)
	if err != nil {
		return toolError("cannot list %s: %v", args.Path, err)
	}

	entries, err := os.ReadDir(full)
	if err != nil {
		return toolError("cannot list %s", args.Path)
	}

	var lines []string
	for _, e := range entries {
		name := e.Name()
		// The indexer's pruning rules apply here too: a listing that advertises
		// .git or a secret-shaped filename has told the model those exist and
		// invited it to ask for them.
		if e.IsDir() {
			if editapply.IsProtectedDirName(name) {
				continue
			}
			lines = append(lines, name+"/")
			continue
		}
		if editapply.MatchesSecretName(name) {
			continue
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)

	truncated := ""
	if len(lines) > maxBuiltinListEntries {
		truncated = fmt.Sprintf("\n[... truncated: %d more entries ...]", len(lines)-maxBuiltinListEntries)
		lines = lines[:maxBuiltinListEntries]
	}
	if len(lines) == 0 {
		return mcp.Result{Content: fmt.Sprintf("%s is empty", filepath.Clean(args.Path))}, nil
	}
	return mcp.Result{Content: strings.Join(lines, "\n") + truncated}, nil
}

func (s *Server) builtinSearchCode(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return toolError("no query was supplied")
	}

	// Reuses the daemon's own retrieval path rather than reimplementing search,
	// so this tool is subject to the same index, the same gitignore rules and
	// the same secret-file skipping the grounded prompt path is.
	outcome := s.gatherContext(ctx, args.Query)
	if outcome.Skipped {
		reason := outcome.Reason
		if reason == "" {
			reason = "retrieval is unavailable"
		}
		return toolError("cannot search: %s", reason)
	}
	if len(outcome.Chunks) == 0 {
		return mcp.Result{Content: "no relevant code found for that query"}, nil
	}

	// Rendered through renderChunk, which is the scrub choke point retrieved
	// content already goes through on the prompt path -- so a secret-shaped
	// string in an indexed file is redacted here exactly as it is there.
	var b strings.Builder
	for i, chunk := range outcome.Chunks {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(renderChunk(i, chunk, s.noScrub()))
	}
	return mcp.Result{Content: b.String()}, nil
}
