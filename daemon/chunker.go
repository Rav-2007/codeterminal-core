package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"codeterminal/editapply"
)

// Chunking parameters. Kept as simple line-based windows — no language-aware
// parsing yet.
const (
	chunkLines   = 40
	overlapLines = 10
)

// maxFileSize is the size cap above which a file is skipped outright.
const maxFileSize = 1 << 20 // 1MB

// binarySniffSize is how many leading bytes of a file are inspected to guess
// whether it's binary.
const binarySniffSize = 8192

// SkipReason names why a path was excluded from indexing.
type SkipReason string

const (
	SkipSecret     SkipReason = "secret"
	SkipBinary     SkipReason = "binary"
	SkipTooLarge   SkipReason = "too_large"
	SkipSymlink    SkipReason = "symlink"
	SkipIgnoredDir SkipReason = "ignored_dir"
	SkipGitignore  SkipReason = "gitignored"
	SkipNoise      SkipReason = "noise"
)

// ignoredDirNames are pruned outright during the walk: a matching directory
// is never descended into, so nothing beneath it is ever scanned, read, or
// counted individually — it's one skip per pruned subtree, not one per file.
//
// It is the union of two sets with different reasons for being here. The
// dangerous ones (VCS internals, .codeterminal, credential dirs) come from
// editapply.ProtectedDirNames, which is also what the edit WRITER refuses to
// write into — that shared source of truth is the point. The two lists were
// maintained separately, and drifted: the indexer pruned .git and .codeterminal
// while the writer happily wrote into both, so model output could reach hooks
// it executes and the backups undo restores from (Fix 3). Anything added there
// is now pruned here for free, and vice versa cannot silently diverge.
//
// The rest are local: build output and dependency trees, skipped as noise.
// They are deliberately NOT protected on the write path — editing vendored or
// generated code is unusual but legitimate.
var ignoredDirNames = buildIgnoredDirNames()

func buildIgnoredDirNames() map[string]bool {
	names := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		"target":       true,
		"out":          true,
		".next":        true,
		"__pycache__":  true,
		".venv":        true,
		"venv":         true,
	}
	for name := range editapply.ProtectedDirNames {
		names[name] = true
	}
	return names
}

// ScanResult is the outcome of walking a workspace: every chunk produced
// (without vectors yet) plus counters for what was scanned and skipped.
type ScanResult struct {
	Chunks       []Chunk
	FilesScanned int
	Skipped      map[SkipReason]int
}

func newScanResult() *ScanResult {
	return &ScanResult{Skipped: make(map[SkipReason]int)}
}

// ScanWorkspace walks root and returns chunked content for every eligible
// file.
//
// Confinement: root is canonicalized once via filepath.EvalSymlinks, and any
// symlinked file or directory encountered during the walk is skipped rather
// than followed — filepath.WalkDir reports a symlink's own type (it uses
// Lstat), so this never silently descends into or reads through one,
// regardless of whether its target is an absolute path or a relative
// "../escape". Nothing outside the resolved root is ever read.
func ScanWorkspace(root string) (*ScanResult, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root %s: %w", root, err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root %s: %w", root, err)
	}

	ignore := loadGitignore(realRoot)
	result := newScanResult()

	walkErr := filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == realRoot {
			return nil
		}

		relPath, relErr := filepath.Rel(realRoot, path)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			// Shouldn't happen from a WalkDir-driven path (WalkDir only ever
			// visits real descendants of realRoot), but never index anything
			// that isn't actually inside root.
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Never follow symlinks. A symlinked entry is reported by its own
		// type (ModeSymlink), not the type of whatever it points to, so this
		// check alone stops any symlink escape without needing to inspect
		// the target.
		if d.Type()&fs.ModeSymlink != 0 {
			result.Skipped[SkipSymlink]++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if ignoredDirNames[d.Name()] {
				result.Skipped[SkipIgnoredDir]++
				return fs.SkipDir
			}
			if ignore.matchDir(relPath) {
				result.Skipped[SkipGitignore]++
				return fs.SkipDir
			}
			return nil
		}

		content, reason, skip, err := readEligibleFile(path, relPath, ignore)
		if err != nil {
			return fmt.Errorf("reading %s: %w", relPath, err)
		}
		if skip {
			result.Skipped[reason]++
			return nil
		}

		result.FilesScanned++
		result.Chunks = append(result.Chunks, chunkContent(content, relPath)...)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	return result, nil
}

// shouldSkipFile is the single gate deciding whether a file's content may be
// read and chunked: secret-name patterns, the workspace's .gitignore, the
// size cap, and the binary sniff all funnel through here. It is called from
// exactly one place — readEligibleFile, immediately before the read it
// gates — so the walk-time decision and the actual content read can never
// diverge; there is no separate walk-time-only prune for files.
func shouldSkipFile(path, relPath string, ignore *gitignoreRules) (SkipReason, bool, error) {
	base := filepath.Base(relPath)

	if editapply.MatchesSecretName(base) {
		return SkipSecret, true, nil
	}
	if isNoiseFile(relPath) {
		return SkipNoise, true, nil
	}
	if ignore.matchFile(relPath) {
		return SkipGitignore, true, nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if info.Size() > maxFileSize {
		return SkipTooLarge, true, nil
	}

	isBinary, err := sniffBinary(path)
	if err != nil {
		return "", false, err
	}
	if isBinary {
		return SkipBinary, true, nil
	}

	return "", false, nil
}

// readEligibleFile is the only function in this file that reads a file's
// content for indexing. It re-runs shouldSkipFile immediately before the
// read, so nothing can reach chunkContent without passing that same gate.
func readEligibleFile(path, relPath string, ignore *gitignoreRules) ([]byte, SkipReason, bool, error) {
	reason, skip, err := shouldSkipFile(path, relPath, ignore)
	if err != nil {
		return nil, "", false, err
	}
	if skip {
		return nil, reason, true, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false, err
	}
	return content, "", false, nil
}

func sniffBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, binarySniffSize)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) != -1, nil
}

// chunkContent splits content into overlapping line-based windows of
// chunkLines with overlapLines of overlap between consecutive windows.
func chunkContent(content []byte, relPath string) []Chunk {
	lines := splitLines(content)
	if len(lines) == 0 {
		return nil
	}

	class := classifyFile(relPath)

	stride := chunkLines - overlapLines
	var chunks []Chunk
	for start := 0; start < len(lines); start += stride {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}

		startLine := start + 1 // 1-indexed for humans and logs
		endLine := end
		chunks = append(chunks, Chunk{
			ID:        fmt.Sprintf("%s:%d-%d", relPath, startLine, endLine),
			FilePath:  relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Content:   strings.Join(lines[start:end], "\n"),
			Class:     class,
		})

		if end == len(lines) {
			break
		}
	}
	return chunks
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(string(content), "\n")
}

// gitignoreRule is one parsed line from a .gitignore file.
type gitignoreRule struct {
	pattern string
	dirOnly bool
}

// gitignoreRules implements a deliberately small subset of .gitignore
// semantics: exact names and simple shell globs (filepath.Match) matched
// against a file's basename or root-relative path, plus trailing-slash
// directory-only rules and plain directory-prefix matches. It does not
// support negation (!), nested .gitignore files, or full gitignore glob
// syntax (**, character classes, etc.). It reads only the workspace root's
// own .gitignore.
type gitignoreRules struct {
	rules []gitignoreRule
}

func loadGitignore(root string) *gitignoreRules {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return &gitignoreRules{}
	}

	var rules []gitignoreRule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dirOnly := strings.HasSuffix(line, "/")
		pattern := strings.TrimSuffix(line, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		if pattern == "" {
			continue
		}
		rules = append(rules, gitignoreRule{pattern: pattern, dirOnly: dirOnly})
	}
	return &gitignoreRules{rules: rules}
}

func (g *gitignoreRules) matches(relPath string, isDir bool) bool {
	if g == nil {
		return false
	}
	base := filepath.Base(relPath)
	for _, r := range g.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if ok, _ := filepath.Match(r.pattern, base); ok {
			return true
		}
		if ok, _ := filepath.Match(r.pattern, relPath); ok {
			return true
		}
		if strings.HasPrefix(relPath, r.pattern+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (g *gitignoreRules) matchDir(relPath string) bool  { return g.matches(relPath, true) }
func (g *gitignoreRules) matchFile(relPath string) bool { return g.matches(relPath, false) }
