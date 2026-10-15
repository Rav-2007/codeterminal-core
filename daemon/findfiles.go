package main

import (
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

// FINDING A FILE BY ITS NAME (2026-10-15).
//
// grep finds text inside files and list_directory shows one folder; nothing
// found a file by name. Asked to "fix the auth middleware", a model had to
// list folder after folder, each listing re-sent with every later call, or
// grep for a word and hope the file mentioned it. find_files is the one call
// that answers "where is the file called ...".
//
// IT SEES EXACTLY WHAT grep SEES: the working copy when the turn has one (so a
// file the agent just created is found), the same skipped folders (protected
// ones, dependency folders, .gitignored ones), and the same read gate
// (editapply.ResolveSafeTargetPath) -- so it never names a file read_file
// would refuse. Bounded like grep: entries walked and results returned.
const (
	findFilesMaxResults = 200
	findFilesMaxWalked  = 50_000
	findFilesMaxPattern = 300
)

func (s *Server) findFilesTool(proposals *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "find_files",
			Description: "Find files in the project by name. A plain word matches any file whose name contains it, " +
				"ignoring case (\"auth\" finds AuthService.ts and auth_test.go). A glob matches the path: * within one " +
				"folder, ** across folders, ? one character (\"**/*.test.ts\", \"cmd/*/main.go\"). Lists project-relative " +
				"paths; it searches your working copy, so it sees files you created.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"pattern":{"type":"string","description":"A word in the file name, or a glob."},
					"path":{"type":"string","description":"Search only this project-relative folder."}
				},
				"required":["pattern"],
				"additionalProperties":false
			}`),
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinFindFiles(proposals.readCtx(ctx), raw)
		},
	}
}

func (s *Server) builtinFindFiles(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		return toolError("give a name or a glob to find")
	}
	if len([]rune(pattern)) > findFilesMaxPattern {
		return toolError("the pattern is longer than %d characters", findFilesMaxPattern)
	}
	match, err := fileNameMatcher(pattern)
	if err != nil {
		return toolError("%v", err)
	}

	root := ""
	if st := stageFromCtx(ctx); st != nil {
		root = st.root
	} else if root, err = s.realWorkspaceRoot(); err != nil {
		return toolError("cannot search: %v", err)
	}
	start := root
	if p := strings.TrimSpace(args.Path); p != "" && p != "." {
		if filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
			return toolError("find_files searches inside the project; give a project-relative path")
		}
		full, err := editapply.ResolveSafeTargetPath(root, p)
		if err != nil {
			return toolError("cannot search %s: %v", p, err)
		}
		if info, err := os.Stat(full); err != nil || !info.IsDir() {
			return toolError("cannot search %s: not a folder in the project", p)
		}
		start = full
	}

	ignore := newGitignoreMatcher(root)
	var found []string
	walked, stoppedBy := 0, ""
	errFull := errors.New("full")
	walkErr := filepath.WalkDir(start, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is left out, as grep leaves it out
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr
		}
		walked++
		if walked > findFilesMaxWalked {
			stoppedBy = fmt.Sprintf("it looked at its limit of %d entries", findFilesMaxWalked)
			return errFull
		}
		if d.IsDir() {
			if editapply.IsProtectedDirName(d.Name()) || stageLinkedDirs[d.Name()] || ignore.matchDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || ignore.matchFile(rel) {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if !match(slash) {
			return nil
		}
		if _, err := editapply.ResolveSafeTargetPath(root, rel); err != nil {
			return nil //nolint:nilerr // a file read_file would refuse is not named
		}
		found = append(found, slash)
		if len(found) >= findFilesMaxResults {
			stoppedBy = fmt.Sprintf("it found its limit of %d files", findFilesMaxResults)
			return errFull
		}
		return nil
	})
	if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
		return toolError("the search was stopped")
	}
	if len(found) == 0 {
		return mcp.Result{Content: fmt.Sprintf("No file in the project matches %q.", pattern), Empty: true}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) match %q:\n", len(found), pattern)
	for _, f := range found {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	if stoppedBy != "" {
		fmt.Fprintf(&b, "(Stopped early: %s. Give a narrower pattern or a path.)\n", stoppedBy)
	}
	return mcp.Result{Content: b.String()}, nil
}

// fileNameMatcher turns a pattern into a test on a slash-separated
// project-relative path. A pattern with no glob character matches any file
// whose NAME contains it, ignoring case. A glob without a slash matches the
// name; one with a slash matches the whole path, with ** spanning folders.
func fileNameMatcher(pattern string) (func(string) bool, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		needle := strings.ToLower(pattern)
		return func(rel string) bool {
			return strings.Contains(strings.ToLower(rel[strings.LastIndex(rel, "/")+1:]), needle)
		}, nil
	}
	var expr strings.Builder
	expr.WriteString("(?i)^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "**/" may also match nothing, so **/x.go finds a top-level x.go.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					expr.WriteString("(?:.*/)?")
				} else {
					expr.WriteString(".*")
				}
			} else {
				expr.WriteString("[^/]*")
			}
		case '?':
			expr.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("the glob %q has a [ with no closing ]", pattern)
			}
			expr.WriteString(pattern[i : i+end+1])
			i += end
		default:
			expr.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	expr.WriteString("$")
	re, err := regexp.Compile(expr.String())
	if err != nil {
		return nil, fmt.Errorf("not a usable glob: %v", err)
	}
	if !strings.Contains(pattern, "/") {
		return func(rel string) bool { return re.MatchString(rel[strings.LastIndex(rel, "/")+1:]) }, nil
	}
	return re.MatchString, nil
}
