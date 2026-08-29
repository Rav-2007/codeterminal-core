package editapply

import (
	"fmt"
	"strings"
)

// Bounds on what this reader will look at. Model output and pasted patches are
// both untrusted, and a diff is the one edit format whose size is not bounded
// by anything the parser has already agreed to.
const (
	maxDiffBytes = 8 << 20 // 8 MiB
	maxDiffLines = 200000
)

// ParseUnifiedDiff reads a unified diff (git or plain `diff -u`) and returns
// the same pair ParseEditBlocks does, so nothing downstream learns a second
// format.
//
// The translation is the whole idea: a hunk's (context + '-') lines ARE the
// SEARCH text and its (context + '+') lines ARE the REPLACE text, so a diff
// becomes ordinary EditBlocks and every existing gate -- path confinement,
// exact match, ambiguity refusal, the syntax gate, confirmation, backup --
// applies to it unchanged. This adds no write path. It adds a reader.
//
// One EditBlock PER HUNK, not per file. That mirrors parseBlockAt's per-block
// recovery (a bad hunk costs only itself) and printEditDiff's per-block review
// (the user confirms one change at a time, seeing exactly what it touches).
//
// What it refuses is as much of the design as what it accepts. A unified diff
// can express deletes, renames and mode changes; this engine has no primitive
// for any of them, and the nearest available substitution is always worse than
// a refusal -- writing an empty file is not deleting one, and `edits undo`
// could not tell the difference afterwards. Each is refused by name, with the
// line it appeared on, exactly like a malformed SEARCH/REPLACE block.
func ParseUnifiedDiff(response string) ([]EditBlock, []BlockError) {
	if len(response) > maxDiffBytes {
		return nil, []BlockError{{Line: 1, Reason: fmt.Sprintf("line 1: diff is %d bytes, over the %d-byte limit; refusing to parse it", len(response), maxDiffBytes)}}
	}
	lines := strings.Split(response, "\n")
	// strings.Split always yields a trailing "" for text ending in a newline,
	// and every real patch ends in one. That phantom is indistinguishable from
	// an empty context line (see readHunk), so a hunk whose declared count is
	// one too high would absorb it and produce a SEARCH with a spurious
	// trailing blank line -- which then fails to match, as a "not found"
	// rather than as the count error it actually is. Dropping it here keeps
	// the count check honest.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > maxDiffLines {
		return nil, []BlockError{{Line: 1, Reason: fmt.Sprintf("line 1: diff is %d lines, over the %d-line limit; refusing to parse it", len(lines), maxDiffLines)}}
	}

	p := &diffParser{lines: lines}
	p.run()
	return p.blocks, p.rejected
}

// diffParser walks the response once, carrying the state of the file stanza it
// is currently inside. Everything it does not recognise -- prose around the
// diff, `index` lines, `similarity index` -- is skipped rather than refused,
// because a model explaining its change above the patch is normal and must not
// cost the patch.
type diffParser struct {
	lines    []string
	blocks   []EditBlock
	rejected []BlockError

	// current file stanza
	path      string // resolved workspace-relative target, "" when unknown
	creates   bool   // old side was /dev/null
	skip      bool   // stanza refused; its hunks must not be emitted
	appliedTo map[string][]hunkExtent
}

// hunkExtent is one hunk's footprint in the ORIGINAL file, used only to detect
// overlap between hunks targeting the same file.
type hunkExtent struct {
	start, count, line int
}

func (p *diffParser) run() {
	p.appliedTo = map[string][]hunkExtent{}

	for i := 0; i < len(p.lines); {
		line := p.lines[i]

		switch {
		case strings.HasPrefix(line, "diff --git "):
			p.resetStanza()
			i++

		case strings.HasPrefix(line, "@@@"):
			// A combined (merge) diff has one column per parent and no single
			// "before" text, so there is nothing to put in SEARCH.
			p.refuse(i, "combined/merge diff (\"@@@\") has no single before-text to search for; re-send an ordinary two-way diff")
			p.skip = true
			i++

		case strings.HasPrefix(line, "--- "):
			i = p.readFileHeader(i)

		case isHunkHeader(strings.TrimSpace(line)):
			i = p.readHunk(i)

		case strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "rename to "):
			p.refuse(i, "the diff renames a file; this engine can only change a file's contents, never its name. Rename it yourself, then send a diff against the new path")
			p.skip = true
			i++

		case strings.HasPrefix(line, "deleted file mode "):
			p.refuse(i, "the diff deletes a file; this engine only writes bytes and has no delete step, and emptying the file instead would be a different change that `edits undo` could not tell apart from a real edit")
			p.skip = true
			i++

		case isExecutableModeLine(line):
			// create.go fixes the new-file mode at 0644 precisely so an edit
			// block cannot produce an executable file. A mode line in model
			// output must not be the thing that undoes that.
			p.refuse(i, "the diff sets an executable file mode (100755); this engine writes every new file 0644 on purpose and will not take a mode from a patch")
			p.skip = true
			i++

		case line == "GIT binary patch" || strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "Files "):
			p.refuse(i, "binary patch; there is no text to show you as a diff and nothing to review, so this is refused rather than applied unseen")
			p.skip = true
			i++

		default:
			i++
		}
	}
}

func (p *diffParser) resetStanza() {
	p.path = ""
	p.creates = false
	p.skip = false
}

func (p *diffParser) refuse(i int, reason string) {
	p.rejected = append(p.rejected, BlockError{Line: i + 1, Reason: fmt.Sprintf("line %d: %s", i+1, reason)})
}

// readFileHeader consumes a "--- old" / "+++ new" pair and resolves the target
// path. A "---" with no "+++" immediately after it is not a file header at all
// (it is a markdown rule or a signature separator), so it is skipped, not
// refused.
func (p *diffParser) readFileHeader(i int) int {
	if i+1 >= len(p.lines) || !strings.HasPrefix(p.lines[i+1], "+++ ") {
		return i + 1
	}
	oldRaw := diffHeaderPath(p.lines[i][len("--- "):])
	newRaw := diffHeaderPath(p.lines[i+1][len("+++ "):])

	// A stanza already refused for a reason the header cannot see (a rename
	// marker, a mode line) keeps that refusal; re-deciding here would report
	// the same file twice.
	if p.skip {
		return i + 2
	}
	p.resetStanza()

	path, creates, err := resolveDiffPaths(oldRaw, newRaw)
	if err != nil {
		p.refuse(i, err.Error())
		p.skip = true
		return i + 2
	}
	p.path, p.creates = path, creates
	return i + 2
}

// readHunk parses one "@@ -l,c +l,c @@" header and its body into an EditBlock.
//
// The body is consumed by COUNT, not by looking for a terminator: the header
// declares how many old-side and new-side lines follow, and the scan stops when
// both are satisfied. That choice does two jobs at once. It makes the
// count-vs-body check structural rather than a separate validation -- a hunk
// truncated by a cut-off stream runs out of lines before its counts are met and
// is refused -- and it lets a zero-length line be read as an empty context
// line, which is what mailers and copy-paste produce when they strip the
// leading space git emits.
func (p *diffParser) readHunk(i int) int {
	hdr, ok := parseHunkHeader(strings.TrimSpace(p.lines[i]))
	if !ok {
		return i + 1
	}

	var search, replace []string
	oldSeen, newSeen := 0, 0
	noNewlineOld, noNewlineNew := false, false
	lastSide := sideNone

	j := i + 1
	for j < len(p.lines) && (oldSeen < hdr.oldCount || newSeen < hdr.newCount) {
		l := p.lines[j]
		switch {
		case strings.HasPrefix(l, "\\"):
			// "\ No newline at end of file" describes the line before it.
			if lastSide == sideOld || lastSide == sideBoth {
				noNewlineOld = true
			}
			if lastSide == sideNew || lastSide == sideBoth {
				noNewlineNew = true
			}

		case l == "" || strings.HasPrefix(l, " "):
			// A context line belongs to BOTH sides, so it needs room on both.
			// Without this a pure-insertion header ("@@ -0,0 +1,2 @@") followed
			// by a context line pushed the old side past a declared count of
			// zero, and the hunk parsed into a block whose SEARCH and REPLACE
			// were both empty -- which for a /dev/null stanza is "create this
			// file with no content", a change nobody asked for. Found by
			// FuzzParseUnifiedDiff.
			if oldSeen >= hdr.oldCount || newSeen >= hdr.newCount {
				p.refuse(i, overrunReason(hdr))
				return j
			}
			content := ""
			if l != "" {
				content = l[1:]
			}
			search = append(search, content)
			replace = append(replace, content)
			oldSeen++
			newSeen++
			lastSide = sideBoth

		case strings.HasPrefix(l, "-"):
			if oldSeen >= hdr.oldCount {
				p.refuse(i, overrunReason(hdr))
				return j
			}
			search = append(search, l[1:])
			oldSeen++
			lastSide = sideOld

		case strings.HasPrefix(l, "+"):
			if newSeen >= hdr.newCount {
				p.refuse(i, overrunReason(hdr))
				return j
			}
			replace = append(replace, l[1:])
			newSeen++
			lastSide = sideNew

		default:
			// A line that is not part of any hunk body arrived before the
			// declared counts were met.
			p.refuse(i, fmt.Sprintf("hunk declares %d line(s) before and %d after, but its body ends after %d and %d; the diff is truncated or malformed", hdr.oldCount, hdr.newCount, oldSeen, newSeen))
			return j
		}
		j++
	}

	// The counts are satisfied, but "\ No newline at end of file" carries no
	// line of its own and so is never counted -- meaning a marker attached to
	// the hunk's LAST line sits just past where the count loop stopped. Not
	// reading it here is what made a symmetric pair look asymmetric, turning
	// an ordinary edit to a file with no trailing newline into a refusal.
	for j < len(p.lines) && strings.HasPrefix(p.lines[j], "\\") {
		if lastSide == sideOld || lastSide == sideBoth {
			noNewlineOld = true
		}
		if lastSide == sideNew || lastSide == sideBoth {
			noNewlineNew = true
		}
		j++
	}

	if oldSeen < hdr.oldCount || newSeen < hdr.newCount {
		p.refuse(i, fmt.Sprintf("hunk declares %d line(s) before and %d after, but only %d and %d are present; the diff is truncated", hdr.oldCount, hdr.newCount, oldSeen, newSeen))
		return j
	}
	if p.skip {
		return j
	}
	if p.path == "" {
		p.refuse(i, "hunk has no file to apply to (no \"--- \"/\"+++ \" header and no \"diff --git\" line above it)")
		return j
	}

	// The final newline lives OUTSIDE the text a hunk matches, so a change to
	// it cannot be expressed as a SEARCH/REPLACE on that text. Splicing anyway
	// would silently drop or add one byte at end of file.
	if noNewlineOld != noNewlineNew {
		p.refuse(i, "this hunk changes whether the file ends in a newline, which is outside the text it replaces; applying it as an ordinary edit would silently add or drop that byte")
		return j
	}

	searchText := strings.Join(search, "\n")
	replaceText := strings.Join(replace, "\n")

	// An empty SEARCH is the CREATE instruction (see IsEmptySearch). It is
	// correct for a /dev/null old side and wrong for anything else: a pure
	// insertion into an existing file has no text to locate, and letting it
	// through would turn "add three lines" into "this file's whole content is
	// these three lines".
	if IsEmptySearch(searchText) && !p.creates {
		p.refuse(i, "hunk only adds lines, so it has no before-text to locate. This engine replaces text it can find; re-send it with a few surrounding context lines")
		return j
	}
	if searchText == replaceText {
		// Applies to creates too: a /dev/null stanza whose hunk adds no lines
		// asks to create an empty file, which is not a change this reader will
		// walk a user through a confirmation prompt for.
		p.refuse(i, "hunk changes nothing (its before- and after-text are identical)")
		return j
	}

	if prev, clash := p.overlaps(hdr); clash {
		p.refuse(i, fmt.Sprintf("this hunk overlaps the one at line %d, which covers the same lines of %s; applying both in order would leave the second searching for text the first had already changed", prev.line, p.path))
		return j
	}
	p.appliedTo[p.path] = append(p.appliedTo[p.path], hunkExtent{start: hdr.oldStart, count: hdr.oldCount, line: i + 1})

	p.blocks = append(p.blocks, EditBlock{FilePath: p.path, Search: searchText, Replace: replaceText})
	return j
}

// overlaps reports whether hdr covers original lines an earlier hunk for the
// same file already claimed.
func (p *diffParser) overlaps(hdr hunkHeader) (hunkExtent, bool) {
	end := hdr.oldStart + hdr.oldCount
	for _, prev := range p.appliedTo[p.path] {
		prevEnd := prev.start + prev.count
		if hdr.oldStart < prevEnd && prev.start < end {
			return prev, true
		}
	}
	return hunkExtent{}, false
}

// overrunReason describes a body that carries more lines than its header
// declared -- the mirror of the truncation message, and the same class of fault:
// the header and the body disagree, so neither can be trusted.
func overrunReason(hdr hunkHeader) string {
	return fmt.Sprintf("hunk body has more lines than its header declares (%d before, %d after); the diff is malformed", hdr.oldCount, hdr.newCount)
}

type bodySide int

const (
	sideNone bodySide = iota
	sideOld
	sideNew
	sideBoth
)

// hunkHeader is the parsed "@@ -oldStart,oldCount +newStart,newCount @@".
type hunkHeader struct {
	oldStart, oldCount int
	newStart, newCount int
}

// parseHunkHeader re-reads a line isHunkHeader has already accepted, this time
// keeping the numbers. Kept separate from isHunkHeader so the sniffer (which
// only ever asks "is this edit-shaped?") stays allocation-free and cannot drift
// into deciding what a hunk means.
func parseHunkHeader(line string) (hunkHeader, bool) {
	rest, ok := strings.CutPrefix(line, "@@ ")
	if !ok {
		return hunkHeader{}, false
	}
	var h hunkHeader
	if rest, h.oldStart, h.oldCount, ok = cutSpan(rest, '-'); !ok {
		return hunkHeader{}, false
	}
	if rest, ok = strings.CutPrefix(rest, " "); !ok {
		return hunkHeader{}, false
	}
	if rest, h.newStart, h.newCount, ok = cutSpan(rest, '+'); !ok {
		return hunkHeader{}, false
	}
	if !strings.HasPrefix(rest, " @@") {
		return hunkHeader{}, false
	}
	return h, true
}

// cutSpan consumes one "<sign><start>[,<count>]" span, returning both numbers.
// An absent count means 1, which is what unified diff means by it.
func cutSpan(s string, sign byte) (rest string, start, count int, ok bool) {
	if len(s) == 0 || s[0] != sign {
		return s, 0, 0, false
	}
	s = s[1:]
	s, start, ok = cutInt(s)
	if !ok {
		return s, 0, 0, false
	}
	count = 1
	if after, found := strings.CutPrefix(s, ","); found {
		s, count, ok = cutInt(after)
		if !ok {
			return s, 0, 0, false
		}
	}
	return s, start, count, true
}

// cutInt consumes one run of ASCII digits, refusing anything that would
// overflow a sane line number rather than wrapping.
func cutInt(s string) (rest string, n int, ok bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		if n > maxDiffLines {
			return s, 0, false
		}
		n = n*10 + int(s[i]-'0')
		i++
	}
	if i == 0 {
		return s, 0, false
	}
	return s[i:], n, true
}

// diffHeaderPath trims the trailing timestamp git and diff(1) append after a
// tab, plus any surrounding whitespace.
func diffHeaderPath(raw string) string {
	if tab := strings.IndexByte(raw, '\t'); tab >= 0 {
		raw = raw[:tab]
	}
	return strings.TrimSpace(raw)
}

const devNull = "/dev/null"

// resolveDiffPaths turns a header pair into the one workspace-relative path an
// EditBlock can carry, or an error naming why the change cannot be expressed.
//
// The a/ and b/ prefixes are stripped exactly ONE component deep, and only when
// the pair actually looks like git's -p1 output. Stripping N components
// heuristically is how a patch reader ends up writing outside the tree; here
// the stripping is a spelling convention, and ResolveSafeTargetPath downstream
// remains the only authority on where a path may point.
func resolveDiffPaths(oldRaw, newRaw string) (path string, creates bool, err error) {
	if oldRaw == "" || newRaw == "" {
		return "", false, fmt.Errorf("file header names no path")
	}
	if newRaw == devNull {
		return "", false, fmt.Errorf("the diff deletes %s; this engine only writes bytes and has no delete step, and emptying the file instead would be a different change that `edits undo` could not tell apart from a real edit", strings.TrimPrefix(oldRaw, "a/"))
	}
	if oldRaw == devNull {
		created := stripDiffPrefix(newRaw, "b/")
		if err := RejectUnprintablePath(created); err != nil {
			return "", false, err
		}
		return created, true, nil
	}

	oldPath, newPath := oldRaw, newRaw
	if strings.HasPrefix(oldRaw, "a/") && strings.HasPrefix(newRaw, "b/") {
		oldPath = strings.TrimPrefix(oldRaw, "a/")
		newPath = strings.TrimPrefix(newRaw, "b/")
	}
	if oldPath != newPath {
		return "", false, fmt.Errorf("the diff renames %s to %s; this engine can only change a file's contents, never its name. Rename it yourself, then send a diff against the new path", oldPath, newPath)
	}
	if err := RejectUnprintablePath(newPath); err != nil {
		return "", false, err
	}
	return newPath, false, nil
}

func stripDiffPrefix(p, prefix string) string {
	if strings.HasPrefix(p, prefix) {
		return strings.TrimPrefix(p, prefix)
	}
	return p
}

// isExecutableModeLine matches the git mode lines that would make a file
// executable. "100644" lines are ignored: they name the default this engine
// already writes, so honouring them changes nothing.
func isExecutableModeLine(line string) bool {
	for _, prefix := range []string{"new file mode ", "new mode ", "old mode ", "index "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.Contains(rest, "100755")
		}
	}
	return false
}
