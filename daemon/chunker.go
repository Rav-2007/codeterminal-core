package main

import (
	"bytes"
	"errors"
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

// maxFilesScanned is the cap on total files indexed per workspace to prevent resource exhaustion.
const maxFilesScanned = 10000

var ErrWorkspaceTooLarge = errors.New("workspace too large")

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

// isPrunedDir reports whether a directory named name is pruned outright during
// the walk: a pruned directory is never descended into, so nothing beneath it is
// ever scanned, read, or counted individually — one skip per pruned subtree, not
// one per file.
//
// Two sets feed this, with different reasons and different matching rules:
//
//   - The dangerous ones (VCS internals, .codeterminal, credential dirs) are
//     editapply.ProtectedDirNames — the same list the edit WRITER refuses to
//     write into. That shared source of truth is the point: the two lists once
//     drifted so the indexer pruned .git while the writer wrote into it (Fix 3).
//     These are matched CASE-INSENSITIVELY (editapply.IsProtectedDirName): a
//     case-varied ".GIT" resolves to the real .git on a case-insensitive
//     filesystem, so a case-sensitive prune would index git internals there (S2).
//
//   - noiseDirNames are local build-output and dependency trees, skipped as
//     noise, deliberately NOT protected on the write path — editing vendored or
//     generated code is unusual but legitimate. These stay case-SENSITIVE: they
//     are not a security boundary, and folding them would risk pruning a
//     legitimately-cased source directory that merely shares a name (e.g. a Go
//     package literally named "Build").
var noiseDirNames = map[string]bool{
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

func isPrunedDir(name string) bool {
	return editapply.IsProtectedDirName(name) || noiseDirNames[name]
}

// ScanResult is the outcome of walking a workspace: every chunk produced
// (without vectors yet) plus counters for what was scanned and skipped.
type ScanResult struct {
	Chunks        []Chunk
	FilesScanned  int
	Skipped       map[SkipReason]int
	LimitExceeded bool
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

	ignore := newGitignoreMatcher(realRoot)
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

		// Never follow a link. A linked entry is reported by its OWN type, not
		// the type of whatever it points to, so this check alone stops the
		// escape without needing to inspect the target.
		//
		// IsLinkLike, not ModeSymlink: on Windows a junction comes back
		// ModeIrregular and this walk would have gone straight through it, out
		// of the workspace, indexing whatever it found into text the model is
		// then given. See editapply/linkmode.go.
		if editapply.IsLinkLike(d.Type()) {
			result.Skipped[SkipSymlink]++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if isPrunedDir(d.Name()) {
				result.Skipped[SkipIgnoredDir]++
				return fs.SkipDir
			}
			if ignore.matchDir(relPath) {
				result.Skipped[SkipGitignore]++
				return fs.SkipDir
			}
			return nil
		}

		if result.LimitExceeded {
			reason, skip, err := shouldSkipFile(path, relPath, ignore)
			if err == nil {
				if skip {
					result.Skipped[reason]++
				} else {
					result.FilesScanned++
				}
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
		if result.FilesScanned > maxFilesScanned {
			result.LimitExceeded = true
			return nil
		}
		// relPath stays native above this line — the gitignore matcher and the
		// secret-name gate both split on filepath.Separator. It becomes an index
		// key here, and index keys are forward-slash on every platform: see the
		// Chunk doc comment for what native keys cost on Windows.
		result.Chunks = append(result.Chunks, chunkContent(content, filepath.ToSlash(relPath))...)
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
func shouldSkipFile(path, relPath string, ignore *gitignoreMatcher) (SkipReason, bool, error) {
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

	// Never follow a symlink, and do it HERE rather than only in the walk.
	//
	// ScanWorkspace has always refused symlinked entries, and its comment said
	// that check "alone stops any symlink escape". That was true while the walk
	// was the only way in. It stopped being true when reindexFile arrived: it
	// calls readEligibleFile directly, so the incremental paths — after every
	// applied edit, and on every save the workspace watcher sees — reached the
	// read with no symlink check at all.
	//
	// Measured before this: a workspace containing `notes.md -> ~/.ssh/id_rsa`
	// returned the private key's contents. Every earlier gate passes it, and
	// each for a good reason — MatchesSecretName sees the LINK's harmless name,
	// and the size check below is why Lstat is used, so it measures the link
	// rather than the target. os.ReadFile then follows it. Indexed content is
	// retrieved into prompts, so this was a read primitive pointed at anything
	// the daemon's uid could open.
	//
	// Lstat reports the link's own type, so this needs no target inspection and
	// has no TOCTOU window of its own. IsLinkLike covers the Windows junction
	// this missed; see editapply/linkmode.go.
	if editapply.IsLinkLike(info.Mode()) {
		return SkipSymlink, true, nil
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
func readEligibleFile(path, relPath string, ignore *gitignoreMatcher) ([]byte, SkipReason, bool, error) {
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

	boundaries := constructStarts(lines)
	byteAt := byteOffsets(lines)

	var chunks []Chunk
	for start := 0; start < len(lines); {
		end, clean := snapEnd(lines, byteAt, start, chunkLines, boundaries)
		if end <= start {
			break // snapEnd never returns this; a guard against an infinite loop
		}

		chunkClass := class
		if chunkClass == FileClassCode && isCommentChunk(lines[start:end]) {
			chunkClass = FileClassDoc
		}

		startLine := start + 1 // 1-indexed for humans and logs
		endLine := end
		chunks = append(chunks, Chunk{
			ID:        fmt.Sprintf("%s:%d-%d", relPath, startLine, endLine),
			FilePath:  relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Content:   strings.Join(lines[start:end], "\n"),
			Class:     chunkClass,
		})

		if end == len(lines) {
			break
		}
		// A CLEAN CUT NEEDS NO OVERLAP. The overlap exists so a construct
		// straddling a window edge is whole in at least one window; when the
		// edge IS the construct's own boundary there is nothing straddling it,
		// and repeating ten lines would only cost budget and hand
		// mergeAdjacentChunks a seam to undo.
		if clean {
			start = end
			continue
		}
		// THE OVERLAP MUST NEVER WALK BACKWARDS. A chunk cut short by the byte
		// ceiling can be shorter than the overlap itself -- one 100 KB line is
		// a whole chunk -- and subtracting ten lines from it then produced a
		// NEGATIVE start, which indexed out of range and, had it not, would
		// have looped forever re-chunking the same lines. Overlap is an
		// optimisation for a forced cut; progress is not negotiable.
		if next := end - overlapLines; next > start {
			start = next
		} else {
			start = end
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

// isCommentChunk reports whether the chunk is overwhelmingly composed of
// comments or empty lines, so it can be reclassified as FileClassDoc.
func isCommentChunk(lines []string) bool {
	var commentLines, codeLines int
	inBlockComment := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if inBlockComment {
			commentLines++
			if strings.Contains(trimmed, "*/") || strings.Contains(trimmed, "-->") {
				inBlockComment = false
			}
			continue
		}

		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "--") {
			commentLines++
		} else if strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "<!--") {
			commentLines++
			if !strings.Contains(trimmed, "*/") && !strings.Contains(trimmed, "-->") {
				inBlockComment = true
			}
		} else if strings.HasPrefix(trimmed, "*") {
			commentLines++
		} else if strings.HasPrefix(trimmed, "package ") {
			codeLines++
		} else {
			codeLines++
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "type ") {
				// Protect well-commented structs/functions from being downweighted
				return false
			}
		}
	}

	total := commentLines + codeLines
	if total == 0 {
		return false
	}
	return (float64(commentLines) / float64(total)) >= 0.5
}

// gitignoreRule is one parsed line from a .gitignore file.
type gitignoreRule struct {
	pattern string
	dirOnly bool
	negate  bool // line began with "!": a re-include that overrides an earlier ignore
}

// gitignoreLayer is one directory's parsed .gitignore (its rules, in file
// order). An absent .gitignore is a cached empty layer, not a nil.
type gitignoreLayer struct {
	rules []gitignoreRule
}

// gitignoreMatcher resolves .gitignore rules the way git itself does: each
// directory's .gitignore applies to that directory and everything beneath it,
// every pattern is interpreted RELATIVE TO THE .gitignore THAT NAMED IT, deeper
// files override shallower ones, and within a single file a later line overrides
// an earlier one (which is what makes "!" negation work). Root-only resolution
// was a real leak: a secret excluded solely by a non-root .gitignore was indexed
// anyway (S1), because only the workspace-root .gitignore was ever read.
//
// It still implements a deliberately small *pattern* subset — exact names and
// simple shell globs (filepath.Match) against a path's basename or the path
// relative to the .gitignore that named it, trailing-slash directory-only rules,
// plain directory-prefix matches, and leading-"!" negation. It does NOT
// implement full gitignore glob syntax (** across separators, character classes,
// mid-pattern anchoring precision). Where a pattern isn't supported, the effect
// is to *not* ignore (index the file) unless a supported form also matches — the
// same conservative posture the previous root-only subset had, now applied at
// every level; the MatchesSecretName gate in shouldSkipFile is the independent
// backstop for the security-critical basenames regardless of ignore semantics.
//
// Layers are loaded lazily and cached, keyed by root-relative directory ("" is
// the root). A matcher is single-goroutine (one per ScanWorkspace / reindex
// call), so the cache needs no locking.
type gitignoreMatcher struct {
	root  string
	cache map[string]*gitignoreLayer
}

func newGitignoreMatcher(root string) *gitignoreMatcher {
	return &gitignoreMatcher{root: root, cache: make(map[string]*gitignoreLayer)}
}

// layerFor loads (and caches) the .gitignore in the root-relative directory
// relDir ("" = workspace root).
func (g *gitignoreMatcher) layerFor(relDir string) *gitignoreLayer {
	if layer, ok := g.cache[relDir]; ok {
		return layer
	}
	layer := parseGitignoreLayer(filepath.Join(g.root, relDir, ".gitignore"))
	g.cache[relDir] = layer
	return layer
}

func parseGitignoreLayer(path string) *gitignoreLayer {
	data, err := os.ReadFile(path)
	if err != nil {
		return &gitignoreLayer{}
	}
	var rules []gitignoreRule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := strings.HasPrefix(line, "!")
		if negate {
			line = line[1:]
		}
		dirOnly := strings.HasSuffix(line, "/")
		pattern := strings.TrimSuffix(line, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		if pattern == "" {
			continue
		}
		rules = append(rules, gitignoreRule{pattern: pattern, dirOnly: dirOnly, negate: negate})
	}
	return &gitignoreLayer{rules: rules}
}

// ancestorDirs returns the root-relative directories whose .gitignore can affect
// relPath: the workspace root ("") and every ancestor directory of relPath, up
// to but NOT including relPath's own basename. For "a/b/c.txt" it returns
// ["", "a", "a/b"]. A directory entry's OWN .gitignore never decides whether the
// directory itself is ignored — only its ancestors do — so the basename is always
// dropped regardless of whether relPath names a file or a directory.
func ancestorDirs(relPath string) []string {
	parts := strings.Split(relPath, string(filepath.Separator))
	dirs := []string{""}
	for i := 0; i < len(parts)-1; i++ {
		dirs = append(dirs, strings.Join(parts[:i+1], string(filepath.Separator)))
	}
	return dirs
}

// matches walks the ancestor .gitignore layers from shallowest to deepest,
// applying each matching rule in file order and letting the LAST match win —
// which is exactly git's precedence (deeper overrides shallower; within a file,
// later overrides earlier; "!" re-includes).
func (g *gitignoreMatcher) matches(relPath string, isDir bool) bool {
	if g == nil {
		return false
	}
	ignored := false
	for _, dir := range ancestorDirs(relPath) {
		sub := relPath
		if dir != "" {
			sub = strings.TrimPrefix(relPath, dir+string(filepath.Separator))
		}
		for _, r := range g.layerFor(dir).rules {
			if r.dirOnly && !isDir {
				continue
			}
			if ruleMatchesSub(r.pattern, sub) {
				ignored = !r.negate
			}
		}
	}
	return ignored
}

// ruleMatchesSub tests one pattern against a path already made relative to the
// .gitignore that named it: basename match, full relative-path match, or a plain
// directory-prefix match (the small subset this resolver supports).
func ruleMatchesSub(pattern, sub string) bool {
	if ok, _ := filepath.Match(pattern, filepath.Base(sub)); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, sub); ok {
		return true
	}
	return strings.HasPrefix(sub, pattern+string(filepath.Separator))
}

func (g *gitignoreMatcher) matchDir(relPath string) bool  { return g.matches(relPath, true) }
func (g *gitignoreMatcher) matchFile(relPath string) bool { return g.matches(relPath, false) }
