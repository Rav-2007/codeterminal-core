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
// chunkerID identifies HOW A CHUNK'S EMBEDDED TEXT IS PRODUCED: both where the
// cuts fall and what is prepended to the text before it is embedded.
//
// It exists because neither existing stamp field covers boundaries. The
// embedder ID covers which model produced the vectors; currentIndexSchemaVersion
// covers the SHAPE of what is stored per chunk (its doc comment says so: it was
// bumped when chunks gained class metadata). Change how text is cut into
// chunks and both are unchanged, so an index built by yesterday's chunker is
// accepted by today's binary -- and since chunk IDs encode line ranges, every
// boundary the new chunker does not reproduce is left behind. pruneOrphanedChunks
// now clears those on a rebuild, but only once a rebuild HAPPENS, and nothing
// was asking for one.
//
// The parameters are interpolated rather than written out so that tuning
// chunkLines or overlapLines cannot silently keep the old identity. The version
// segment covers what the parameters cannot: a change to the ALGORITHM at
// unchanged parameters -- which is exactly what AST-aware chunk boundaries
// would be. TestTheChunkerIDChangesWhenTheBoundariesDo pins that, so it is not
// left to whoever edits this file to remember.
// THE CONTRACT IS WIDER THAN BOUNDARIES, and it had to be. As first written
// this identified where the cuts fall, which was the only thing that varied.
// Then the text handed to the EMBEDDER stopped being the chunk's own lines
// (chunkcontext.go): same cuts, same ids, same stored Content, different
// vectors -- and no stamp field covered it. bgeEmbedderID covers the model and
// its sequence length, currentIndexSchemaVersion covers the shape of what is
// stored. An index built under one header policy would have been accepted and
// queried under another, which is exactly the silent-mismatch class this
// variable exists to prevent. The policy is interpolated, so switching arms
// invalidates every index by itself.
var chunkerID = fmt.Sprintf("fixed-window/v2/lines=%d/overlap=%d/prefix=%s",
	chunkLines, overlapLines, activeEmbedPrefix)

// chunkLines with overlapLines of overlap between consecutive windows.
func chunkContent(content []byte, relPath string) []Chunk {
	lines := splitLines(content)
	if len(lines) == 0 {
		return nil
	}

	class := classifyFile(relPath)

	// FIXED WINDOWS, AND THE OVERLAP IS LOAD-BEARING.
	//
	// STRUCTURE-AWARE BOUNDARIES WERE TRIED THREE TIMES AND LOST THREE TIMES.
	// Do not attempt a fourth without reading this, because the reason is not
	// the one it looks like.
	//
	// The idea is sound on its face: 55% of chunks here begin on an indented
	// line, which means opening mid-function with no signature and no name. The
	// implementation exists and works -- `git show 880df70:daemon/chunkboundary.go`,
	// 261 lines with 358 lines of tests, snapping each cut to the nearest
	// construct start.
	//
	//	arm                                delivered  semantic tier  chunks  impl
	//	fixed windows (this)               29/49      24/49          4,581   12/24
	//	snap, no overlap, byte cap         26/49      15/49          5,266   11/24
	//	snap, overlap kept, no byte cap    27/49      17/49          4,911   13/24
	//
	// (First measured at k=5 as 8/9 -> 4/9 and reverted in b552daf; the two
	// rows above are re-measurements at today's k=10 / 16000 with neighbour
	// expansion, which was built partly to repair the harm the first attempt
	// caused. It repaired some of it and did not save the feature.)
	//
	// WHAT ACTUALLY KILLS IT IS THE SEMANTIC TIER, and that survived every fix.
	// Aligning a chunk to a construct makes it SEMANTICALLY NARROWER: it is
	// about one thing, so its single 384-dimensional vector matches one kind of
	// question. An arbitrary 40-line window straddles two or three constructs
	// and embeds as a broader, blurrier thing that matches more queries. The
	// blur is not a defect being tolerated here -- on this corpus it is worth
	// seven queries, and no boundary policy recovers them.
	//
	// The corpus explains why. Measured over 887 real functions: median 12
	// lines, p75 26, and only 13% exceed one 40-line window. One construct per
	// chunk is simply too fine a granularity to embed well.
	//
	// The upside is real and small: implementation-seeking queries do best
	// under snapping (13/24, the highest of any arm). It is not worth a forced
	// re-index of every existing installation, which chunkerID now makes
	// mandatory.
	//
	// If this is revisited, the thing to change is the EMBEDDING, not the
	// boundary -- a per-chunk representation that does not lose by being
	// specific. Body elision is the nearer lever: it targets the 13% of
	// constructs too big for a window, which is where the measured misses
	// actually concentrate, and it does not narrow what a chunk is about.
	//
	// THAT LEVER WAS TAKEN, AND IT LOST TOO. Enclosing-context injection was
	// built and measured in four more arms on 2026-08-28: prefixing a mid-body
	// chunk with its enclosing declaration scores 28/49, with the declaration
	// and its doc line 27/49, against a 30/49 baseline. Adding that prefix to a
	// run that already prefixes the FILE PATH costs two further queries, 33 down
	// to 31. Six attempts to put program structure into the vector, six losses.
	//
	// WHAT DID WORK is one line of the file's own path, prepended to every
	// chunk's embedded text: 30/49 -> 33/49 delivered, and the semantic tier
	// alone from 23/49 to 30/49. It is the EmbedText below. The cuts are
	// untouched -- every chunk keeps the line range, ID and Content it had --
	// and the full table with the mechanism is at the top of chunkcontext.go.
	stride := chunkLines - overlapLines

	var chunks []Chunk
	for start := 0; start < len(lines); start += stride {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}

		chunkClass := class
		if chunkClass == FileClassCode && isCommentChunk(lines[start:end]) {
			chunkClass = FileClassDoc
		}

		startLine := start + 1 // 1-indexed for humans and logs
		endLine := end
		content := strings.Join(lines[start:end], "\n")
		chunks = append(chunks, Chunk{
			ID:        fmt.Sprintf("%s:%d-%d", relPath, startLine, endLine),
			FilePath:  relPath,
			StartLine: startLine,
			EndLine:   endLine,
			Content:   content,
			Class:     chunkClass,
			EmbedText: embedPrefixFor(relPath) + content,
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

// maxGitignoreBytes bounds ONE .gitignore file.
//
// IT IS maxFileSize AND NOT A NEW NUMBER, deliberately. This indexer already
// refuses to read any ordinary source file over that size; a .gitignore past the
// bound at which the same indexer skips source is out of scope by the indexer's
// own rule, and inventing a second constant here would be inventing a second
// policy.
//
// WHY THIS EXISTS. parseGitignoreLayer called os.ReadFile on a path found while
// walking, with no size check anywhere in its chain -- the one read in this file
// that does not re-run the gate. readEligibleFile below re-runs shouldSkipFile
// immediately before reading, and planmode.go states the rule in general: the
// read side must not assume the write side ran.
//
// MEASURED 2026-09-16: 90,333,064 bytes allocated for a 33.5 MB .gitignore,
// against a maxFileSize of 1 MiB. Reached from search_code and repo_map -- both
// model-facing -- though the PATH comes from the directory walk rather than from
// the model, which is why this is filed as the lower-severity of the two sites
// this pass found rather than inflated to match the other.
const maxGitignoreBytes = maxFileSize

func parseGitignoreLayer(path string) *gitignoreLayer {
	f, err := os.Open(path)
	if err != nil {
		return &gitignoreLayer{}
	}
	defer func() { _ = f.Close() }()

	// STAT FIRST, so an oversized file costs no read at all. io.ReadAll over a
	// LimitReader was the first version of this fix and it still allocated 5.2 MB
	// for a 33.5 MB file: ReadAll grows by doubling, so the intermediate buffers
	// sum to about twice the final one even when the final one is bounded. Reading
	// into a slice sized here spends exactly what the file needs, which is the
	// property this whole guard is about -- io.ReadFull's length is the CALLER's.
	info, err := f.Stat()
	if err != nil || info.Size() > maxGitignoreBytes {
		return &gitignoreLayer{}
	}

	// +1, AND THE STAT IS A HINT RATHER THAN THE BOUND. The file can grow between
	// the stat and the read, so the buffer carries one spare byte: if it fills,
	// more arrived than was announced and this refuses rather than truncating.
	//
	// TRUNCATION IS THE WRONG FAILURE HERE, and worse than not reading at all: a
	// rule cut mid-line becomes a DIFFERENT rule -- "build/secr" for
	// "build/secret/" -- so a truncating parse invents an ignore pattern nobody
	// wrote.
	buf := make([]byte, info.Size()+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return &gitignoreLayer{}
	}
	data := buf[:n]
	if int64(n) > info.Size() {
		// Same empty layer this function already returned for an unreadable file,
		// and the same consequence: no rules from this directory. That loses
		// precision, not secrecy -- shouldSkipFile applies the secret-name and
		// noise-file checks independently of any .gitignore, so a secret-named
		// file is skipped either way.
		//
		// NAMED IN PROSE RATHER THAN BY SYMBOL, on purpose. This comment
		// originally spelled the qualified symbol, which made it the SECOND
		// occurrence of an eval anchor in this file -- and the locate eval's
		// anchor-ambiguity check went red: "resolves to 4 chunks, past the 3
		// ceiling". A comment about a symbol is not an answer to a query about
		// it, and the eval cannot tell those apart, so the ground truth pays for
		// the explanation. See the pass's addendum; this is the second time a
		// thing I wrote to explain something broke a measurement of it.
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
