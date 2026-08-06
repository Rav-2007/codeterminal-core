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
	// NeedsArgs: if true, a bare "/name" shows usage instead of running.
	NeedsArgs bool
}

var slashCatalog = []slashDef{
	{Name: "help", Kind: slashLocal, Summary: "list slash commands"},
	{Name: "model", Kind: slashLocal, Summary: "list or select a models.json tier (/model <name>)"},
	{Name: "mcp-server", Kind: slashLocal, Summary: "show configured MCP servers and tool policy"},
	{Name: "clear", Kind: slashLocal, Summary: "clear the on-screen transcript"},
	{Name: "compact", Kind: slashLocal, Summary: "drop older turns; keep the last few"},
	{Name: "context", Kind: slashLocal, Summary: "show workspace, model tier, and last grounding"},
	{Name: "git", Kind: slashLocal, Summary: "show git status for the workspace"},
	{Name: "init", Kind: slashLocal, Summary: "quick start checklist for this workspace"},
	{Name: "search", Kind: slashLocal, Summary: "search past conversation turns (/search <query>)", NeedsArgs: true},
	{Name: "exit", Kind: slashLocal, Summary: "quit the TUI"},

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
	{Name: "plan", Kind: slashSteered, NeedsArgs: true, Summary: "make an implementation plan",
		Preamble: "Produce a concise implementation plan with ordered steps and key files. Do not write full code yet unless asked.\n\n"},
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
	names := make([]string, 0, len(slashCatalog)+1)
	for _, d := range slashCatalog {
		names = append(names, d.Name)
	}
	names = append(names, "model")
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
	return strings.TrimRight(b.String(), "\n")
}

func steeredPrompt(def *slashDef, args string) string {
	if def.Preamble == "" {
		return args
	}
	return def.Preamble + args
}

func runGitStatus(workspace string) string {
	dir := workspace
	if dir == "" {
		dir = "."
	}
	cmd := exec.Command("git", "-C", dir, "status", "-sb")
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
const daemonBinEnvVar = "CODETERMINAL_DAEMON_BIN"

// resolveDaemonBin finds the daemon binary WITHOUT ever trusting the working
// directory.
//
// This used to try "daemon/codeterminal-daemon" and
// "../../daemon/codeterminal-daemon" first, both relative to the CWD. The TUI's
// only mode of use is to run it from inside the repository you are working on,
// so those two candidates meant: a repository that ships an executable at
// daemon/codeterminal-daemon gets it EXECUTED when the user types /mcp-server.
//
// Measured before this change: a temp repo containing that file ran attacker
// code and read CODETERMINAL_API_KEY out of the inherited environment. No
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
			filepath.Join(dir, "codeterminal-daemon"),
			filepath.Join(dir, "..", "daemon", "codeterminal-daemon"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}

	if p, err := exec.LookPath("codeterminal-daemon"); err == nil {
		return p
	}
	return ""
}

func runMCPServerList(configPath string) string {
	bin := resolveDaemonBin()
	if bin == "" {
		return "codeterminal-daemon binary not found — build it with:\n" +
			"  (cd daemon && go build -o codeterminal-daemon .)\n" +
			"then put it next to this binary or on PATH, or set " + daemonBinEnvVar +
			" to its path, and retry /mcp-server"
	}
	args := []string{"mcp", "list"}
	if configPath != "" {
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
  1. Daemon running with --config ./models.json
  2. CODETERMINAL_API_KEY set (OpenRouter) OR run-proxy.sh for managed proxy
  3. Optional: ./daemon/codeterminal-daemon index %s
  4. /model to pick a model; /mcp-server to see agent tools
  5. /help for all slash commands`, workspace, workspace))
}
