package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
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

// maxBuiltinReadBytes bounds THE READ, and therefore also the result, of one
// read_file. It used to bound only the result: the whole file was materialised
// with os.ReadFile and then sliced, so a 32 MiB file cost 32 MiB of daemon RSS
// to return 64 KiB (measured: 33,715,296 bytes allocated for 65,609 returned).
// Whether a cap bounds the READ or the RESULT is the distinction that hid that,
// so every cap in this file now says which. Bounds one read_file result before the loop's own
// per-result cap applies. A tool that can return a 400 MB file into a model
// request is a denial-of-wallet, not a feature.
const maxBuiltinReadBytes = 64 * 1024

// maxBuiltinListEntries bounds the RESULT of a directory listing -- how many
// names are shown. maxBuiltinListScan bounds the READ that feeds it.
//
// TWO CONSTANTS BECAUSE THERE ARE TWO LIMITS, and collapsing them is what the
// bug was. Selecting the alphabetically-first N requires seeing every name, so
// the scan cannot simply stop at N without changing which entries are shown.
// It can stop at a bound far above any real source directory, which keeps the
// listing identical everywhere it matters and bounds the allocation everywhere
// it does not. Exceeding it is announced, not silent.
const maxBuiltinListEntries = 500

// maxBuiltinListScan bounds how many directory entries are materialised. At
// ~120 bytes per entry this is a few megabytes; os.ReadDir on an unbounded
// directory is unbounded (measured: 9,694,832 bytes to list 500 of 40,000).
const maxBuiltinListScan = 10000

// proposalSink is one turn's edits: collected so they can be emitted on the
// final Done message exactly like the blocks parsed out of assistant text.
// Per-turn rather than per-Server: proposals belong to the turn that made them.
//
// It also owns the turn's WORKING COPY (stage.go) when the turn has one:
// stageFrom names the real workspace to copy, and the copy is made on first
// use. A sink with stageFrom == "" -- every test that builds &proposalSink{},
// and plan mode -- proposes against the real files exactly as before.
type proposalSink struct {
	// blocks are proposals nothing has applied: every edit when there is no
	// working copy, and edits outside the project (~/...) when there is.
	blocks []editapply.EditBlock

	stageFrom string
	stage     *stagedWorkspace
	stageErr  error

	// The last command run in the working copy, for WorkingCopyInfo.
	checked, checkOutput string
	checkPassed          bool
	// Every command this turn ran, for record_criterion's evidence check.
	checks []ranCheck

	// The active spec (spec.go), when the turn has one. specOnly limits
	// propose_edit to files under specs/ (/spec); checking marks a /spec check
	// turn, whose working copy is thrown away and whose verdicts are reported.
	spec     *activeSpec
	specOnly bool
	checking bool
	building bool
	verdicts map[string]specVerdict
	report   *protocol.SpecReport

	// The build's task list (update_tasks), and how it reaches the client.
	tasks   []protocol.TaskItem
	onTasks func([]protocol.TaskItem)

	// textNotes are answer-text edits absorbText could not bring into the
	// working copy, reported with the copy's other not-offered changes.
	textNotes []string

	// task is the long task this request is running (longtask.go), or nil.
	task *taskRun
}

// ranCheck is one command a turn ran and whether it passed.
type ranCheck struct {
	command string
	passed  bool
	// edits is how many edits the working copy had when it ran, so a pass can
	// be told apart from a pass that predates the last change (finishRefusal).
	edits int
}

// editCount is how many edits this turn has made: in the working copy, or
// filed as proposals when there is none.
func (p *proposalSink) editCount() int {
	if p == nil {
		return 0
	}
	if p.stage != nil {
		return len(p.stage.applied)
	}
	return len(p.blocks)
}

// lastCheck is the last command this turn ran in its working copy, or nil.
func (p *proposalSink) lastCheck() *ranCheck {
	if p == nil || len(p.checks) == 0 {
		return nil
	}
	return &p.checks[len(p.checks)-1]
}

// workingCopy returns the turn's private copy of the project, making it on
// first use. nil with a nil error means this turn has no working copy; nil
// with an error means making it failed, and the turn carries on without one.
func (p *proposalSink) workingCopy() (*stagedWorkspace, error) {
	if p == nil || p.stageFrom == "" {
		return nil, nil
	}
	if p.stage != nil || p.stageErr != nil {
		return p.stage, p.stageErr
	}
	p.stage, p.stageErr = newStagedWorkspace(p.stageFrom)
	return p.stage, p.stageErr
}

// readCtx carries the working copy, if one exists yet, to the read tools. A
// read never MAKES the copy: until an edit or a command, the copy and the
// project are the same files.
func (p *proposalSink) readCtx(ctx context.Context) context.Context {
	if p == nil || p.stage == nil {
		return ctx
	}
	return context.WithValue(ctx, stageCtxKey{}, p.stage)
}

type stageCtxKey struct{}

func stageFromCtx(ctx context.Context) *stagedWorkspace {
	st, _ := ctx.Value(stageCtxKey{}).(*stagedWorkspace)
	return st
}

// recordCheck remembers a command that ran in the working copy.
func (p *proposalSink) recordCheck(command string, res mcp.Result) {
	if p == nil || res.IsError {
		return
	}
	p.checked = command
	p.checkPassed = !strings.HasPrefix(res.Content, "Command exited with error") &&
		!strings.HasPrefix(res.Content, "Command timed out")
	p.checkOutput = lastNLines(res.Content, 12)
	p.checks = append(p.checks, ranCheck{command: command, passed: p.checkPassed, edits: p.editCount()})
}

// absorbText brings edit blocks the model wrote in its ANSWER into the working
// copy, so they join the one net diff instead of being offered on top of it.
//
// MEASURED: a model made a change with propose_edit -- landing in the copy --
// and then also wrote it out as a SEARCH/REPLACE block in its answer, as the
// system prompt teaches. Offered on top of the copy's diff, it applied twice:
// "IsPalindrome redeclared". A block whose replacement is already in the copy
// is that duplicate and is dropped; one that applies to the copy is applied
// there; one that does not apply to the copy (it collides with the agent's own
// edits) is not offered, and said so -- offering it against the real file
// would put back a state the agent never tested. Outside the project, and
// with no working copy, blocks are returned untouched.
func (p *proposalSink) absorbText(blocks []editapply.EditBlock) (rest []editapply.EditBlock) {
	if p == nil || p.stage == nil || p.checking {
		return blocks
	}
	st := p.stage
	for _, b := range blocks {
		rel, inside := st.relFor(b.FilePath)
		if !inside {
			rest = append(rest, b)
			continue
		}
		inCopy := b
		inCopy.FilePath = rel
		// THE DUPLICATE: the same edit the agent already made with a tool,
		// restated in its answer -- or, failing an exact match, a block whose
		// replacement is in the copy and whose search text is gone from it.
		duplicate := false
		for _, done := range st.applied {
			if sameEdit(done, inCopy) {
				duplicate = true
				break
			}
		}
		if !duplicate && b.Replace != "" {
			// Bounded like every read on this surface (builtinreadalloc_test.go);
			// a file past the bound is simply not judged a duplicate.
			if current, size, err := readBoundedFile(filepath.Join(st.root, rel), maxFileSize); err == nil &&
				size <= maxFileSize && strings.Contains(string(current), b.Replace) &&
				(b.Search == "" || !strings.Contains(string(current), b.Search)) {
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		if _, err := st.apply(inCopy); err != nil {
			p.textNotes = append(p.textNotes, filepath.ToSlash(rel)+
				": an edit written in the answer did not apply to the working copy, so it is not offered")
		}
	}
	return rest
}

// finish ends the turn's use of the working copy: the edits to offer, what
// the user should be told about the copy, and a degradation if the copy could
// not be made. The copy is removed.
func (p *proposalSink) finish() (blocks []editapply.EditBlock, info *protocol.WorkingCopyInfo, degraded []protocol.Degradation) {
	if p == nil {
		return nil, nil, nil
	}
	if p.stageErr != nil {
		degraded = append(degraded, describeStageRefusal(p.stageErr))
	}
	// A CHECK CHANGES NOTHING: its working copy is where its commands ran, and
	// all it offers is the spec's ticked boxes.
	if p.checking {
		var ticks []editapply.EditBlock
		p.report, ticks = p.specReport()
		if p.stage != nil {
			if net, _ := p.stage.netChanges(); len(net) > 0 {
				degraded = append(degraded, protocol.Degradation{Component: protocol.DegradedWorkingCopy,
					Detail: "the check changed files in its throwaway copy of the project; none of that was kept"})
			}
			p.stage.close()
			p.stage = nil
		}
		return ticks, nil, degraded
	}
	// A BUILD offers its changes like any turn, with the criteria's verdicts
	// ticked into the spec in the same working copy first.
	if p.building {
		p.report, _ = p.specReport()
		tickSpecInCopy(p.stage, p.spec, p.verdicts)
	}
	if p.stage == nil {
		return p.blocks, nil, degraded
	}
	edits := p.editCount()
	net, notOffered := p.stage.netChanges()
	p.stage.close()
	p.stage = nil
	notOffered = append(notOffered, p.textNotes...)
	info = &protocol.WorkingCopyInfo{Checked: p.checked, Passed: p.checkPassed, NotOffered: notOffered}
	// A CHECK VOUCHES ONLY FOR WHAT IT RAN AGAINST. An edit after it -- with a
	// tool, a restore, or written in the answer and absorbed above -- changed
	// what is offered, and "checked: passed" alone would describe code that
	// nobody ran.
	if c := p.lastCheck(); c != nil && c.edits != edits {
		info.Stale = true
	}
	if p.checked != "" && !p.checkPassed {
		info.Output = p.checkOutput
	}
	return append(net, p.blocks...), info, degraded
}

// discard removes the working copy without computing anything: the turn is
// not going to offer its edits (an error, an interrupt).
func (p *proposalSink) discard() {
	if p != nil && p.stage != nil {
		p.stage.close()
		p.stage = nil
	}
}

func lastNLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
	// A LONG TASK READS BY RANGE AND RUNS COMMANDS LONGER (taskmodes.go). Both
	// are offered in its modes only, so an ordinary turn's request -- the
	// measured default -- stays byte-identical.
	readDescription := "Read a UTF-8 text file. A workspace-relative path reads from the workspace. " +
		"An absolute or ~/ path reads anywhere else on the user's machine: the user is asked first, " +
		"and private keys and credential stores are always refused."
	readSchema := `{
					"type":"object",
					"properties":{"path":{"type":"string","description":"Workspace-relative path, or an absolute or ~/ path outside the workspace."}},
					"required":["path"],
					"additionalProperties":false
				}`
	if isLongTaskMode(mode) {
		readDescription += " start_line and end_line (1-based, inclusive) return just those lines, numbered: " +
			"the way to read part of a file, or a file past 64 KB."
		readSchema = `{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path, or an absolute or ~/ path outside the workspace."},
						"start_line":{"type":"integer","description":"First line to return, 1-based."},
						"end_line":{"type":"integer","description":"Last line to return, inclusive. Omit for the end of the file."}
					},
					"required":["path"],
					"additionalProperties":false
				}`
	}
	// The same literal as always (schema() keeps its token order), with the
	// program list widened only when extra_programs applies on this host.
	execSchema := `{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"One build or test command: go, npm, make or cargo and its arguments, e.g. go test ./... -- NOT a shell: no pipes, &&, cat or echo. To see a file, use read_file."}
					},
					"required":["command"],
					"additionalProperties":false
				}`
	if programs := s.execPrograms(); len(programs) > 4 {
		execSchema = strings.Replace(execSchema, "go, npm, make or cargo", strings.Join(programs, ", "), 1)
	}

	tools := []mcp.Builtin{
		{
			Tool: mcp.Tool{
				Name:         "read_file",
				Description:  readDescription,
				Schema:       schema(readSchema),
				ReadOnlyHint: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinReadFile(proposals.readCtx(ctx), raw)
			},
		},
		{
			Tool: mcp.Tool{
				Name: "list_directory",
				Description: "List file and subdirectory names in a directory. " +
					"Does not execute tests, builds, or other commands. A workspace-relative path lists the workspace; " +
					"an absolute or ~/ path (for example ~/Desktop) lists anywhere else on the user's machine after the user approves.",
				Schema: schema(`{
					"type":"object",
					"properties":{"path":{"type":"string","description":"Workspace-relative directory, \".\" for the root, or an absolute or ~/ path outside the workspace."}},
					"required":["path"],
					"additionalProperties":false
				}`),
				ReadOnlyHint: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinListDirectory(proposals.readCtx(ctx), raw)
			},
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
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinSearchCode(proposals.readCtx(ctx), raw)
			},
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
				// Starts a language server (LSPBridge -> exec.Command). That file
				// calls a language server UNTRUSTED INPUT: project config can load
				// plugins, so this is not the plain read it reads as.
				LaunchesSubprocess: true,
			},
			Handler: s.builtinLSPDefinition,
			// Says whether THIS call would start the server, so the prompt can
			// ask about the launch rather than about a lookup (register item 32).
			Launch: s.lspLaunchPlan,
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
				// Starts a language server (LSPBridge -> exec.Command). That file
				// calls a language server UNTRUSTED INPUT: project config can load
				// plugins, so this is not the plain read it reads as.
				LaunchesSubprocess: true,
			},
			Handler: s.builtinLSPReferences,
			// Says whether THIS call would start the server, so the prompt can
			// ask about the launch rather than about a lookup (register item 32).
			Launch: s.lspLaunchPlan,
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
				// The timeout and the programs it states are the ones the
				// handler uses (execTimeoutFor, execPrograms).
				Description:  s.sandboxExecDescriptionFor(execTimeoutFor(proposals)),
				Schema:       schema(execSchema),
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
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinSandboxExecStaged(ctx, raw, proposals)
			},
		},
	}

	// The network tools, when they are not switched off. Appended rather than
	// listed inline because they are the only built-ins whose PRESENCE is
	// configurable -- every other tool in this function exists unconditionally
	// and is governed only by policy.
	tools = append(tools, s.webTools()...)

	// repo_map is READ-ONLY and belongs to every mode, plan included.
	//
	// It used to sit in the `mode != "plan"` block below, alongside the two
	// propose_* tools, and so was withdrawn from plan mode as collateral -- not
	// by any decision about what plan mode should allow, but because it had been
	// typed inside the same append. Plan mode is the mode that most needs it:
	// working out what to change means finding out what exists first, and the
	// alternative to a repo map is the model guessing at paths.
	tools = append(tools, mcp.Builtin{
		Tool: mcp.Tool{
			Name: "repo_map",
			Description: "Show the shape of this workspace: every directory, the files in it, " +
				"and the top-level declarations of as many files as fit. Use it to find out what " +
				"exists before searching or guessing at a path. Takes no arguments.",
			Schema:       schema(`{"type":"object","properties":{},"additionalProperties":false}`),
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, _ json.RawMessage) (mcp.Result, error) {
			return s.builtinRepoMap(proposals.readCtx(ctx))
		},
	})

	if isCheckMode(mode) || isBuildMode(mode) {
		tools = append(tools, s.recordCriterionTool(proposals))
	}
	if isBuildMode(mode) || isLongTaskMode(mode) {
		tools = append(tools, s.updateTasksTool(proposals))
	}
	// A long task's own record and its gate (longtask.go), and the two tools
	// its work leans on hardest: exact search, and the history a regression
	// hunt needs. Offered in the long-task modes only, so an ordinary turn's
	// menu -- the measured default -- is unchanged.
	if isLongTaskMode(mode) {
		tools = append(tools, s.recordFindingTool(proposals), s.finishTaskTool(proposals),
			s.grepTool(proposals), s.gitHistoryTool(), s.checkpointTool(proposals),
			s.renameSymbolTool(proposals), s.investigateTool(proposals))
	}

	// propose_edit in every mode that edits: not plan, not check. In spec mode
	// it is limited to specs/ by the handler (proposalSink.specOnly).
	if !isPlanMode(mode) && !isCheckMode(mode) {
		tools = append(tools, mcp.Builtin{
			Tool: mcp.Tool{
				Name: "propose_edit",
				Description: "Propose an edit to a file, or create one (empty search). The edit is NOT applied: it is shown " +
					"to the user as a reviewable diff, which they accept or reject. Give the exact " +
					"existing text to replace and the text to replace it with. A workspace-relative path edits the " +
					"project; a ~/ path (for example ~/Desktop/notes/todo.md) edits or creates a file anywhere in the " +
					"user's home folder, creating folders as needed -- except hidden files and folders, ~/bin and " +
					".desktop files, which are always refused.",
				Schema: schema(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Workspace-relative path, or a ~/ path in the user's home folder."},
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
					// Reaches lspServerForFile to find the node, so it starts a
					// language server exactly as query_compiler_* does. Found by
					// TestNoBuiltinSpawnsWithoutDeclaringIt, not by reading: its
					// description says "using the native compiler" and nobody had
					// connected that phrase to exec.Command.
					LaunchesSubprocess: true,
				},
				Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
					return s.builtinProposeASTEdit(ctx, raw, proposals)
				},
				// The same probe as query_compiler_*: it reaches the same server.
				Launch: s.lspLaunchPlan,
			})
	}

	// PLAN MODE DROPS EVERY TOOL THAT EXECUTES CODE OR REACHES THE NETWORK.
	//
	// This used to be one line above -- `if mode != "plan"` removing
	// propose_edit -- and that was the whole of it. The effect was backwards:
	// plan mode withdrew the tool that proposes a REVIEWED edit and left
	// sandbox_exec, the one built-in that runs arbitrary code, registered
	// unconditionally. A user who picks a mode called "plan" has said they want
	// thinking and not doing; what they got was the safe way to change the
	// workspace removed and the unreviewed one kept.
	//
	// KEYED ON THE CAPABILITY, NOT ON THE NAME -- see planModeDenies, which is
	// where that argument now lives and where the correction was made. The fix
	// above replaced the name check with `t.ExecutesCode` and described itself
	// as capability-keyed; mcp.Tool declares TWO capability flags and this
	// tested one. web_search and web_fetch set ReachesNetwork, not
	// ExecutesCode, and were appended to `tools` a few lines up -- so they
	// survived a filter whose stated purpose they fell squarely inside.
	//
	// The lesson is narrower than "check both flags": a filter that names the
	// principle it follows ("the capability") and then implements one instance
	// of it reads as complete to every later reviewer. planModeDenies exists so
	// there is exactly one place where the set of withheld capabilities is
	// written down.
	// The long-task modes too: they withhold the web tools (modeWithholds),
	// and a tool withheld only at dispatch is still ON THE MENU -- offered,
	// then refused, every time the model reaches for it. MEASURED on the real
	// binary: a menu of 12 hid this by cutting the web tools off its
	// alphabetical end; raised to 16, they were advertised and refused.
	if isPlanMode(mode) || isSpecMode(mode) || isCheckMode(mode) || isLongTaskMode(mode) {
		kept := tools[:0]
		for _, b := range tools {
			if modeWithholds(mode, b.Tool) {
				continue
			}
			// A spec is written with propose_edit alone; a symbol-level edit
			// has no business in a Markdown file.
			if isSpecMode(mode) && b.Tool.Name == "propose_ast_edit" {
				continue
			}
			// A long task's menu is chosen, not just capped (longTaskOmits).
			if _, omitted := longTaskOmits[b.Tool.Name]; omitted && isLongTaskMode(mode) {
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

// readBoundedFile reads at most max bytes from path and reports the file's FULL
// size, so a caller can announce truncation honestly without having read the
// rest.
//
// THE CAP IS ON THE READ. os.ReadFile has no bounded form, so every caller that
// "capped" a file read in this package did it by slicing afterwards -- which
// bounds the result and not the allocation. Three handlers did that; this is
// the one place that does not.
//
// It stats through the OPEN FILE DESCRIPTOR rather than the path, so the size
// reported and the bytes read come from the same inode. A path-based os.Stat
// followed by a read is a TOCTOU window, and this package already refuses that
// shape elsewhere: chunker.go's readEligibleFile re-runs its eligibility gate
// immediately before reading for exactly this reason.
//
// Symlink semantics are os.ReadFile's, deliberately unchanged: callers reach
// here only through ResolveSafeTargetPath, which has already resolved and
// re-checked the real target.
func readBoundedFile(path string, max int64) (data []byte, fullSize int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	data, err = io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, 0, err
	}
	return data, fi.Size(), nil
}

func (s *Server) builtinReadFile(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return toolError("no path was supplied")
	}

	// Workspace paths as before; an outside path only with this call's
	// approval (see outsideread.go).
	full, err := s.resolveToolPath(ctx, args.Path)
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
	if args.StartLine != 0 || args.EndLine != 0 {
		return readFileLines(full, args.Path, args.StartLine, args.EndLine)
	}

	data, fullSize, err := readBoundedFile(full, maxBuiltinReadBytes)
	if err != nil {
		return toolError("cannot read %s", args.Path)
	}

	truncated := ""
	if fullSize > maxBuiltinReadBytes {
		// Truncation is ANNOUNCED. A model given a silently clipped file will
		// reason about the part it cannot see as if it were absent. fullSize,
		// not len(data): the whole point of readBoundedFile is that the rest
		// was never read, so the honest number comes from the stat.
		truncated = fmt.Sprintf("\n\n[... truncated: %s is %d bytes, showing the first %d ...]",
			args.Path, fullSize, maxBuiltinReadBytes)
	}
	return mcp.Result{Content: string(data) + truncated}, nil
}

// maxRangedReadBytes bounds the READ behind a ranged read_file. A file far
// past maxBuiltinReadBytes can be read a slice at a time, which is the point;
// what goes back is still bounded by maxBuiltinReadBytes.
const maxRangedReadBytes = 8 << 20

// readFileLines returns lines start..end (1-based, inclusive; end 0 is the
// last line) numbered, so a finding or an edit can cite them -- the way a long
// task reads a large file, or one part of it, without paying for the rest.
func readFileLines(full, shown string, start, end int) (mcp.Result, error) {
	if start < 1 {
		start = 1
	}
	data, size, err := readBoundedFile(full, maxRangedReadBytes)
	if err != nil {
		return toolError("cannot read %s", shown)
	}
	if size > maxRangedReadBytes {
		return toolError("%s is %d bytes; a ranged read opens files up to %d MB", shown, size, maxRangedReadBytes>>20)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if start > len(lines) {
		return toolError("%s has %d lines; start_line %d is past its end", shown, len(lines), start)
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end < start {
		return toolError("end_line %d is before start_line %d", end, start)
	}
	var b strings.Builder
	width := len(strconv.Itoa(end))
	last := start - 1
	for i := start; i <= end; i++ {
		line := fmt.Sprintf("%*d| %s\n", width, i, lines[i-1])
		if b.Len()+len(line) > maxBuiltinReadBytes {
			fmt.Fprintf(&b, "[... stopped at line %d: one read returns at most %d bytes; ask for the rest from line %d ...]\n",
				last, maxBuiltinReadBytes, last+1)
			break
		}
		b.WriteString(line)
		last = i
	}
	fmt.Fprintf(&b, "[%s: lines %d-%d of %d]", shown, start, last, len(lines))
	return mcp.Result{Content: b.String()}, nil
}

func (s *Server) builtinListDirectory(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}

	full, err := s.resolveToolPath(ctx, args.Path)
	if err != nil {
		return toolError("cannot list %s: %v", args.Path, err)
	}

	dir, err := os.Open(full)
	if err != nil {
		return toolError("cannot list %s", args.Path)
	}
	// ReadDir on the HANDLE, with a count: os.ReadDir's convenience form reads
	// every entry before returning. Asking for one more than the scan cap is
	// what distinguishes "exactly at the cap" from "more than we looked at".
	entries, err := dir.ReadDir(maxBuiltinListScan + 1)
	_ = dir.Close()
	if err != nil && err != io.EOF {
		return toolError("cannot list %s", args.Path)
	}
	scanTruncated := len(entries) > maxBuiltinListScan
	if scanTruncated {
		entries = entries[:maxBuiltinListScan]
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
	// Stated separately from the result cap above, because they mean different
	// things (M5): one says "more names exist and were counted", the other says
	// "this directory is larger than we were willing to look at, so the names
	// shown are the first %d scanned rather than the first alphabetically".
	if scanTruncated {
		truncated += fmt.Sprintf("\n[... this directory holds more than %d entries; "+
			"only the first %d were examined, so this listing may not be alphabetically complete ...]",
			maxBuiltinListScan, maxBuiltinListScan)
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
	stale := map[string]bool{}
	st := stageFromCtx(ctx)
	for i, chunk := range outcome.Chunks {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(renderChunk(i, chunk, s.noScrub()))
		if st != nil && st.touched[filepath.FromSlash(chunk.FilePath)] {
			stale[chunk.FilePath] = true
		}
	}
	// THE INDEX DESCRIBES THE PROJECT, NOT THE WORKING COPY. A hit in a file
	// this turn changed shows the text as it was; said, so the model re-reads
	// rather than edits against lines that are gone.
	if len(stale) > 0 {
		names := make([]string, 0, len(stale))
		for n := range stale {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "\n\n(You changed %s this turn; the results above show it as it was before. "+
			"read_file shows it as it is now.)", strings.Join(names, ", "))
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
// applyInWorkingCopy makes one edit in the turn's working copy. ok is false
// when it did not take the edit -- no working copy, or a path outside the
// project, which stays a proposal against the real file as before.
func applyInWorkingCopy(proposals *proposalSink, block editapply.EditBlock) (mcp.Result, bool) {
	st, _ := proposals.workingCopy()
	if st == nil {
		return mcp.Result{}, false
	}
	rel, inside := st.relFor(block.FilePath)
	if !inside {
		return mcp.Result{}, false
	}
	shown := block.FilePath
	block.FilePath = rel
	prepared, err := st.apply(block)
	if err != nil {
		res, _ := toolError("that edit cannot be applied: %s", st.toReal(err.Error()))
		return res, true
	}
	what := fmt.Sprintf("changed lines %d-%d of %s", prepared.StartLine, prepared.EndLine, shown)
	if prepared.Creates {
		what = "created " + shown
	}
	if prepared.MatchNote != "" {
		what += " (" + prepared.MatchNote + ")"
	}
	return mcp.Result{Content: "Done: " + what + " in this turn's working copy of the project. " +
		"read_file now shows the file as it is, and sandbox_exec runs against it. Nothing reaches the " +
		"user's real files until they review all of this turn's changes when you finish -- so do not " +
		"repeat this edit; build on it."}, true
}

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

	// /spec writes a spec and nothing else. The only control that matters is
	// this one: the handler is the only way propose_edit writes.
	if proposals != nil && proposals.specOnly {
		if _, ok := specRelPath(args.Path); !ok {
			return toolError("in /spec mode the only file you can write is the spec itself, a .md file "+
				"under %s/; %s is not one", specsDir, args.Path)
		}
	}

	block := editapply.EditBlock{FilePath: args.Path, Search: args.Search, Replace: args.Replace}

	// IN THE WORKING COPY when the turn has one: the edit is made there, so
	// the next read shows it and the next command builds it.
	if res, ok := applyInWorkingCopy(proposals, block); ok {
		return res, nil
	}

	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("that edit cannot be applied: %v", err)
	}
	prepared, err := editapply.PrepareEditAnywhere(realRoot, block)
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
