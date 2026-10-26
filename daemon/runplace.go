package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// ONE VIEW OF THE PROJECT (2026-10-26, the owner's choice).
//
// A turn's edits, its reads and sandbox_exec all work in the turn's private
// working copy (stage.go). run_command used to work on the REAL files, which do
// not have the edits until the user accepts them -- so the agent was working in
// two places that disagreed. In a run of eight real fixes from real GitHub
// projects every serious failure came from there:
//
//   - tests run on the code without the fix, and "all 18 tests pass" said of them;
//   - an edit that "did not work" (the test ran on the old file), so the agent
//     rewrote more.py through the shell -- no diff, no backup, no undo;
//   - "already resolved, nothing to fix", which was wrong;
//   - files left in the project by shell heredocs (CrashTest.java, FuzzTest.java).
//
// A sentence telling the model "run_command cannot see your edits" was already
// in its tool description, and it made 107 run_command calls anyway. So the fix
// is not a sentence: INSIDE THE PROJECT, run_command now runs in the working
// copy. It sees the edits; what it changes there comes back through the same
// review as the agent's own edits, labelled as the command's (ByCommand); a
// file it creates is not offered (netChanges), so a heredoc cannot leave one in
// the project. Absolute paths into the project are pointed at the copy too: the
// model writes `cd /home/u/proj && mvn test` as often as `mvn test`.
//
// TWO KINDS OF COMMAND STILL RUN IN THE REAL PROJECT, because the copy cannot
// stand in for what they act on:
//
//   - git: the copy has no .git (copyFrom leaves protected folders out). A git
//     command that WRITES is refused while the turn has a working copy, before
//     anyone is asked: it would act on a repository underneath the copy, and
//     commit a tree that does not have the edits waiting for review.
//   - package installers: they change the environment, and `pip install -e .`
//     from a copy would point the user's environment at a folder that is about
//     to be removed.
//
// A line that mixes those with anything else is refused, with the way to split
// it. Whatever a command changes in the real project anyway -- through git, an
// installer, or a path the mapping below cannot see -- is found by comparing
// the project before and after, and reported to the model and in the review
// (WorkingCopyInfo.OutsideReview): named, though not undone.

// auditRefusedByTool and denyByTool record a call its own tool refused before
// anyone was asked (mcp.Builtin.Place).
const (
	auditRefusedByTool = "refused_by_tool"
	denyByTool         = "tool"
)

// runCommandArgs are run_command's arguments.
type runCommandArgs struct {
	Command        string `json:"command"`
	Cwd            string `json:"cwd"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// lineKind is what a shell line does, as far as where it may run.
type lineKind int

const (
	lineAnywhere lineKind = iota // builds, tests, scripts, reads: the working copy
	lineGitRead                  // git that reads: the real project
	lineGitWrite                 // git that writes: the real project, never under a working copy
	lineInstall                  // a package installer: the real project
	lineMixed                    // git or an installer with something else: refused
)

// shellCommandWords cuts a shell line into its commands -- at unquoted &&, ||,
// ;, |, &, parentheses and line breaks -- and each command into its words, with
// quotes removed. A here-document's body is skipped. It is for telling which
// programs a line starts, nothing more: the user's shell runs the line itself.
func shellCommandWords(line string) [][]string {
	var (
		cmds     [][]string
		words    []string
		cur      strings.Builder
		inWord   bool
		quote    rune
		heredocs []string // delimiters whose bodies start after this line
	)
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if quote != 0 {
			switch {
			case r == quote:
				quote = 0
			case quote == '"' && r == '\\' && i+1 < len(rs):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, inWord = r, true
		case '\\':
			if i+1 < len(rs) && rs[i+1] != '\n' {
				i++
				cur.WriteRune(rs[i])
				inWord = true
			}
		case ' ', '\t':
			endWord()
		case '\n':
			endCommand()
			for _, delim := range heredocs {
				i = skipHeredoc(rs, i+1, delim)
			}
			heredocs = nil
		case '&':
			// Part of a redirection (2>&1, &>log), not a command run in the background.
			if (inWord && strings.HasSuffix(cur.String(), ">")) || (i+1 < len(rs) && rs[i+1] == '>') {
				cur.WriteRune(r)
				inWord = true
				continue
			}
			endCommand()
		case ';', '|', '(', ')':
			endCommand()
		case '<':
			if i+1 < len(rs) && rs[i+1] == '<' && (i+2 >= len(rs) || rs[i+2] != '<') {
				endWord()
				delim, next := heredocDelimiter(rs, i+2)
				if delim != "" {
					heredocs = append(heredocs, delim)
				}
				i = next - 1
				continue
			}
			cur.WriteRune(r)
			inWord = true
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endCommand()
	return cmds
}

// heredocDelimiter reads the word after "<<" (an optional "-", spaces, quotes
// removed) and returns it with the index just past it.
func heredocDelimiter(rs []rune, i int) (string, int) {
	if i < len(rs) && rs[i] == '-' {
		i++
	}
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	var b strings.Builder
	for ; i < len(rs); i++ {
		r := rs[i]
		if r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' || r == '<' || r == '>' {
			break
		}
		if r != '\'' && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return b.String(), i
}

// skipHeredoc returns the index of the line break that ends a here-document
// whose body starts at i, or the end of the line when it never ends.
func skipHeredoc(rs []rune, i int, delim string) int {
	for i < len(rs) {
		end := i
		for end < len(rs) && rs[end] != '\n' {
			end++
		}
		if strings.TrimLeft(string(rs[i:end]), "\t") == delim {
			return end
		}
		i = end + 1
	}
	return len(rs)
}

// commandWrappers run the command after their own options: `time make`,
// `env CC=gcc make`, `timeout 60 pytest`.
var commandWrappers = map[string]bool{"env": true, "time": true, "nice": true, "nohup": true, "command": true,
	"exec": true, "builtin": true, "timeout": true, "stdbuf": true}

// installerPrograms install whatever their subcommand is.
var installerPrograms = map[string]bool{"sudo": true, "doas": true, "brew": true, "apt": true, "apt-get": true,
	"dnf": true, "yum": true, "pacman": true, "zypper": true, "apk": true, "snap": true, "port": true, "choco": true,
	"winget": true, "scoop": true, "pipx": true, "rustup": true, "nvm": true, "pyenv": true}

// installerSubcommands are the subcommands that install, per program; "" is a
// bare program that installs on its own (`yarn`, `bundle`).
var installerSubcommands = map[string]map[string]bool{
	"pip": {"install": true, "uninstall": true, "download": true, "wheel": true},
	"npm": {"install": true, "i": true, "ci": true, "add": true, "uninstall": true, "remove": true, "rm": true,
		"un": true, "update": true, "up": true, "upgrade": true, "link": true, "prune": true, "dedupe": true},
	"yarn":     {"": true, "install": true, "add": true, "remove": true, "upgrade": true, "up": true, "link": true},
	"pnpm":     {"install": true, "i": true, "add": true, "remove": true, "rm": true, "update": true, "up": true, "link": true},
	"bun":      {"install": true, "i": true, "add": true, "remove": true, "rm": true, "update": true, "link": true},
	"cargo":    {"install": true, "uninstall": true, "add": true, "remove": true, "rm": true},
	"go":       {"install": true, "get": true},
	"uv":       {"add": true, "remove": true, "sync": true, "lock": true, "pip": true, "tool": true, "venv": true},
	"poetry":   {"add": true, "remove": true, "install": true, "update": true, "lock": true},
	"pipenv":   {"install": true, "uninstall": true, "update": true, "lock": true, "sync": true},
	"conda":    {"install": true, "remove": true, "uninstall": true, "create": true, "update": true, "env": true},
	"mamba":    {"install": true, "remove": true, "uninstall": true, "create": true, "update": true, "env": true},
	"gem":      {"install": true, "uninstall": true, "update": true},
	"bundle":   {"": true, "install": true, "update": true, "add": true},
	"composer": {"install": true, "require": true, "update": true, "remove": true},
}

// gitReadSubcommands read the repository and change nothing in it.
var gitReadSubcommands = map[string]bool{"status": true, "diff": true, "log": true, "show": true, "blame": true,
	"annotate": true, "grep": true, "ls-files": true, "ls-tree": true, "ls-remote": true, "rev-parse": true,
	"rev-list": true, "describe": true, "shortlog": true, "cat-file": true, "whatchanged": true, "version": true,
	"help": true, "show-ref": true, "name-rev": true, "merge-base": true, "count-objects": true,
	"check-ignore": true, "for-each-ref": true, "diff-tree": true, "diff-files": true, "diff-index": true,
	"cherry": true, "range-diff": true, "reflog": true}

// gitListingOnly are subcommands that read when given only these flags (or
// none) and write otherwise: `git branch` lists, `git branch x` makes one.
var gitListingOnly = map[string]map[string]bool{
	"branch": {"--list": true, "-l": true, "-a": true, "--all": true, "-r": true, "--remotes": true, "-v": true,
		"-vv": true, "--show-current": true, "--contains": true, "--merged": true, "--no-merged": true},
	"tag":      {"--list": true, "-l": true, "-n": true},
	"remote":   {"-v": true, "--verbose": true, "show": true, "get-url": true},
	"config":   {"--get": true, "--get-all": true, "--list": true, "-l": true, "--get-regexp": true},
	"stash":    {"list": true, "show": true},
	"worktree": {"list": true},
}

// commandCore is one command of a line as its program and arguments: no
// redirections, no NAME=VALUE in front, no wrapper (`env`, `time`, `timeout 60`).
func commandCore(words []string) []string {
	words = withoutRedirections(words)
	for len(words) > 0 && looksLikeEnvAssignment(words[0]) {
		words = words[1:]
	}
	for len(words) > 0 && commandWrappers[filepath.Base(words[0])] {
		words = words[1:]
		for len(words) > 0 && (strings.HasPrefix(words[0], "-") || looksLikeEnvAssignment(words[0]) ||
			strings.Trim(words[0], "0123456789.smhd") == "") {
			words = words[1:]
		}
	}
	return words
}

// classifyCommand says what one command of a line does.
func classifyCommand(words []string) lineKind {
	words = commandCore(words)
	if len(words) == 0 || words[0] == "cd" {
		return lineAnywhere
	}
	program := strings.TrimSuffix(filepath.Base(words[0]), ".exe")
	args := words[1:]
	// `python -m pip install x` is pip.
	if (strings.HasPrefix(program, "python") || program == "py") && len(args) >= 2 && args[0] == "-m" {
		program, args = args[1], args[2:]
	}
	if program == "pip3" || strings.HasPrefix(program, "pip3.") {
		program = "pip"
	}
	switch {
	case program == "git":
		return classifyGit(args)
	case installerPrograms[program]:
		return lineInstall
	}
	if subs := installerSubcommands[program]; subs != nil {
		sub := ""
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				sub = a
				break
			}
		}
		if subs[sub] {
			return lineInstall
		}
	}
	return lineAnywhere
}

// classifyGit says whether a git command reads or writes, from its arguments.
func classifyGit(args []string) lineKind {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		takesValue := args[0] == "-C" || args[0] == "-c"
		args = args[1:]
		if takesValue && len(args) > 0 {
			args = args[1:]
		}
	}
	if len(args) == 0 || gitReadSubcommands[args[0]] {
		return lineGitRead
	}
	if listing, ok := gitListingOnly[args[0]]; ok {
		if args[0] == "stash" && len(args) == 1 {
			return lineGitWrite // a bare `git stash` is `git stash push`
		}
		for _, a := range args[1:] {
			if !listing[a] {
				return lineGitWrite
			}
		}
		return lineGitRead
	}
	return lineGitWrite
}

// withoutRedirections drops a command's redirections: `>out`, `2>/dev/null`,
// and the file after a bare `>`.
func withoutRedirections(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := strings.TrimLeft(words[i], "0123456789&")
		if strings.HasPrefix(w, ">") || strings.HasPrefix(w, "<") {
			if strings.Trim(w, "<>&|") == "" {
				i++ // the file it names
			}
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// classifyLine says what a whole shell line does: every command in it the same
// way, or mixed.
func classifyLine(line string) lineKind {
	kind, seen := lineAnywhere, false
	for _, words := range shellCommandWords(line) {
		k := classifyCommand(words)
		if len(withoutRedirections(words)) > 0 && words[0] == "cd" {
			continue // where the rest of the line runs, not a command of its own
		}
		switch {
		case !seen:
			kind, seen = k, true
		case k == kind:
		case (k == lineGitRead && kind == lineGitWrite) || (k == lineGitWrite && kind == lineGitRead):
			kind = lineGitWrite
		default:
			return lineMixed
		}
	}
	return kind
}

// mixedLineRefusal is why a line mixing the real project's commands with the
// working copy's is not run.
const mixedLineRefusal = "Not run, and the user was not asked: this line mixes commands that run in the user's " +
	"real project (git, or a package installer) with commands that run in this turn's working copy, which has " +
	"your edits. Run them as separate run_command calls."

// gitWriteRefusal is why a git command that writes is not run while the turn
// has a working copy.
func gitWriteRefusal(p *proposalSink) string {
	return "Not run, and the user was not asked: this turn is working in a private copy of the project, and a git " +
		"command that writes would act on the user's real repository underneath it -- which does not have " + editsSaid(p) +
		". Tell the user the git command to run after they review your changes."
}

// editsSaid is "your 2 edits (a.go, b.go)" for a message, or "your edits".
func editsSaid(p *proposalSink) string {
	files := p.editedFiles()
	if len(files) == 0 {
		return "your edits"
	}
	return fmt.Sprintf("your %s (%s)", plural(len(files), "edited file"), strings.Join(firstN(files, 5), ", "))
}

// editedFiles are the files this turn has changed in its working copy, in the
// order it first changed them.
func (p *proposalSink) editedFiles() []string {
	if p == nil || p.stage == nil {
		return nil
	}
	files := make([]string, 0, len(p.stage.order))
	for _, rel := range p.stage.order {
		files = append(files, filepath.ToSlash(rel))
	}
	return files
}

func firstN(items []string, n int) []string {
	if len(items) > n {
		return append(append([]string(nil), items[:n]...), fmt.Sprintf("%d more", len(items)-n))
	}
	return items
}

// runCommandPlacement is run_command's Place probe: whether this call would run
// in the user's real project, and why it must not run at all. A command that
// may run in the working copy makes the copy here if the turn has none yet --
// the only honest way to know one can be made, as sandbox_exec's InPlace does.
func (s *Server) runCommandPlacement(raw json.RawMessage, proposals *proposalSink) (inPlace bool, refusal string) {
	var args runCommandArgs
	_ = json.Unmarshal(raw, &args) // the handler reports a bad one
	if proposals == nil || proposals.stageFrom == "" {
		return true, "" // a turn with no working copy runs every command where it always ran
	}
	switch classifyLine(args.Command) {
	case lineGitWrite:
		if proposals.stage != nil {
			return true, gitWriteRefusal(proposals)
		}
		return true, ""
	case lineGitRead, lineInstall:
		return true, ""
	case lineMixed:
		return true, mixedLineRefusal
	}
	st, _ := proposals.workingCopy()
	return st == nil, ""
}

// builtinRunCommandIn is run_command's handler: in the turn's working copy when the
// command may run there, in the real project otherwise -- decided by the same
// probe the approval prompt was built from, so what the user approved is where
// it runs.
func (s *Server) builtinRunCommandIn(ctx context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	inPlace, refusal := s.runCommandPlacement(raw, proposals)
	if refusal != "" {
		return toolError("%s", refusal)
	}
	var st *stagedWorkspace
	if proposals != nil {
		st = proposals.stage
	}
	if inPlace || st == nil {
		return s.runInProject(ctx, raw, proposals)
	}
	return s.runInCopy(ctx, raw, st, proposals)
}

// runInProject runs the command in the real project, as run_command always
// did, and says what the model needs to know about that: its edits are not
// there, and what the command changed there was not reviewed.
func (s *Server) runInProject(ctx context.Context, raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args runCommandArgs
	_ = json.Unmarshal(raw, &args)
	root, _ := s.realWorkspaceRoot()
	before, complete := projectStates(root)
	res, err := s.builtinRunCommand(ctx, raw)
	if err != nil || res.IsError {
		return res, err
	}
	if complete {
		res.Content += proposals.noteOutsideReview(statesChanged(before, projectStatesOnly(root)), args.Command)
	}
	if files := proposals.editedFiles(); len(files) > 0 {
		res.Content += "\n[Mochiii: this ran in the user's real project, which does not have " + editsSaid(proposals) +
			" yet: they reach it when the user accepts them.]"
	}
	return res, nil
}

// runInCopy runs the command in the turn's working copy: from the same folder
// of it, with paths into the project pointed at the copy, and Python told to
// import the copy's code. What it changed there is the command's in the review;
// what it changed in the real project anyway is named.
func (s *Server) runInCopy(ctx context.Context, raw json.RawMessage, st *stagedWorkspace, proposals *proposalSink) (mcp.Result, error) {
	var args runCommandArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return toolError("no command was given")
	}
	dir, err := st.commandDir(args.Cwd)
	if err != nil {
		return toolError("not run: %v", err)
	}
	line := st.toCopy(args.Command, s.workspace)
	before := st.fileStates()
	realBefore, complete := projectStates(st.real)
	s.logger.Printf("agent: run_command in the working copy (%s): %s", st.toReal(dir), truncateForClient(args.Command, 200))
	// As sandbox_exec runs in the copy: no bytecode cache (see mcp_exec.go), and Python imports the copy's code.
	res, outcome, err := s.runShellLine(ctx, line, dir, append(stagedPythonPath(st.root), "PYTHONDONTWRITEBYTECODE=1"), args.TimeoutSeconds)
	if err != nil {
		return res, err
	}
	changed := statesChanged(before, st.fileStates())
	label := truncateForClient(strings.Join(strings.Fields(args.Command), " "), 80)
	st.noteCommandChanges(before, label)
	res.Content = st.toReal(res.Content)
	// A run in the working copy is a check of it, as a sandbox_exec run is: it
	// is what the check line above the review and Mochiii's record report.
	proposals.recordCheck(args.Command, raw, res, outcome)
	res.Content += "\n[Mochiii: this ran in this turn's working copy of the project, which has your edits.]"
	if tests := st.testsChangedByCommand(changed); len(tests) > 0 {
		res.Content += testChangeWarning(tests...)
	}
	if created := st.createdByCommand(changed); len(created) > 0 {
		res.Content += "\n[Mochiii: files a command creates in the working copy are not offered for review (" +
			strings.Join(firstN(created, 5), ", ") + "). If your change needs a new file, create it with propose_edit.]"
	}
	if complete {
		res.Content += proposals.noteOutsideReview(statesChanged(realBefore, projectStatesOnly(st.real)), args.Command)
	}
	return res, nil
}

// createdByCommand are the files among changed that a command made in the
// copy -- not the project's, not an edit's -- leaving out what .gitignore
// covers (build output, caches): what netChanges will not offer.
func (st *stagedWorkspace) createdByCommand(changed []string) []string {
	ignore := newGitignoreMatcher(st.root)
	var created []string
	for _, rel := range changed {
		_, known := st.manifest[rel]
		if _, err := os.Lstat(filepath.Join(st.root, rel)); err != nil || known || st.touched[rel] || ignoredPath(ignore, rel) {
			continue
		}
		created = append(created, filepath.ToSlash(rel))
	}
	return created
}

// ignoredPath reports whether .gitignore covers rel or a folder above it.
func ignoredPath(ignore *gitignoreMatcher, rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 1; i < len(parts); i++ {
		if ignore.matchDir(filepath.FromSlash(strings.Join(parts[:i], "/"))) {
			return true
		}
	}
	return ignore.matchFile(rel)
}

// noteOutsideReview records files a command changed in the REAL project, and
// returns what the model is told about them ("" for none).
func (p *proposalSink) noteOutsideReview(changed []string, command string) string {
	if p == nil || len(changed) == 0 {
		return ""
	}
	label := truncateForClient(strings.Join(strings.Fields(command), " "), 80)
	seen := map[string]bool{}
	for _, entry := range p.outsideReview {
		seen[strings.SplitN(entry, " (", 2)[0]] = true
	}
	shown := make([]string, 0, len(changed))
	for _, rel := range changed {
		slash := filepath.ToSlash(rel)
		shown = append(shown, slash)
		if !seen[slash] && len(p.outsideReview) < stageMaxNotes {
			p.outsideReview = append(p.outsideReview, slash+" ("+label+")")
		}
	}
	return "\n[Mochiii: while this ran, these changed in the user's REAL project, outside review: " +
		strings.Join(firstN(shown, 8), ", ") + ". The user is told.]"
}

// commandDir is where in the copy a command with this cwd starts: the copy's
// root, or the same folder of it the cwd names in the project.
func (st *stagedWorkspace) commandDir(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || cwd == "." {
		return st.root, nil
	}
	rel, ok := st.relFor(cwd)
	rel = filepath.Clean(rel)
	if !ok || filepath.IsAbs(rel) || leavesRoot(rel) {
		return "", fmt.Errorf("%s is not a folder in the project", cwd)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if editapply.IsProtectedDirName(part) {
			return "", fmt.Errorf("%s is a protected folder", cwd)
		}
	}
	path := filepath.Join(st.root, rel)
	resolved, err := filepath.EvalSymlinks(path)
	if info, statErr := os.Stat(path); err != nil || statErr != nil || !info.IsDir() || !isWithin(st.root, resolved) {
		return "", fmt.Errorf("%s is not a folder in this turn's working copy of the project (the copy leaves out "+
			".gitignored folders; a build can make one there)", cwd)
	}
	return path, nil
}

// toCopy points the project's own paths in a command line at the working copy:
// the project's real location, as the model has seen it in output (toReal) and
// spelt any other way the daemon knows it, and its ~/ form. A path is replaced
// only where it is the whole of a path or its start -- never inside a longer
// name, so /home/u/proj does not touch /home/u/proj2.
func (st *stagedWorkspace) toCopy(line string, aliases ...string) string {
	roots := []string{st.real}
	for _, a := range aliases {
		if a = filepath.Clean(a); a != "." && a != st.real {
			roots = append(roots, a)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := strings.CutPrefix(st.real, home+string(filepath.Separator)); ok {
			roots = append(roots, "~"+string(filepath.Separator)+rel)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, root := range roots {
		line = replacePathPrefix(line, root, st.root)
	}
	return line
}

// replacePathPrefix replaces old in s wherever it stands as a whole path or the
// start of one.
func replacePathPrefix(s, old, replacement string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(old)
		startsPath := i == 0 || !continuesName(s[i-1])
		endsPath := end == len(s) || !continuesName(s[end]) || s[end] == '/' || s[end] == '\\'
		b.WriteString(s[:i])
		if startsPath && endsPath {
			b.WriteString(replacement)
		} else {
			b.WriteString(old)
		}
		s = s[end:]
	}
}

// continuesName reports a byte that would make a path name longer.
func continuesName(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c == '/' || c == '\\' || c == '~' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// stagedPythonPath is the PYTHONPATH entry for a command run in a working copy
// at root, so Python imports the copy's code. An editable install of the real
// project (`pip install -e .`) otherwise wins for a src/ layout, and the tests
// the agent runs would import the code without its edits: the two-copies
// failure again, one level down. Nil for a root that is not a working copy.
func stagedPythonPath(root string) []string {
	parent, err := stageParentDir()
	if err != nil || !isWithin(parent, root) {
		return nil
	}
	paths := []string{root}
	if info, err := os.Stat(filepath.Join(root, "src")); err == nil && info.IsDir() {
		paths = append([]string{filepath.Join(root, "src")}, paths...)
	}
	if prior := os.Getenv("PYTHONPATH"); prior != "" {
		paths = append(paths, prior)
	}
	return []string{"PYTHONPATH=" + strings.Join(paths, string(os.PathListSeparator))}
}

// projectStates is the state of the real project's files a command could
// change without anyone reviewing it: everything netChanges' walk would see,
// minus .gitignored files and the dependency folders an installer is meant to
// change. complete is false past stageMaxFiles, where nothing is compared.
func projectStates(root string) (states map[string]stagedState, complete bool) {
	states, complete = map[string]stagedState{}, true
	if root == "" {
		return states, false
	}
	ignore := newGitignoreMatcher(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return nil //nolint:nilerr // an unreadable entry is left out
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if editapply.IsProtectedDirName(d.Name()) || stageLinkedDirs[d.Name()] || ignore.matchDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || ignore.matchFile(rel) {
			return nil
		}
		if len(states) >= stageMaxFiles {
			complete = false
			return fs.SkipAll
		}
		if info, err := d.Info(); err == nil {
			states[rel] = stagedState{size: info.Size(), mod: info.ModTime(), mode: info.Mode()}
		}
		return nil
	})
	return states, complete
}

// projectStatesOnly is projectStates without its completeness.
func projectStatesOnly(root string) map[string]stagedState {
	states, _ := projectStates(root)
	return states
}

// statesChanged lists, sorted, the files created, changed or removed between
// two states of the same tree.
func statesChanged(before, after map[string]stagedState) []string {
	var changed []string
	for rel, now := range after {
		if was, ok := before[rel]; !ok || was != now {
			changed = append(changed, rel)
		}
	}
	for rel := range before {
		if _, ok := after[rel]; !ok {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed
}
