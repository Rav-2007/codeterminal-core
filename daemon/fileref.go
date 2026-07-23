package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Fix 12: resolve the exact pointers a pasted build/test failure already
// contains, instead of leaving them to similarity search.
//
// The diagnosis behind this: edit-shaped prompts are long and mostly pasted
// tool output, so embedding the WHOLE prompt as one vector matches nothing
// well — the measured misses sat at ranks #79 and #305 of ~1188. But that
// pasted output is not opaque. A compiler or test runner prints
// "./helperproc_test.go:136:4: h.extraEnv undefined", which names a file and a
// line exactly. Nothing about an exact pointer should be left to fuzzy
// matching, so these are resolved straight off disk and put at the TOP of the
// retrieved set.
//
// Read the honest scope limit with it: for Go's "undefined:" class of error
// the pointer names the USE site (the test that calls the missing method), not
// the DEFINITION site (the implementation that must grow it). This retrieves
// the line the tool complained about — necessary context the model previously
// did not get at all — and it does not, on its own, find the place the edit
// belongs. See BACKLOG.md and the eval's referenced-span metric.
const (
	// directSpanContextLines is how many lines either side of a referenced
	// line are included, making a ~41-line span — deliberately comparable to
	// the indexer's own chunkLines=40 window, so a direct span and an index
	// chunk are the same order of size and merge cleanly (chunkmerge.go).
	directSpanContextLines = 20

	// maxParsedRefs bounds how many distinct file:line references are even
	// looked at. A pasted failure can name hundreds of lines; parsing all of
	// them would turn one prompt into hundreds of stat calls for no benefit,
	// since only a handful can fit the context budget anyway.
	maxParsedRefs = 24

	// maxDirectSpans bounds how many resolved spans reach the prompt, so
	// direct resolution can never crowd similarity retrieval out entirely.
	// Also clamped to k-1 by fuseDirectSpans.
	maxDirectSpans = 3
)

// fileLineRef is one file:line (or file:line:col) pointer found in a prompt.
type fileLineRef struct {
	Path string // as written, cleaned of a leading "./"
	Line int
}

// fileLineRefPattern matches the pointer shape compilers, linters, test
// runners and stack traces print: an optional directory prefix, a filename
// with an extension, then ":LINE" and optionally ":COL".
//
// The trailing extension is load-bearing — it is what keeps this from firing
// on clock times ("10:30"), ratios, "Error:42", and Go's own "package:line"
// log prefixes. What it does NOT try to exclude is host:port in a URL
// ("api.example.com:8080"): that is left to resolution, which requires the
// path to name a real, indexable file inside the workspace and so discards it
// anyway. Filtering by existence rather than by regex cleverness keeps the
// pattern readable and the security property in one place.
var fileLineRefPattern = regexp.MustCompile(
	`(?m)(?:^|[\s"'` + "`" + `(\[<])` + // start of a line, or a plausible delimiter
		`((?:\.{0,2}/)?(?:[\w.@+-]+/)*` + // optional ./ or ../, then directories
		`[\w@+-]+(?:\.[\w@+-]+)*\.[A-Za-z][A-Za-z0-9]*)` + // filename with an extension
		`:(\d+)(?::\d+)?`) // :line, optional :col

// parseFileLineRefs extracts every distinct file:line pointer from prompt, in
// the order it appears, capped at maxParsedRefs. Order matters: the first
// reference in a build failure is the first error the tool reported, which is
// usually the one to act on.
func parseFileLineRefs(prompt string) []fileLineRef {
	matches := fileLineRefPattern.FindAllStringSubmatch(prompt, -1)
	seen := make(map[string]bool, len(matches))
	refs := make([]fileLineRef, 0, len(matches))
	for _, m := range matches {
		line, err := strconv.Atoi(m[2])
		if err != nil || line < 1 {
			continue
		}
		path := strings.TrimPrefix(filepath.ToSlash(m[1]), "./")
		if path == "" {
			continue
		}
		key := path + ":" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, fileLineRef{Path: path, Line: line})
		if len(refs) == maxParsedRefs {
			break
		}
	}
	return refs
}

// resolveFileLineRefs turns the pointers in prompt into concrete spans read
// from disk under workspaceRoot, best-effort: a reference that cannot be
// resolved is skipped and the rest still resolve. It never returns an error —
// direct resolution is an enhancement to retrieval, and retrieval must never
// prevent generation (the same contract gatherContext already holds).
//
// Every span is subject to the SAME eligibility gates the indexer applies
// (shouldSkipFile, chunker.go): secret-named files, .gitignored files, binary
// files and oversized files are refused, and the resolved path must be a
// regular file confined to workspaceRoot with no symlink escape. This is not
// incidental. Chunk text leaves the machine — it is POSTed to the hosted
// completion provider on every grounded turn — so without these gates a
// pasted ".env:1" or a stack frame pointing at a credential file would be a
// straightforward way to make the daemon read a secret and send it upstream.
// Direct resolution deliberately cannot reach anything indexing would not.
func resolveFileLineRefs(prompt, workspaceRoot string, logger interface{ Printf(string, ...any) }) []Chunk {
	resolved := resolveRefs(prompt, workspaceRoot, logger)
	if len(resolved) == 0 {
		return nil
	}
	spans := make([]Chunk, len(resolved))
	for i, r := range resolved {
		spans[i] = r.Span
	}
	return spans
}

// resolvedRef is one file:line pointer that survived resolution: the
// workspace-relative file it actually names, the line it named, and the span
// read around it. The (RelPath, Line) pair is what makes "did the prompt get
// the line the tool complained about?" a measurable question — see the
// referenced-span metric in edit_eval_test.go.
type resolvedRef struct {
	RelPath string
	Line    int
	Span    Chunk
}

// resolveRefs is resolveFileLineRefs's full result; see its doc comment for
// the contract and the eligibility gates, which live here.
func resolveRefs(prompt, workspaceRoot string, logger interface{ Printf(string, ...any) }) []resolvedRef {
	if workspaceRoot == "" {
		return nil
	}
	refs := parseFileLineRefs(prompt)
	if len(refs) == 0 {
		return nil
	}

	realRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil
	}
	ignore := newGitignoreMatcher(realRoot)

	// Two passes so the (relatively expensive) basename search happens at most
	// once for the whole prompt: first resolve everything that names a real
	// workspace-relative path, then look up whatever is left by suffix.
	type pending struct {
		ref     fileLineRef
		relPath string // resolved, workspace-relative; empty until resolved
	}
	items := make([]pending, 0, len(refs))
	var unresolved []string
	for _, r := range refs {
		rel := workspaceRelPath(realRoot, r.Path)
		items = append(items, pending{ref: r, relPath: rel})
		if rel == "" {
			unresolved = append(unresolved, r.Path)
		}
	}
	if len(unresolved) > 0 {
		// A tool run inside a subdirectory prints package-relative paths
		// ("./helperproc_test.go") that mean nothing from the workspace root.
		// Resolve those by unique path suffix; ambiguity is refused, not
		// guessed at, same discipline as the edit parser's separator rule.
		found := findFilesBySuffix(realRoot, ignore, unresolved)
		for i := range items {
			if items[i].relPath == "" {
				items[i].relPath = found[items[i].ref.Path]
			}
		}
	}

	var resolved []resolvedRef
	for _, it := range items {
		if it.relPath == "" {
			continue
		}
		span, err := readReferencedSpan(realRoot, it.relPath, it.ref.Line, ignore)
		if err != nil {
			if logger != nil {
				logger.Printf("direct ref %s:%d not resolved: %v", it.ref.Path, it.ref.Line, err)
			}
			continue
		}
		resolved = append(resolved, resolvedRef{RelPath: it.relPath, Line: it.ref.Line, Span: span})
	}
	return resolved
}

// workspaceRelPath returns refPath as a clean workspace-relative path if it
// names a regular file confined to realRoot, or "" if it doesn't resolve that
// way — a nonexistent path, a directory, a path escaping the root, or a
// symlink pointing outside it.
func workspaceRelPath(realRoot, refPath string) string {
	candidate := refPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(realRoot, refPath)
	}

	// EvalSymlinks resolves every component, so a symlinked path (or a
	// symlinked parent directory) can't be used to reach outside the root —
	// the same confinement ScanWorkspace gets by refusing to follow symlinks
	// during the walk.
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	info, err := os.Lstat(real)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return filepath.ToSlash(rel)
}

// findFilesBySuffix walks realRoot once looking for files whose relative path
// ends with any of the given suffixes, and returns suffix -> unique relative
// path. A suffix matching two or more files is left OUT of the result: an
// ambiguous pointer is refused rather than resolved to a coin flip.
//
// The walk prunes exactly what the indexer prunes (ignoredDirNames, gitignored
// directories, symlinks), so it never descends into node_modules, .git or
// anything else ScanWorkspace wouldn't — that keeps the cost proportional to
// the source tree and keeps this from finding files indexing can't see.
func findFilesBySuffix(realRoot string, ignore *gitignoreMatcher, suffixes []string) map[string]string {
	want := make(map[string]bool, len(suffixes))
	for _, s := range suffixes {
		want[s] = true
	}
	matches := make(map[string][]string, len(suffixes))

	filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == realRoot {
			return nil //nolint:nilerr // an unreadable subtree just isn't searched
		}
		rel, relErr := filepath.Rel(realRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if ignoredDirNames[d.Name()] || ignore.matchDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		for s := range want {
			if rel == s || strings.HasSuffix(rel, "/"+s) {
				matches[s] = append(matches[s], rel)
			}
		}
		return nil
	})

	unique := make(map[string]string, len(matches))
	for s, found := range matches {
		if len(found) == 1 {
			unique[s] = found[0]
		}
	}
	return unique
}

// readReferencedSpan reads the window of directSpanContextLines either side of
// line in relPath and returns it as a Chunk whose declared range describes
// exactly the lines it holds. It reads CURRENT disk content, not the index, so
// a reference resolves correctly even when the file has changed since the last
// index run (if the two disagree, chunkmerge's overlap check simply declines to
// splice them and both are kept).
//
// Errors — rather than a silent empty chunk — for every degradation case, so
// the caller can log why a pointer was dropped: a file the indexer would skip,
// an unreadable file, or a line number past the end of the file.
func readReferencedSpan(realRoot, relPath string, line int, ignore *gitignoreMatcher) (Chunk, error) {
	absPath := filepath.Join(realRoot, filepath.FromSlash(relPath))

	// Exactly the indexer's eligibility gate: secret-named, gitignored,
	// oversized and binary files are all off limits here too.
	if reason, skip, err := shouldSkipFile(absPath, relPath, ignore); err != nil {
		return Chunk{}, fmt.Errorf("checking eligibility: %w", err)
	} else if skip {
		return Chunk{}, fmt.Errorf("file is excluded from indexing (%s)", reason)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return Chunk{}, err
	}
	lines := splitLines(content)
	if line > len(lines) {
		return Chunk{}, fmt.Errorf("line %d is past the end of the file (%d lines)", line, len(lines))
	}

	start := line - directSpanContextLines
	if start < 1 {
		start = 1
	}
	end := line + directSpanContextLines
	if end > len(lines) {
		end = len(lines)
	}

	return Chunk{
		ID:        fmt.Sprintf("%s:%d-%d", relPath, start, end),
		FilePath:  relPath,
		StartLine: start,
		EndLine:   end,
		Content:   strings.Join(lines[start-1:end], "\n"),
		Class:     classifyFile(relPath),
	}, nil
}

// fuseDirectSpans combines directly-resolved spans with similarity-retrieved
// chunks into the final ranked set.
//
// Direct spans go FIRST — that is the whole point of resolving an exact
// pointer deterministically, and it is what moves a referenced location from
// "buried at rank #79, i.e. never seen" to "first thing the model reads". They
// then go through the same mergeAdjacentChunks pass everything else does
// (chunkmerge.go), which is what makes fusion dedupe rather than duplicate: a
// direct span and a similarity chunk covering the same lines become ONE span
// at the direct span's position, not two overlapping copies of the same code.
//
// Direct spans are capped at maxDirectSpans, and further at k-1, so similarity
// retrieval always keeps at least one slot: a prompt that pastes fifty
// compiler errors must not turn retrieval into "the fifty lines the compiler
// mentioned and nothing else".
//
// SavedBytes is measured across the merge alone (both sides untruncated), so
// it reports what folding duplicated overlap actually reclaimed and never
// counts the k-truncation as a saving.
func fuseDirectSpans(direct, similar []Chunk, k int, scrubDisabled bool) fusedRetrieval {
	// Fold the direct spans into each other BEFORE capping, so the cap bounds
	// SPANS reaching the prompt rather than raw references. A build failure
	// naming four lines in one file is four references but one region: merged
	// first, that is a single span and all four locations survive; capped
	// first, the fourth reference would be discarded for no gain. Measured on
	// the eval's tui case, this is the difference between 3/4 and 4/4
	// referenced locations reaching the prompt.
	direct = mergeAdjacentChunks(direct)

	limit := maxDirectSpans
	if k >= 2 && limit > k-1 {
		limit = k - 1
	}
	if len(direct) > limit {
		direct = direct[:limit]
	}

	fused := make([]Chunk, 0, len(direct)+len(similar))
	fused = append(fused, direct...)
	fused = append(fused, similar...)

	merged := mergeAdjacentChunks(fused)

	out := fusedRetrieval{InputCount: len(fused), DirectSpans: len(direct)}
	if len(merged) != len(fused) {
		out.SavedBytes = mergeSavings(fused, merged, scrubDisabled)
	}
	if k > 0 && len(merged) > k {
		merged = merged[:k]
	}
	out.Chunks = merged
	return out
}

// fusedRetrieval is what one request's retrieval produced, plus the numbers
// needed to log what fusion and merging did to it.
type fusedRetrieval struct {
	Chunks      []Chunk // final: direct-first, merged, truncated to k
	InputCount  int     // direct + similarity chunks before merging
	SavedBytes  int     // rendered bytes merging reclaimed (pre-truncation, both sides)
	DirectSpans int     // directly-resolved spans that survived the cap
}
