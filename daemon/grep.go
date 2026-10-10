package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// grep: EXACT search, beside search_code's search by meaning. A refactor needs
// every use of a name and a debugging session needs where an error message
// comes from; similarity finds code that looks related, and only an exact
// search finds all of it.
//
// IT READS ONLY WHAT read_file COULD READ. Every file goes through the same
// gate read_file uses (editapply.ResolveSafeTargetPath: protected folders,
// secret-shaped names, symlink escapes), the same .gitignore rules the working
// copy and the index use, and in a working copy it searches the copy -- so a
// search can never see a file a read would have been refused, and it sees the
// agent's own edits.
//
// BOUNDED, because a search is a scan: files, bytes, matches and line length
// are capped, and stopping early is said. Go's regexp is RE2, which matches in
// time linear in its input -- no pattern can make a search hang.
const (
	grepMaxMatches     = 200
	grepMaxLineRunes   = 300
	grepMaxFileBytes   = 2 << 20
	grepMaxFiles       = 20_000
	grepMaxTotalBytes  = 64 << 20
	grepMaxPatternRune = 500
)

func (s *Server) builtinGrep(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Regex      bool   `json:"regex"`
		IgnoreCase bool   `json:"ignore_case"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if args.Pattern == "" {
		return toolError("give a pattern to search for")
	}
	if len([]rune(args.Pattern)) > grepMaxPatternRune {
		return toolError("the pattern is longer than %d characters", grepMaxPatternRune)
	}
	expr := args.Pattern
	if !args.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if args.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return toolError("not a valid regular expression (Go RE2 syntax): %v", err)
	}

	// The working copy when the turn has one, so the search sees its edits.
	root := ""
	if st := stageFromCtx(ctx); st != nil {
		root = st.root
	} else if root, err = s.realWorkspaceRoot(); err != nil {
		return toolError("cannot search: %v", err)
	}
	start := root
	if p := strings.TrimSpace(args.Path); p != "" && p != "." {
		// Inside the project only: a search is a read of many files, and an
		// outside read is approved one exact path at a time (outsideread.go).
		if filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
			return toolError("grep searches inside the project; give a project-relative path")
		}
		full, err := editapply.ResolveSafeTargetPath(root, p)
		if err != nil {
			return toolError("cannot search %s: %v", p, err)
		}
		if _, err := os.Stat(full); err != nil {
			return toolError("cannot search %s: no such file or directory", p)
		}
		start = full
	}

	g := grepScan{re: re, root: root, ignore: newGitignoreMatcher(root)}
	walkErr := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is left out, as the index leaves it out
		}
		return g.visit(path, d)
	})
	if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
		return toolError("the search was stopped")
	}
	return mcp.Result{Content: g.report(args.Pattern), Empty: g.matches == 0}, nil
}

// errGrepFull ends a walk that has found or read as much as it may.
var errGrepFull = errors.New("grep: limit reached")

type grepScan struct {
	re     *regexp.Regexp
	root   string
	ignore *gitignoreMatcher

	out                       strings.Builder
	matches, files, withMatch int
	read                      int64
	stoppedBy                 string
}

func (g *grepScan) visit(path string, d fs.DirEntry) error {
	if g.stoppedBy != "" {
		return errGrepFull
	}
	rel, err := filepath.Rel(g.root, path)
	if err != nil || rel == "." {
		return nil //nolint:nilerr
	}
	switch {
	case d.IsDir():
		// Dependency folders are the project's inputs, not its code -- the
		// working copy links them for the same reason (stageLinkedDirs).
		if editapply.IsProtectedDirName(d.Name()) || stageLinkedDirs[d.Name()] || g.ignore.matchDir(rel) {
			return fs.SkipDir
		}
		return nil
	case !d.Type().IsRegular(), g.ignore.matchFile(rel):
		return nil
	}
	// The gate read_file uses, so nothing is searched that could not be read.
	full, err := editapply.ResolveSafeTargetPath(g.root, rel)
	if err != nil {
		return nil //nolint:nilerr // a refused file is simply not searched
	}
	g.files++
	if g.files > grepMaxFiles {
		g.stoppedBy = fmt.Sprintf("it searched its limit of %d files", grepMaxFiles)
		return errGrepFull
	}
	data, size, err := readBoundedFile(full, grepMaxFileBytes)
	if err != nil || size > grepMaxFileBytes || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil //nolint:nilerr // unreadable, too large or binary: not searched
	}
	g.read += int64(len(data))
	if g.read > grepMaxTotalBytes {
		g.stoppedBy = fmt.Sprintf("it read its limit of %d MB", grepMaxTotalBytes>>20)
		return errGrepFull
	}
	found := false
	for i, line := range strings.Split(string(data), "\n") {
		if !g.re.MatchString(line) {
			continue
		}
		found = true
		g.matches++
		fmt.Fprintf(&g.out, "%s:%d: %s\n", filepath.ToSlash(rel), i+1,
			truncateRunes(strings.TrimRight(line, "\r"), grepMaxLineRunes))
		if g.matches >= grepMaxMatches {
			g.withMatch++
			g.stoppedBy = fmt.Sprintf("it reached its limit of %d matches", grepMaxMatches)
			return errGrepFull
		}
	}
	if found {
		g.withMatch++
	}
	return nil
}

func (g *grepScan) report(pattern string) string {
	if g.matches == 0 {
		msg := fmt.Sprintf("No line matches %q in the %d file(s) searched.", pattern, g.files)
		if g.stoppedBy != "" {
			msg += " The search stopped early: " + g.stoppedBy + "; narrow it with path."
		}
		return msg
	}
	head := fmt.Sprintf("%d matching line(s) in %d file(s):\n", g.matches, g.withMatch)
	tail := ""
	if g.stoppedBy != "" {
		tail = "\n[the search stopped early: " + g.stoppedBy + "; there may be more -- narrow it with path or a more specific pattern]"
	}
	return head + g.out.String() + tail
}

// searchByMeaning reports whether search_code can answer in this daemon: there
// is an index and an embedder to query it with. False from start-up to exit
// when there is not, with the reason in retrievalDisabledReason.
func (s *Server) searchByMeaning() bool { return s.retrievalDisabledReason == "" }

// grepTool is the built-in: exact search, in every turn but a build's
// (builtinTools says why).
func (s *Server) grepTool(proposals *proposalSink) mcp.Builtin {
	// The description names search_code only where search_code is offered: a
	// model told about a tool it was not given calls it, and pays a model call
	// to be told there is no such tool.
	description := "Search the project's files for exact text, or a regular expression (Go RE2 syntax), and " +
		"list every matching line as path:line: text. Use it for every use of a name, where an error " +
		"message comes from, or a string to change everywhere: search_code finds code by meaning, grep " +
		"finds all of it exactly. It searches your working copy, so it sees your own edits."
	if !s.searchByMeaning() {
		description = "Search the project's files for exact text, or a regular expression (Go RE2 syntax), and " +
			"list every matching line as path:line: text. Use it to find where a name is defined or used, " +
			"where an error message comes from, or a string to change everywhere. It searches your working " +
			"copy, so it sees your own edits."
	}
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name:        "grep",
			Description: description,
			Schema: schema(`{
				"type":"object",
				"properties":{
					"pattern":{"type":"string","description":"The text to find, or a regular expression when regex is true."},
					"regex":{"type":"boolean","description":"Treat pattern as a regular expression. Default false: exact text."},
					"ignore_case":{"type":"boolean","description":"Match regardless of case."},
					"path":{"type":"string","description":"Search only this project-relative folder or file."}
				},
				"required":["pattern"],
				"additionalProperties":false
			}`),
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinGrep(proposals.readCtx(ctx), raw)
		},
	}
}
