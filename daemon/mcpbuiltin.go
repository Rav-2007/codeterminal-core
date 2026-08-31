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

// proposalSink collects the edits propose_edit produced during one turn, so
// they can be emitted on the final Done message exactly like the blocks parsed
// out of assistant text. Per-turn rather than per-Server: proposals belong to
// the turn that made them.
type proposalSink struct {
	blocks []editapply.EditBlock
}

func (p *proposalSink) add(b editapply.EditBlock) {
	if p != nil {
		p.blocks = append(p.blocks, b)
	}
}

// builtinTools returns the Lane A tools, closed over this Server's state and
// this turn's proposal sink. Registered by buildRegistry, which forces their
// lane and confinement so a tool here cannot misreport itself.
func (s *Server) builtinTools(proposals *proposalSink, mode string) []mcp.Builtin {
	tools := []mcp.Builtin{
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
				Name: "list_directory",
				Description: "List file and subdirectory names in a workspace directory. " +
					"Does not execute tests, builds, or other commands. The path must be workspace-relative.",
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
		{
			Tool: mcp.Tool{
				Name: "query_compiler_definition",
				Description: "Queries the native language compiler/server (e.g., gopls) for the definition of a symbol. " +
					"The path must be workspace-relative. Line and character are 0-indexed.",
				Schema: schema(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path to the file containing the symbol."},
						"line":{"type":"integer","description":"0-indexed line number."},
						"character":{"type":"integer","description":"0-indexed character offset."}
					},
					"required":["path","line","character"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: s.builtinLSPDefinition,
		},
		{
			Tool: mcp.Tool{
				Name: "query_compiler_references",
				Description: "Queries the native language compiler/server (e.g., gopls) for all usages/references of a symbol. " +
					"The path must be workspace-relative. Line and character are 0-indexed.",
				Schema: schema(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path to the file containing the symbol."},
						"line":{"type":"integer","description":"0-indexed line number."},
						"character":{"type":"integer","description":"0-indexed character offset."}
					},
					"required":["path","line","character"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: s.builtinLSPReferences,
		},
		{
			Tool: mcp.Tool{
				Name: "sandbox_exec",
				// The description is what the approving human reads, so it must
				// not promise confinement the host may not provide. It said
				// "Runs in a restricted sandbox" while the handler executed
				// straight on the host with the daemon's full environment.
				// RESOLVED PER HOST, not written once. See
				// sandboxExecDescription: both the confinement claim and the
				// resource-limit claim are derived from the same config the
				// handler uses, because a description is consent and consent
				// obtained for the wrong thing is not consent.
				Description: s.sandboxExecDescription(),
				Schema: schema(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"The shell command to execute."}
					},
					"required":["command"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: false,
				// RUNS ARBITRARY CODE BY DESIGN. This is what stops
				// RegisterBuiltin asserting Confined, and what binds an
				// approve-for-turn grant to the arguments as well as the tool.
				ExecutesCode: true,
				// RESOLVED, NOT ASSERTED. Every other built-in is confined by
				// construction; this one is confined only if the host supplies
				// bwrap or docker. Reported from the same computation the
				// handler confines with (sandboxExecConfig), so the prompt
				// cannot describe a sandbox the command does not get.
				Confined: s.sandboxExecConfined(),
			},
			Handler: s.builtinSandboxExec,
		},
	}

	// The network tools, when they are not switched off. Appended rather than
	// listed inline because they are the only built-ins whose PRESENCE is
	// configurable -- every other tool in this function exists unconditionally
	// and is governed only by policy.
	tools = append(tools, s.webTools()...)

	if mode != "plan" {
		tools = append(tools, mcp.Builtin{
			Tool: mcp.Tool{
				Name: "propose_edit",
				Description: "Propose an edit to a workspace file. The edit is NOT applied: it is shown " +
					"to the user as a reviewable diff, which they accept or reject. Give the exact " +
					"existing text to replace and the text to replace it with.",
				Schema: schema(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path to edit."},
						"search":{"type":"string","description":"The exact existing text to replace. Must appear exactly once in the file."},
						"replace":{"type":"string","description":"The text to put in its place."}
					},
					"required":["path","search","replace"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinProposeEdit(ctx, raw, proposals)
			},
		},
			mcp.Builtin{
				Tool: mcp.Tool{
					Name: "repo_map",
					Description: "Show the shape of this workspace: every directory, the files in it, " +
						"and the top-level declarations of as many files as fit. Use it to find out what " +
						"exists before searching or guessing at a path. Takes no arguments.",
					Schema:       schema(`{"type":"object","properties":{},"additionalProperties":false}`),
					ReadOnlyHint: true,
				},
				Handler: func(ctx context.Context, _ json.RawMessage) (mcp.Result, error) {
					return s.builtinRepoMap(ctx)
				},
			},
			mcp.Builtin{
				Tool: mcp.Tool{
					Name:        "propose_ast_edit",
					Description: "Propose an edit to a workspace file via AST diffing. Finds the exact node for the given symbol (e.g. 'function foo' or 'MyStruct') using the native compiler, and replaces its entire definition with the supplied code. The edit is NOT applied immediately: it is shown to the user as a reviewable diff.",
					Schema: schema(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path to edit."},
						"symbol":{"type":"string","description":"The exact name of the symbol/function to replace (e.g. 'foo' or 'MyClass')."},
						"replace":{"type":"string","description":"The full new code to replace the symbol with."}
					},
					"required":["path","symbol","replace"],
					"additionalProperties":false
				}`),
					ReadOnlyHint: true,
				},
				Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
					return s.builtinProposeASTEdit(ctx, raw, proposals)
				},
			})
	}

	// PLAN MODE DROPS EVERY TOOL THAT EXECUTES CODE.
	//
	// This used to be one line above -- `if mode != "plan"` removing
	// propose_edit -- and that was the whole of it. The effect was backwards:
	// plan mode withdrew the tool that proposes a REVIEWED edit and left
	// sandbox_exec, the one built-in that runs arbitrary code, registered
	// unconditionally. A user who picks a mode called "plan" has said they want
	// thinking and not doing; what they got was the safe way to change the
	// workspace removed and the unreviewed one kept.
	//
	// KEYED ON THE CAPABILITY, NOT ON THE NAME. mcp.Tool.ExecutesCode already
	// exists for exactly this reason -- registry.go and agentloop.go both branch
	// on it, because confinement is a property of a tool and not a fact about
	// its spelling. Filtering on `Name == "sandbox_exec"` would work today and
	// fail silently on the day a second code-executing built-in is added, which
	// is the failure this flag was introduced to prevent.
	if mode == "plan" {
		kept := tools[:0]
		for _, b := range tools {
			if b.Tool.ExecutesCode {
				continue
			}
			kept = append(kept, b)
		}
		tools = kept
	}

	return tools
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

	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("cannot read %s: %v", args.Path, err)
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, args.Path)
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

	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("cannot list %s: %v", args.Path, err)
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, args.Path)
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

// builtinProposeEdit validates a proposed edit and files it for human review.
// It writes NOTHING.
//
// This is the shape the whole Lane A design turns on. The obvious tool to write
// is write_file, and it would have been wrong: it replaces a diff the user
// reads with a JSON argument blob they skim, and it puts the daemon's own
// writer behind a consent prompt that was never designed to carry a code
// change. So the model gets a tool that PROPOSES, and the proposal lands in the
// same review that has governed every edit since before agent mode existed --
// same diff, same exact-match gate, same backup session, same undo.
//
// PrepareEdit is called here for its VALIDATION, not to apply anything: it
// resolves the path under confinement and locates SEARCH, refusing an ambiguous
// or absent match. Doing it now rather than at apply time means the model finds
// out immediately that its SEARCH text does not match, while it still has the
// file in context and can correct itself -- instead of the user discovering it
// at review time, one round trip too late.
// realWorkspaceRoot resolves s.workspace to the symlink-free form the
// confinement gates require.
//
// s.workspace is filepath.Abs ONLY -- validateWorkspace never calls
// EvalSymlinks -- while resolveSafeTarget compares the root it is given against
// EvalSymlinks(root/relPath) using filepath.Rel, which is a BYTE comparison.
// Hand it the unresolved form and the two sides disagree about every path in the
// workspace, so Rel returns "../.." and EVERY edit is refused with "resolves
// outside the workspace root" -- an availability outage wearing a confinement
// error's clothes.
//
// That is not hypothetical and it was not Windows-specific. Both MCP edit tools
// passed s.workspace straight through; the Windows runner surfaced it first
// (8.3 short names) but it reproduces on Linux through any symlinked path, and
// on macOS /tmp is a symlink to /private/tmp by default.
//
// A method rather than an inline call at each site, so a sixth caller has
// something to find. server.go and apply_cmd.go predate this and inline the
// identical call -- they were already correct, which is what made the asymmetry
// invisible: three callers resolved, two did not.
func (s *Server) realWorkspaceRoot() (string, error) {
	return editapply.ResolveRealWorkspaceRoot(s.workspace)
}

func (s *Server) builtinProposeEdit(_ context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		Path    string `json:"path"`
		Search  string `json:"search"`
		Replace string `json:"replace"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return toolError("no path was supplied")
	}

	block := editapply.EditBlock{FilePath: args.Path, Search: args.Search, Replace: args.Replace}

	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("that edit cannot be applied: %v", err)
	}
	prepared, err := editapply.PrepareEdit(realRoot, block)
	if err != nil {
		// The refusal text is already written for a human and names no absolute
		// path -- it is the same message the edit pipeline shows. Handing it
		// back to the model lets it fix its own SEARCH text.
		return toolError("that edit cannot be applied: %v", err)
	}

	proposals.add(block)

	note := ""
	if prepared.MatchNote != "" {
		note = " (" + prepared.MatchNote + ")"
	}
	verb := "replaces"
	if prepared.Creates {
		verb = "creates"
	}
	return mcp.Result{Content: fmt.Sprintf(
		"Proposed: %s %s lines %d-%d%s. NOT applied — the user will review this as a diff and "+
			"decide. Do not propose it again, and do not assume it has taken effect.",
		args.Path, verb, prepared.StartLine, prepared.EndLine, note)}, nil
}
