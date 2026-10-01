package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// git_history: the project's history, read-only -- the regression question a
// long debugging session keeps coming back to: what changed, in which commit,
// and when did this line take its present form.
//
// GIT READS THE REPOSITORY'S OWN CONFIG, AND SOME OF IT IS COMMANDS. A folder
// can arrive with a .git/config written by anyone (an unzipped archive, a
// synced directory, a container mount -- clients/tui/slash.go records the
// history), and log, show, diff and blame each reach config that git runs:
//
//   - core.fsmonitor, core.pager, diff.external -- the TUI's /git list,
//     mirrored here and kept equal by a test;
//   - log.showSignature, which makes log and show run gpg.program;
//   - a diff driver's textconv, chosen by .gitattributes in the tree itself
//     (--no-textconv), and external diff drivers (--no-ext-diff);
//   - clean filters, which run when a diff compares against the WORKING TREE.
//     No subcommand here touches the working tree: diff compares two commits,
//     and blame starts from a commit.
//
// Each override is on the command line, where -c beats every config file, and
// TestGitHistoryRunsNothingAHostileRepositoryPlants proves by running it that
// none of the planted commands runs.
//
// WHAT IT SHOWS IS WHAT read_file COULD SHOW. A path argument passes the same
// gate; and since a commit can hold a secret file read_file would refuse -- a
// committed .env -- every file section of show and diff output whose name is
// secret-shaped or under a protected folder is withheld, and said so.

// gitHistoryNeutralised overrides the config keys whose values git executes on
// these four subcommands. The first six are clients/tui/slash.go's
// neutralisedGitConfig verbatim (TestGitHistoryNeutralisesWhatTheTUIDoes).
var gitHistoryNeutralised = []string{
	"core.fsmonitor=",
	"core.pager=cat",
	"core.sshCommand=",
	"core.alternateRefsCommand=",
	"diff.external=",
	"uploadpack.packObjectsHook=",
	"log.showSignature=false",
}

const (
	gitHistoryTimeout    = 15 * time.Second
	gitHistoryMaxBytes   = 64 << 10
	gitHistoryMaxLog     = 50
	gitHistoryLogDefault = 20
)

// gitRevPattern is a revision a model may name: a hash, a branch or tag, HEAD
// with ~ and ^, or HEAD@{n}. No leading '-' (it would be an option), no ':'
// (HEAD:.env reads a file at a revision, past the path gate), no "..": two
// revisions are two arguments.
var gitRevPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./~^@{}-]{0,99}$`)

var gitBlameLines = regexp.MustCompile(`^[0-9]{1,7},[0-9]{1,7}$`)

func validGitRev(rev string) bool {
	return gitRevPattern.MatchString(rev) && !strings.Contains(rev, "..")
}

// gitHistoryArgs builds the argument vector: overrides first (they must
// precede -C, or the repository is selected before they apply), then the
// subcommand.
func gitHistoryArgs(root string, sub ...string) []string {
	args := []string{"--no-pager"}
	for _, kv := range gitHistoryNeutralised {
		args = append(args, "-c", kv)
	}
	args = append(args, "--no-optional-locks", "-C", root)
	return append(args, sub...)
}

func (s *Server) builtinGitHistory(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Command string `json:"command"`
		Rev     string `json:"rev"`
		Rev2    string `json:"rev2"`
		Path    string `json:"path"`
		Lines   string `json:"lines"`
		Max     int    `json:"max"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	root, err := s.realWorkspaceRoot()
	if err != nil {
		return toolError("cannot read the history: %v", err)
	}
	for _, rev := range []string{args.Rev, args.Rev2} {
		if rev != "" && !validGitRev(rev) {
			return toolError("%q is not a revision this tool accepts: give a hash, a branch or tag, or HEAD~n", rev)
		}
	}
	path := strings.TrimSpace(args.Path)
	if path != "" {
		if filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
			return toolError("give a project-relative path")
		}
		if _, err := editapply.ResolveSafeTargetPath(root, path); err != nil {
			return toolError("cannot read the history of %s: %v", path, err)
		}
		path = filepath.ToSlash(filepath.Clean(path))
	}
	rev := args.Rev

	var sub []string
	switch strings.ToLower(strings.TrimSpace(args.Command)) {
	case "log":
		n := args.Max
		if n <= 0 {
			n = gitHistoryLogDefault
		}
		n = min(n, gitHistoryMaxLog)
		sub = []string{"log", "-n", strconv.Itoa(n), "--no-show-signature", "--stat", "--date=short",
			"--format=commit %h  %ad  %an%n    %s%n", "--end-of-options"}
		if rev != "" {
			sub = append(sub, rev)
		}
	case "show":
		if rev == "" {
			rev = "HEAD"
		}
		sub = []string{"show", "--no-show-signature", "--no-textconv", "--no-ext-diff", "--stat", "--patch",
			"--date=short", "--end-of-options", rev}
	case "diff":
		// Two commits, never the working tree (see the file comment): one
		// revision means that commit's own change.
		if rev == "" {
			return toolError("diff needs rev (and rev2 to compare two commits); it never compares the working tree")
		}
		from, to := rev+"^", rev
		if args.Rev2 != "" {
			from, to = rev, args.Rev2
		}
		sub = []string{"diff", "--no-textconv", "--no-ext-diff", "--ignore-submodules=all", "--stat", "--patch",
			"--end-of-options", from, to}
	case "blame":
		if path == "" {
			return toolError("blame needs path")
		}
		if rev == "" {
			rev = "HEAD"
		}
		sub = []string{"blame", "--no-textconv", "--date=short"}
		if args.Lines != "" {
			if !gitBlameLines.MatchString(args.Lines) {
				return toolError("lines is first,last -- for example 40,80")
			}
			sub = append(sub, "-L", args.Lines)
		}
		// No --end-of-options: blame's parser refuses it (git 2.43). The
		// revision cannot be read as an option anyway -- validGitRev refuses a
		// leading '-'.
		sub = append(sub, rev)
	default:
		return toolError("command must be log, show, diff or blame")
	}
	if path != "" {
		sub = append(sub, "--", path)
	}

	runCtx, cancel := context.WithTimeout(ctx, gitHistoryTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", gitHistoryArgs(root, sub...)...)
	cmd.Dir = root
	cmd.Env = gitHistoryEnv()
	out := &headBuffer{max: gitHistoryMaxBytes}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return toolError("git %s took longer than %s", sub[0], gitHistoryTimeout)
		}
		return toolError("git %s failed: %s", sub[0], strings.TrimSpace(lastNLines(out.String(), 6)))
	}
	text, withheld := withholdSecretFileSections(out.String())
	if withheld > 0 {
		text += fmt.Sprintf("\n[%d file section(s) withheld: secret-shaped names or protected folders, "+
			"the files read_file refuses]", withheld)
	}
	if out.dropped > 0 {
		text += fmt.Sprintf("\n[... truncated: %d more bytes not shown; narrow it with path, or log with max ...]", out.dropped)
	}
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return mcp.Result{Content: text}, nil
}

// gitHistoryEnv is the environment git runs in: the user's own PATH and HOME
// (their global config is theirs to trust), no system config, no prompts, no
// locks, and a fixed language so the output parses the same everywhere.
func gitHistoryEnv() []string {
	env := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"}
	for _, name := range []string{"PATH", "HOME"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// withholdSecretFileSections drops each "diff --git" section of a patch that
// names a file read_file would refuse -- on EITHER side, so a commit renaming
// notes.txt to .env is withheld too -- and counts them.
func withholdSecretFileSections(patch string) (string, int) {
	var b strings.Builder
	hiding, withheld := false, 0
	for _, line := range strings.SplitAfter(patch, "\n") {
		if paths, ok := diffSectionPaths(line); ok {
			hiding = false
			for _, p := range paths {
				if refusedHistoryPath(p) {
					hiding = true
				}
			}
			if hiding {
				withheld++
			}
		}
		if !hiding {
			b.WriteString(line)
		}
	}
	return b.String(), withheld
}

// diffSectionPaths is every file a patch section header names, or false when
// line is not a header.
//
// GIT QUOTES A PATH holding anything unusual -- a non-ASCII letter, a tab, a
// quote -- as a C string: diff --git "a/conf \303\251/x.pem" "b/...". Such a
// header used to go unrecognised, and its section through. FOUND 2026-10-01,
// with only the a/ side checked as well: a rename TO .env showed the new file.
func diffSectionPaths(line string) ([]string, bool) {
	line = strings.TrimRight(line, "\n")
	if rest, ok := strings.CutPrefix(line, "diff --git "); ok {
		return diffGitPaths(rest), true
	}
	for _, prefix := range []string{"diff --cc ", "diff --combined "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return []string{unquoteGitPath(rest)}, true
		}
	}
	return nil, false
}

// diffGitPaths splits a "diff --git" header's two sides. An unquoted side can
// hold " b/" itself, so EVERY place the second side could begin gives a
// candidate pair: withholding a section too many is safe, one too few is the
// leak.
func diffGitPaths(rest string) []string {
	var out []string
	if strings.HasPrefix(rest, `"`) {
		if q, err := strconv.QuotedPrefix(rest); err == nil {
			return []string{unquoteGitPath(q), unquoteGitPath(strings.TrimPrefix(rest[len(q):], " "))}
		}
	}
	for i := 0; i < len(rest); i++ {
		if strings.HasPrefix(rest[i:], ` "b/`) || strings.HasPrefix(rest[i:], " b/") {
			out = append(out, unquoteGitPath(rest[:i]), unquoteGitPath(rest[i+1:]))
		}
	}
	if len(out) == 0 {
		out = append(out, unquoteGitPath(rest))
	}
	return out
}

// unquoteGitPath undoes git's quoting of one side (Go's string escapes are a
// superset of git's: \t, \", \\, and \ooo octal bytes), then drops its a/ or
// b/ prefix. A side that will not unquote is judged as written.
func unquoteGitPath(side string) string {
	if strings.HasPrefix(side, `"`) {
		if u, err := strconv.Unquote(side); err == nil {
			side = u
		}
	}
	for _, prefix := range []string{"a/", "b/"} {
		if p, ok := strings.CutPrefix(side, prefix); ok {
			return p
		}
	}
	return side
}

// refusedHistoryPath reports whether read_file would refuse this path by name.
func refusedHistoryPath(p string) bool {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for _, part := range parts[:len(parts)-1] {
		if editapply.IsProtectedDirName(part) {
			return true
		}
	}
	return editapply.MatchesSecretName(parts[len(parts)-1])
}

// headBuffer keeps the first max bytes written to it and counts the rest: for
// history the head is the useful part -- the commit, its message, its stat.
type headBuffer struct {
	max     int
	buf     strings.Builder
	dropped int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - h.buf.Len(); room > 0 {
		keep := min(room, len(p))
		h.buf.Write(p[:keep])
		h.dropped += len(p) - keep
	} else {
		h.dropped += len(p)
	}
	return len(p), nil
}

func (h *headBuffer) String() string { return h.buf.String() }

// gitHistoryTool is the built-in, offered in the long-task modes.
func (s *Server) gitHistoryTool() mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "git_history",
			Description: "Read the project's git history, read-only: log (recent commits, optionally for one path), " +
				"show (one commit's change), diff (between two commits; never the working tree) or blame (who " +
				"last changed each line, from a commit). For regressions: find when something last worked and " +
				"what changed since.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"command":{"type":"string","enum":["log","show","diff","blame"]},
					"rev":{"type":"string","description":"A commit: hash, branch, tag or HEAD~n. log: where to start; show: the commit; diff: the commit (or the first of two); blame: the commit to blame from."},
					"rev2":{"type":"string","description":"diff only: the second commit."},
					"path":{"type":"string","description":"Limit to this project-relative file or folder (required for blame)."},
					"lines":{"type":"string","description":"blame only: first,last line, e.g. 40,80."},
					"max":{"type":"integer","description":"log only: how many commits (at most 50)."}
				},
				"required":["command"],
				"additionalProperties":false
			}`),
			ReadOnlyHint: true,
			// It starts git, a program this daemon does not control, against
			// config the repository supplies: declared, so plan mode withholds
			// it and the class table files it under the tools that launch.
			LaunchesSubprocess: true,
		},
		Handler: s.builtinGitHistory,
		// EVERY CALL STARTS GIT, and the approval says so: "starts git" is the
		// decision being made. Repeatable -- a fresh, short-lived, hardened git
		// each time, never a server kept running -- so a "yes for this whole
		// task" covers the task's later calls instead of asking at every one.
		Launch: func(json.RawMessage) mcp.LaunchPlan {
			return mcp.LaunchPlan{Needed: true, Program: "git", Key: "git", Repeatable: true}
		},
	}
}
