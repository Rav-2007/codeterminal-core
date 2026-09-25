package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Slash command surface for the TUI (and mirrored in the VS Code client).
//
// Two families:
//
//   - local: handled entirely in the client (help, model, mcp-server, git, …)
//   - steered: the user's remainder is sent to the model with a fixed task
//     preamble so the model knows what job to do
//
// /reason and /refactor keep their existing PromptKind wire values (reasoning
// tier escalation) and also apply a short steered preamble.

// modePlan is protocol.PromptRequest.Mode's plan value. Must equal the daemon's
// modePlan; see slashDef.Mode.
const modePlan = "plan"

type slashKind int

const (
	slashLocal slashKind = iota
	slashSteered
)

type slashDef struct {
	Name    string
	Kind    slashKind
	Summary string
	// Preamble is prepended for steered commands. Empty for local.
	Preamble string
	// PromptKind is the optional wire PromptKind (/reason, /refactor).
	PromptKind string
	// Mode is the optional wire Mode (protocol.PromptRequest.Mode). Set only by
	// /plan. MUST match the daemon's modePlan exactly -- the two packages are
	// separate and nothing compiles one against the other, the same
	// convention-and-comment contract PromptKind's wire values carry above.
	Mode string
	// NeedsArgs: if true, a bare "/name" shows usage instead of running.
	NeedsArgs bool
}

var slashCatalog = []slashDef{
	{Name: "help", Kind: slashLocal, Summary: "list slash commands"},
	{Name: "model", Kind: slashLocal, Summary: "list or select a models.json tier (/model <name>)"},
	{Name: "mcp-server", Kind: slashLocal, Summary: "show configured MCP servers and tool policy"},
	{Name: "clear", Kind: slashLocal, Summary: "clear the on-screen transcript"},
	{Name: "compact", Kind: slashLocal, Summary: "keep only the last 8 turns, now"},
	{Name: "mouse", Kind: slashLocal, Summary: "toggle mouse capture: on scrolls with the wheel, off lets you select text"},
	{Name: "context", Kind: slashLocal, Summary: "show workspace, model tier, and last grounding"},
	{Name: "git", Kind: slashLocal, Summary: "show git status for the workspace"},
	{Name: "init", Kind: slashLocal, Summary: "quick start checklist for this workspace"},
	{Name: "search", Kind: slashLocal, Summary: "search past conversation turns (/search <query>)", NeedsArgs: true},
	{Name: "connect", Kind: slashLocal, Summary: "set the provider API key (/connect show, /connect forget)"},
	{Name: "exit", Kind: slashLocal, Summary: "quit the TUI"},

	// /team is in the catalog to be FOUND, and parsed elsewhere.
	//
	// The pipeline it runs (daemon/orchestrator.go, four specialist roles, an
	// A/B-measured default shape) has been complete and reachable-in-principle
	// since it was written, and reachable-in-practice by nobody: the wire field
	// existed, the daemon resolved it, and the command that sends it appeared in
	// no help text and no autocomplete popup. A capability the user cannot find
	// is not a capability, which is the whole reason this line exists.
	//
	// Kind is steered because that is what it is -- a turn that goes to the
	// model -- but it carries NO preamble: what it steers is the pipeline shape
	// on the wire, not the text. parseSlash hands it straight past (see there),
	// exactly as it does /model.
	{Name: "team", Kind: slashSteered, NeedsArgs: true,
		Summary: "run one turn through specialists (/team:a,b …)"},

	{Name: "explain", Kind: slashSteered, NeedsArgs: true, Summary: "explain code or a concept",
		Preamble: "Explain clearly and thoroughly. Use concrete references from the workspace when relevant.\n\n"},
	{Name: "fix", Kind: slashSteered, NeedsArgs: true, Summary: "find and fix a bug",
		Preamble: "You are fixing a bug. Diagnose first, then propose a minimal correct fix with edits.\n\n"},
	{Name: "test", Kind: slashSteered, NeedsArgs: true, Summary: "add or improve tests",
		Preamble: "Write or improve tests for the described code. Prefer existing test style in this repo.\n\n"},
	{Name: "refactor", Kind: slashSteered, NeedsArgs: true, Summary: "refactor code (may escalate to reasoning tier)",
		Preamble:   "Refactor for clarity and maintainability without changing behavior. Propose focused edits.\n\n",
		PromptKind: promptKindRefactor},
	{Name: "doc", Kind: slashSteered, NeedsArgs: true, Summary: "write or improve documentation",
		Preamble: "Write or improve documentation. Match the project's existing doc tone.\n\n"},
	{Name: "security", Kind: slashSteered, NeedsArgs: true, Summary: "security review of the described code",
		Preamble: "Perform a security review. Call out concrete risks, severity, and mitigations. Do not invent exploits.\n\n"},
	{Name: "review", Kind: slashSteered, NeedsArgs: true, Summary: "code review",
		Preamble: "Review the code as a careful senior engineer. Separate blockers from suggestions.\n\n"},
	// /plan SENDS A MODE, and no longer a preamble.
	//
	// It used to be steering text prepended to the user's own message, which
	// meant the TUI's plan command and the VS Code one shared a name and nothing
	// else: VS Code set PromptRequest.Mode and got the daemon's tool filter,
	// while this sent a polite request and got the full menu, sandbox_exec
	// included. A user typing /plan here was told the model would plan rather
	// than act, and nothing enforced it.
	//
	// Dropping the preamble is not a loss. The daemon appends the same
	// instruction for mode=plan (planModeSystemPrompt) into the SYSTEM role,
	// where a preamble concatenated onto the user's text could never go -- and
	// planmode.go records at length why instructions to the model do not belong
	// in user-role text.
	{Name: "plan", Kind: slashSteered, NeedsArgs: true, Summary: "make an implementation plan (read-only: no edits, no commands, no network)",
		Mode: modePlan},
	{Name: "run", Kind: slashSteered, NeedsArgs: true, Summary: "suggest how to run/build/test something",
		Preamble: "Explain exactly which commands to run, from which directory, and what success looks like.\n\n"},

	{Name: "implement", Kind: slashSteered, NeedsArgs: true, Summary: "implement a new feature from end-to-end",
		Preamble: "Implement the following feature from end-to-end. Break down the work into logical steps and execute them. Do not hesitate to use tools like `propose_edit` and `run_command`.\n\n"},
	{Name: "debug", Kind: slashSteered, NeedsArgs: true, Summary: "deeply debug an issue, error, or failing test",
		Preamble: "You are an expert debugger. Investigate the following issue deeply. Run tests, add logging, and examine state until the root cause is found, then propose a fix.\n\n"},
	{Name: "explore", Kind: slashSteered, NeedsArgs: true, Summary: "explore the codebase to gather context",
		Preamble: "Explore the codebase to understand the following concept or component. Read files, grep for usages, and build a comprehensive understanding before answering.\n\n"},
	{Name: "research", Kind: slashSteered, NeedsArgs: true, Summary: "research a topic comprehensively",
		Preamble: "Research the following topic comprehensively. Use search and read tools as needed. Provide a detailed summary of your findings.\n\n"},

	// Keep legacy /reason as steered+PromptKind (not in the user list, but already shipped).
	{Name: "reason", Kind: slashSteered, NeedsArgs: true, Summary: "deep reasoning pass (may escalate tier)",
		Preamble:   "Think carefully and show rigorous reasoning before the answer.\n\n",
		PromptKind: promptKindReason},
}

// Deduplicate catalog if the source was edited with accidental duplicates.
func init() {
	seen := map[string]bool{}
	out := slashCatalog[:0]
	for _, d := range slashCatalog {
		if seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	slashCatalog = out
}

type slashParse struct {
	Def  *slashDef
	Args string
	// RawPassthrough means this was not a slash command.
	RawPassthrough bool
	UsageOnly      bool // bare command that NeedsArgs
}

func lookupSlash(name string) *slashDef {
	for i := range slashCatalog {
		if slashCatalog[i].Name == name {
			return &slashCatalog[i]
		}
	}
	return nil
}

// parseSlash inspects trimmed input. Non-commands return RawPassthrough.
// "/model …" is handled by parseModelCommand separately (existing path).
func parseSlash(raw string) slashParse {
	if raw == "" || raw[0] != '/' {
		return slashParse{RawPassthrough: true}
	}
	// /model is its own command family (list/select).
	if raw == "/model" || strings.HasPrefix(raw, "/model ") {
		return slashParse{RawPassthrough: true}
	}
	// /team is the same arrangement as /model, and the ORDER IS LOAD-BEARING.
	//
	// startTurn runs parseSlash BEFORE parseTeamCommand, so the moment "team"
	// appears in the catalog, "/team fix this" starts matching here and routes
	// to handleSlash -- a steered command with no preamble, which sends the raw
	// text and no pipeline. Adding the catalog entry would then have broken the
	// very command it was advertising, silently, with every existing test still
	// green (they call parseTeamCommand directly). Hence this exclusion, and
	// TestParseSlashHandsTheTeamCommandOnward, which fails if it is ever
	// deleted.
	//
	// The bare form is the one exception: it has no question to ask, so it gets
	// the usage line every other NeedsArgs command gets, rather than being sent
	// to the model as the literal text "/team".
	if raw == "/team" {
		return slashParse{Def: lookupSlash("team"), UsageOnly: true}
	}
	if strings.HasPrefix(raw, commandTeam) || strings.HasPrefix(raw, commandTeamShape) {
		return slashParse{RawPassthrough: true}
	}
	body := strings.TrimPrefix(raw, "/")
	name, rest, _ := strings.Cut(body, " ")
	name = strings.ToLower(strings.TrimSpace(name))
	rest = strings.TrimSpace(rest)
	def := lookupSlash(name)
	if def == nil {
		return slashParse{RawPassthrough: true}
	}
	if def.NeedsArgs && rest == "" {
		return slashParse{Def: def, UsageOnly: true}
	}
	return slashParse{Def: def, Args: rest}
}

func formatSlashHelp() string {
	var b strings.Builder
	b.WriteString("slash commands:\n")
	// DEDUPED, because "model" is both in the catalog and appended below, and
	// the un-deduped version printed /model twice in every /help this product
	// has ever shown. The VS Code mirror used a Set and never had the bug (see
	// slashAutocompleteNames in slashCommands.ts); this is that behaviour.
	seen := make(map[string]bool, len(slashCatalog)+1)
	names := make([]string, 0, len(slashCatalog)+1)
	for _, d := range slashCatalog {
		if !seen[d.Name] {
			seen[d.Name] = true
			names = append(names, d.Name)
		}
	}
	if !seen["model"] {
		names = append(names, "model")
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "model" {
			fmt.Fprintf(&b, "  /%-12s %s\n", "model", "list or select a models.json tier")
			continue
		}
		d := lookupSlash(name)
		if d == nil {
			continue
		}
		fmt.Fprintf(&b, "  /%-12s %s\n", d.Name, d.Summary)
	}
	// The keys, listed here because /help is where someone looks for "how do I
	// make it do X" -- and the one they most need mid-turn is the one they
	// cannot go looking for once a turn is running.
	b.WriteString("\nkeys:\n")
	b.WriteString("  esc            stop the turn in flight; the transcript is kept\n")
	b.WriteString("  ctrl+c         stop the turn in flight, or quit when nothing is running\n")
	b.WriteString("  ctrl+n         start a new conversation\n")

	// THE AUTOMATIC BOUND, said here because /help is where someone goes after
	// seeing the eviction marker and wondering what took their transcript.
	// This is a TUI-only paragraph rather than part of the shared catalog: the
	// VS Code client has no such bound, and a mirrored summary that claimed it
	// did would be a lie in the other client (slash_clientparity_test.go is
	// what stops that happening by accident).
	fmt.Fprintf(&b, "\ntranscript:\n"+
		"  a long session trims its own oldest turns once it passes %d turns or %s\n"+
		"  of text, and says so in the transcript when it does. /compact is the\n"+
		"  deliberate version and keeps far less. Set %s or\n"+
		"  %s to change the ceilings.\n",
		defaultMaxTurns, humanBytes(defaultMaxTranscriptBytes), maxTurnsEnv, maxBytesEnv)
	return strings.TrimRight(b.String(), "\n")
}

func steeredPrompt(def *slashDef, args string) string {
	if def.Preamble == "" {
		return args
	}
	return def.Preamble + args
}

// neutralisedGitConfig lists the config keys whose values git EXECUTES as
// commands. They are overridden on the command line, where -c beats anything
// the repository's own .git/config says.
//
// `git status` reads the config of the repository it is pointed at, and
// core.fsmonitor names a command it then runs. CONFIRMED BY EXECUTION
// 2026-08-08: a planted core.fsmonitor script ran during
// `git -C <repo> status -sb`. /git is one keystroke away for the user.
//
// REACHABILITY. `git clone` does not transfer config, so cloning a hostile
// repository does not carry this. Every other way a folder arrives does: an
// unzipped release archive containing .git/, a synced directory, a container
// mount. Git's safe.directory ownership check does not help -- it refuses
// repositories owned by a DIFFERENT user, and a folder you unzipped is yours.
//
// Mirrored verbatim in clients/vscode/src/safeGit.ts. Two clients ran the same
// command and only one of them being fixed is precisely how the /mcp-server
// hijack survived d56e425.
var neutralisedGitConfig = []string{
	"core.fsmonitor=",
	"core.pager=cat",
	"core.sshCommand=",
	"core.alternateRefsCommand=",
	"diff.external=",
	"uploadpack.packObjectsHook=",
}

// safeGitArgs builds the argument vector for git inside an untrusted
// repository. The -c overrides must precede -C, or the repository is selected
// before they apply.
func safeGitArgs(dir string, subcommand ...string) []string {
	args := []string{"--no-pager"}
	for _, kv := range neutralisedGitConfig {
		args = append(args, "-c", kv)
	}
	// A read-only question must not write to a repository the user only asked
	// about.
	args = append(args, "--no-optional-locks", "-C", dir)
	return append(args, subcommand...)
}

func runGitStatus(workspace string) string {
	// An empty workspace is a REFUSAL, not a fallback to ".". The TUI's working
	// directory is whatever the user happened to launch it from, and reporting
	// on that is reporting on a repository nobody asked about -- the same
	// working-directory trust that d56e425 removed from this very file.
	if workspace == "" {
		return "no workspace is set, so there is no repository to report on"
	}
	cmd := exec.Command("git", safeGitArgs(workspace, "status", "-sb")...)
	// Not the workspace. Every path here is absolute, and staying out of the
	// repository keeps it that way if a relative one is ever added.
	cmd.Dir = ""
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git status failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "(git status: empty)"
	}
	return s
}

// daemonBinEnvVar lets a developer point /mcp-server at a daemon built
// somewhere else. An explicit variable the USER sets is a trusted input; the
// working directory is not.
const daemonBinEnvVar = "MOCHIII_DAEMON_BIN"

// resolveDaemonBin finds the daemon binary WITHOUT ever trusting the working
// directory.
//
// This used to try "daemon/mochiii-daemon" and
// "../../daemon/mochiii-daemon" first, both relative to the CWD. The TUI's
// only mode of use is to run it from inside the repository you are working on,
// so those two candidates meant: a repository that ships an executable at
// daemon/mochiii-daemon gets it EXECUTED when the user types /mcp-server.
//
// Measured before this change: a temp repo containing that file ran attacker
// code and read MOCHIII_API_KEY out of the inherited environment. No
// approval prompt stands in front of a slash command.
//
// Every candidate below is anchored to something the user controls — an
// explicit environment variable, the location of the running binary, or PATH.
// None is relative to whatever directory the TUI happens to have been started
// in. exec.LookPath is used for the bare name because Go resolves a
// separator-free argument through PATH anyway; doing it here makes the check
// and the execution agree on one path rather than statting the CWD and then
// executing something else.
func resolveDaemonBin() string {
	if override := strings.TrimSpace(os.Getenv(daemonBinEnvVar)); override != "" {
		return override
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "mochiii-daemon"),
			filepath.Join(dir, "..", "daemon", "mochiii-daemon"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}

	if p, err := exec.LookPath("mochiii-daemon"); err == nil {
		return p
	}
	return ""
}

func runMCPServerList(configPath string) string {
	bin := resolveDaemonBin()
	if bin == "" {
		return "mochiii-daemon binary not found — build it with:\n" +
			"  (cd daemon && go build -o mochiii-daemon .)\n" +
			"then put it next to this binary or on PATH, or set " + daemonBinEnvVar +
			" to its path, and retry /mcp-server"
	}
	args := []string{"mcp", "list"}
	// A RELATIVE config path is refused outright rather than passed through.
	//
	// `mcp list` STARTS the servers a config names, so --config is a
	// code-execution argument, not a display option. A relative path resolves
	// against this process's working directory, which is the repository the user
	// opened -- and that is how a workspace models.json got its command run
	// (CONFIRMED by execution 2026-08-08).
	//
	// An ABSOLUTE path is still honoured: that is a developer pointing at a
	// config they chose, which is a trusted input in the same way
	// MOCHIII_DAEMON_BIN is. Empty means "let the daemon resolve its own",
	// which is what every ordinary caller now passes.
	if configPath != "" {
		if !filepath.IsAbs(configPath) {
			return "refusing a relative --config path (" + configPath + "): it would resolve " +
				"against the current directory, and `mcp list` STARTS the servers a config names. " +
				"Pass an absolute path, or none to use the daemon's own config."
		}
		args = append(args, "--config", configPath)
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("mcp list failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "no MCP servers configured (agent mode off or empty mcp.servers)"
	}
	return s
}

func formatInitChecklist(workspace string) string {
	return strings.TrimSpace(fmt.Sprintf(`workspace init checklist:
  workspace: %s
  1. Daemon running (it finds models.json beside its own binary)
  2. MOCHIII_API_KEY set (OpenRouter) OR run-proxy.sh for managed proxy
  3. Optional: ./daemon/mochiii-daemon index %s
  4. /model to pick a model; /mcp-server to see agent tools
  5. /help for all slash commands`, workspace, workspace))
}
