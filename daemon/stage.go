package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
)

// THE WORKING COPY: an agent turn edits a private copy of the project, runs
// its tests there, and hands the human the net diff.
//
// Before this, propose_edit validated each edit against the REAL files and
// only recorded it; the real files changed after the turn, when the human
// pressed y. So within a turn the agent could not see its own work:
//
//   - a second edit that built on the first failed, because the first had not
//     happened;
//   - sandbox_exec ran the tests against the OLD code -- including the /team
//     Tester, which "validated" code the Coder had not touched;
//   - "write the test, run it, implement, run it again, fix" was impossible.
//
// Now the first propose_edit or sandbox_exec of a turn copies the project to a
// private directory, and from then on edits apply there, reads read there and
// commands run there. At the end the difference between the copy and the real
// project is turned back into ordinary edit blocks, and the human reviews them
// exactly as before. THE REAL PROJECT IS UNTOUCHED UNTIL THE HUMAN SAYS YES --
// more so than before, in fact: a command can no longer change it mid-turn.
//
// What the copy contains: every file the index would walk, minus .gitignored
// and protected directories (.git, .mochiii, ...). Dependency folders are
// LINKED, not copied (see stageLinkedDirs), so tests that need node_modules
// still find it and a large one costs nothing. Past stageMaxFiles or
// stageMaxBytes the copy is not made and the turn behaves as it always did.

// Vars so a test can make a small project "too large".
var (
	stageMaxFiles       = 50_000
	stageMaxBytes int64 = 500 << 20
)

// stageLinkedDirs are dependency folders a test run needs but a turn does not
// edit: symlinked into the copy, so they are READ from the real project and a
// write to them fails (bwrap binds / read-only; Landlock grants only the copy).
// Build-output folders (target, dist, build) are deliberately NOT here: a build
// recreates them, and a read-only one would make `cargo build` fail.
var stageLinkedDirs = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "vendor": true}

// errStageTooLarge is why a project is not copied.
var errStageTooLarge = errors.New("the project is too large to copy for this turn")

// stagedFile is what was true of one file when the copy was made.
type stagedFile struct {
	realSize  int64
	realMod   time.Time
	stageSize int64
	stageMod  time.Time
}

// stagedWorkspace is one turn's private copy of the project.
type stagedWorkspace struct {
	real, root string
	manifest   map[string]stagedFile // workspace-relative, native separators
	touched    map[string]bool       // files an edit tool changed this turn
	order      []string              // touched, in first-touch order
	backupDir  string
	// applied is every edit made in the copy, so an answer that restates one
	// word for word is recognised as the same edit (see absorbText).
	applied []editapply.EditBlock
	// checkpoints are the long task's named snapshots (checkpoint.go).
	checkpoints []*stageCheckpoint
	// byCommand is every file a COMMAND changed in the copy, and the first
	// command that did (noteCommandChanges). Kept because of what netChanges
	// offers: a file a command changed is offered like the agent's own edit.
	byCommand map[string]string
	// offeredByCommand is netChanges' account of it: the offered files a
	// command changed, each with its command, for WorkingCopyInfo.ByCommand.
	offeredByCommand []string
}

// stagedState is what fileStates records of one file: enough to tell that a
// command changed it, without reading it.
type stagedState struct {
	size int64
	mod  time.Time
	mode fs.FileMode
}

// fileStates is the state of every regular file in the copy that netChanges
// would consider, taken before a command runs and compared after it.
func (st *stagedWorkspace) fileStates() map[string]stagedState {
	states := map[string]stagedState{}
	_ = filepath.WalkDir(st.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == st.root {
			return nil //nolint:nilerr
		}
		if d.IsDir() {
			if editapply.IsProtectedDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			rel, _ := filepath.Rel(st.root, path)
			states[rel] = stagedState{size: info.Size(), mod: info.ModTime(), mode: info.Mode()}
		}
		return nil
	})
	return states
}

// noteCommandChanges records which files command changed, given the copy's
// state from just before it ran.
//
// WHY IT IS RECORDED. FOUND 2026-10-07 by running a hostile test file through
// sandbox_exec: the sandbox held (nothing outside the copy was read or
// written, on bwrap and on Landlock), and the one thing the test COULD do --
// append a line to main.go in the copy -- came back at the end of the turn as
// a proposed edit like any other, with nothing to say the agent had not
// written it. A person reviewing "the agent's changes" was reviewing a
// stranger's. The change is still offered, because `gofmt -w`, `go mod tidy`
// and a code generator are commands too; it is offered BY NAME.
func (st *stagedWorkspace) noteCommandChanges(before map[string]stagedState, command string) {
	if st.byCommand == nil {
		st.byCommand = map[string]string{}
	}
	for rel, now := range st.fileStates() {
		if was, existed := before[rel]; existed && was == now {
			continue
		}
		if _, noted := st.byCommand[rel]; !noted {
			st.byCommand[rel] = command
		}
	}
}

// offeredFrom notes that rel is being offered, when a command changed it.
func (st *stagedWorkspace) offeredFrom(rel string) {
	if command, ok := st.byCommand[rel]; ok {
		st.offeredByCommand = append(st.offeredByCommand, filepath.ToSlash(rel)+" ("+command+")")
	}
}

// sameEdit reports whether two edit blocks are the same edit to the same
// file, ignoring the whitespace a restatement tends to change.
func sameEdit(a, b editapply.EditBlock) bool {
	norm := func(s string) string {
		var lines []string
		for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		return strings.Join(lines, "\n")
	}
	return filepath.Clean(a.FilePath) == filepath.Clean(b.FilePath) &&
		norm(a.Search) == norm(b.Search) && norm(a.Replace) == norm(b.Replace)
}

// stageParentDir is where every copy lives: under the user cache dir, never in
// the project and never in /tmp (bwrap mounts its own /tmp over it).
func stageParentDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "mochiii", "stages"), nil
}

// newStagedWorkspace copies realRoot (already symlink-resolved) into a fresh
// private directory.
func newStagedWorkspace(realRoot string) (*stagedWorkspace, error) {
	parent, err := stageParentDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, protocol.WorkspaceTag(realRoot)+"-")
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	st := &stagedWorkspace{real: realRoot, root: root, manifest: map[string]stagedFile{}, touched: map[string]bool{}}
	if err := st.copyFrom(); err != nil {
		st.close()
		return nil, err
	}
	return st, nil
}

func (st *stagedWorkspace) copyFrom() error {
	ignore := newGitignoreMatcher(st.real)
	var files int
	var bytes int64
	return filepath.WalkDir(st.real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == st.real {
				return err
			}
			return nil //nolint:nilerr // an unreadable entry is left out, never fatal
		}
		if path == st.real {
			return nil
		}
		rel, err := filepath.Rel(st.real, path)
		if err != nil {
			return nil //nolint:nilerr
		}
		target := filepath.Join(st.root, rel)
		switch {
		case d.IsDir():
			name := d.Name()
			if editapply.IsProtectedDirName(name) {
				return fs.SkipDir
			}
			if stageLinkedDirs[name] {
				_ = os.Symlink(path, target)
				return fs.SkipDir
			}
			if ignore.matchDir(rel) {
				return fs.SkipDir
			}
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			if ignore.matchFile(rel) {
				return nil
			}
			if link, err := os.Readlink(path); err == nil {
				_ = os.Symlink(link, target)
			}
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		if ignore.matchFile(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		files++
		bytes += info.Size()
		if files > stageMaxFiles || bytes > stageMaxBytes {
			return errStageTooLarge
		}
		if err := copyRegularFile(path, target, info.Mode().Perm()); err != nil {
			return nil //nolint:nilerr // an unreadable file is left out, as the index leaves it out
		}
		copied, err := os.Stat(target)
		if err != nil {
			return nil //nolint:nilerr
		}
		st.manifest[rel] = stagedFile{realSize: info.Size(), realMod: info.ModTime(),
			stageSize: copied.Size(), stageMod: copied.ModTime()}
		return nil
	})
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// close removes the copy.
func (st *stagedWorkspace) close() {
	if st != nil && st.root != "" {
		_ = os.RemoveAll(st.root)
	}
}

// sweepStaleStages removes copies a crashed or killed daemon left behind for
// this workspace. One daemon serves one workspace, so at startup none of them
// can be in use.
func sweepStaleStages(realRoot string) {
	parent, err := stageParentDir()
	if err != nil {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(parent, protocol.WorkspaceTag(realRoot)+"-*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// relFor maps a path the model gave -- workspace-relative, or absolute into
// the real project or into this copy -- to a workspace-relative path. ok is
// false for anything else (an outside path).
func (st *stagedWorkspace) relFor(p string) (string, bool) {
	abs, isAbs := expandOutsidePath(p)
	if !isAbs {
		return p, true
	}
	for _, root := range []string{st.root, st.real} {
		if resolved := resolveOutsidePath(abs); isWithin(root, resolved) {
			if rel, err := filepath.Rel(root, resolved); err == nil {
				return rel, true
			}
		}
		if isWithin(root, abs) {
			if rel, err := filepath.Rel(root, abs); err == nil {
				return rel, true
			}
		}
	}
	return "", false
}

// apply makes one edit in the copy, through the same gates every edit goes
// through (PrepareEdit's confinement, match and syntax checks; Apply's atomic
// write), with the copy standing in for the project.
func (st *stagedWorkspace) apply(block editapply.EditBlock) (*editapply.PreparedEdit, error) {
	prepared, err := editapply.PrepareEdit(st.root, block)
	if err != nil {
		return nil, err
	}
	if st.backupDir == "" {
		dir, err := editapply.NewBackupSessionDir(st.root)
		if err != nil {
			return nil, err
		}
		st.backupDir = dir
	}
	if err := editapply.Apply(st.root, prepared, st.backupDir); err != nil {
		return nil, err
	}
	st.applied = append(st.applied, block)
	if rel, err := filepath.Rel(st.root, prepared.TargetPath); err == nil && !st.touched[rel] {
		st.touched[rel] = true
		st.order = append(st.order, rel)
	}
	return prepared, nil
}

// BY NAME, INSIDE THE COPY ONLY. A command runs in the copy and can leave links
// there -- a folder replaced by a link to ~/.kube, say -- and a path joined to
// st.root follows a link at ANY depth, out of the copy. os.Root refuses every
// path that escapes it. FOUND 2026-10-01: through a planted folder link a
// checkpoint read a file outside the copy (and a restore brought its content
// in, where read_file showed it), a restore DELETED one, and a spec tick wrote
// one. Every by-name read, write and removal in the copy goes through these.

// readCopyFile reads one regular file of the copy, refusing one larger than max
// (0: no bound). exists is false when there is no such file.
func (st *stagedWorkspace) readCopyFile(rel string, max int64) (data []byte, exists bool, err error) {
	root, err := os.OpenRoot(st.root)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }() // read-only
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is no longer a regular file in the working copy", filepath.ToSlash(rel))
	}
	if max > 0 && info.Size() > max {
		return nil, false, fmt.Errorf("%s is larger than %d bytes", filepath.ToSlash(rel), max)
	}
	data, err = root.ReadFile(rel)
	return data, err == nil, err
}

// writeCopyFile replaces one file of the copy.
func (st *stagedWorkspace) writeCopyFile(rel string, data []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(st.root)
	if err != nil {
		return err
	}
	if err := root.WriteFile(rel, data, perm); err != nil {
		_ = root.Close() // the write's error is the one to report
		return err
	}
	return root.Close()
}

// removeCopyFile removes one file of the copy.
func (st *stagedWorkspace) removeCopyFile(rel string) error {
	root, err := os.OpenRoot(st.root)
	if err != nil {
		return err
	}
	if err := root.Remove(rel); err != nil {
		_ = root.Close() // the removal's error is the one to report
		return err
	}
	return root.Close()
}

// toReal rewrites the copy's location as the project's in text a command
// printed, so neither the model nor the user ever sees the private path: a
// stack trace naming it would send the model off to read a file "outside the
// project".
func (st *stagedWorkspace) toReal(text string) string {
	return strings.ReplaceAll(text, st.root, st.real)
}

// stageMaxNotes bounds how many not-offered paths are listed.
const stageMaxNotes = 20

// netChanges turns the copy's difference from the project back into edit
// blocks for review, and says what it is not offering and why.
//
// OFFERED: every file an edit tool changed or created, and every file that was
// in the project and a command changed. NOT OFFERED, and said: a file a command
// created (build output); a file that no longer exists in the copy (deleting
// is not something an edit can do); a file that changed on disk while the
// agent worked (its version would silently undo the user's).
func (st *stagedWorkspace) netChanges() (blocks []editapply.EditBlock, notOffered []string) {
	st.offeredByCommand = nil
	var paths []string
	_ = filepath.WalkDir(st.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == st.root {
			return nil //nolint:nilerr
		}
		rel, _ := filepath.Rel(st.root, path)
		if d.IsDir() {
			if editapply.IsProtectedDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			paths = append(paths, rel)
		}
		return nil
	})
	sort.Strings(paths)

	note := func(s string) {
		if len(notOffered) < stageMaxNotes {
			notOffered = append(notOffered, s)
		}
	}
	present := map[string]bool{}
	for _, rel := range paths {
		present[rel] = true
		slash := filepath.ToSlash(rel)
		info, err := os.Stat(filepath.Join(st.root, rel))
		if err != nil {
			continue
		}
		was, known := st.manifest[rel]
		if !known {
			if !st.touched[rel] {
				note(slash + ": created by a command, not offered")
				continue
			}
			content, err := os.ReadFile(filepath.Join(st.root, rel))
			if err != nil {
				continue
			}
			if _, err := os.Lstat(filepath.Join(st.real, rel)); err == nil {
				note(slash + ": created in the copy, but a file with that name appeared in the project meanwhile; not offered")
				continue
			}
			blocks = append(blocks, editapply.EditBlock{FilePath: slash, Search: "", Replace: string(content)})
			st.offeredFrom(rel)
			continue
		}
		if !st.touched[rel] && info.Size() == was.stageSize && info.ModTime().Equal(was.stageMod) {
			continue
		}
		after, err := os.ReadFile(filepath.Join(st.root, rel))
		if err != nil {
			continue
		}
		realInfo, err := os.Stat(filepath.Join(st.real, rel))
		if err != nil || realInfo.Size() != was.realSize || !realInfo.ModTime().Equal(was.realMod) {
			if before, rerr := os.ReadFile(filepath.Join(st.real, rel)); rerr != nil || string(before) != string(after) {
				note(slash + ": changed on disk while the agent worked, so its version is not offered (it would undo yours)")
			}
			continue
		}
		before, err := os.ReadFile(filepath.Join(st.real, rel))
		if err != nil || string(before) == string(after) {
			continue
		}
		if strings.ContainsRune(string(after), 0) || strings.ContainsRune(string(before), 0) {
			note(slash + ": a binary file changed in the copy; not offered")
			continue
		}
		blocks = append(blocks, stagedEditBlocks(slash, string(before), string(after))...)
		st.offeredFrom(rel)
	}
	for rel := range st.manifest {
		if !present[rel] {
			note(filepath.ToSlash(rel) + ": deleted in the copy; deleting is not offered")
		}
	}
	sort.Strings(notOffered)
	return blocks, notOffered
}

// ---------------------------------------------------------------------------
// Turning a before/after pair into reviewable edit blocks.
// ---------------------------------------------------------------------------

// stageDiffContext is how many unchanged lines each block carries around its
// change, before widening for uniqueness.
const stageDiffContext = 2

// stageDiffMaxCells bounds the line-LCS table. Past it, a file's change is
// offered as one block spanning the first to the last changed line.
const stageDiffMaxCells = 4_000_000

// stagedEditBlocks returns SEARCH/REPLACE blocks that turn before into after
// when applied in order, each SEARCH matching exactly once at its turn -- which
// is what the review applies. Small blocks where possible, so the review shows
// the change and not the file; verified by replaying them, and when a replay
// would not reproduce after exactly, one block for the whole file instead.
func stagedEditBlocks(path, before, after string) []editapply.EditBlock {
	whole := []editapply.EditBlock{{FilePath: path, Search: before, Replace: after}}
	if before == "" {
		return whole
	}
	a, b := splitLinesKeep(before), splitLinesKeep(after)
	hunks := lineHunks(a, b)
	if len(hunks) == 0 {
		return nil
	}
	var blocks []editapply.EditBlock
	for i, h := range hunks {
		lo, hi := h.a0, h.a1
		minLo, maxHi := 0, len(a)
		if i > 0 {
			minLo = hunks[i-1].a1
		}
		if i+1 < len(hunks) {
			maxHi = hunks[i+1].a0
		}
		lo, hi = max(minLo, lo-stageDiffContext), min(maxHi, hi+stageDiffContext)
		for {
			search := strings.Join(a[lo:hi], "")
			if search != "" && strings.Count(before, search) == 1 {
				break
			}
			if lo == minLo && hi == maxHi {
				return whole
			}
			lo, hi = max(minLo, lo-1), min(maxHi, hi+1)
		}
		replace := strings.Join(a[lo:h.a0], "") + strings.Join(b[h.b0:h.b1], "") + strings.Join(a[h.a1:hi], "")
		blocks = append(blocks, editapply.EditBlock{FilePath: path, Search: strings.Join(a[lo:hi], ""), Replace: replace})
	}
	cur := before
	for _, bl := range blocks {
		if strings.Count(cur, bl.Search) != 1 {
			return whole
		}
		cur = strings.Replace(cur, bl.Search, bl.Replace, 1)
	}
	if cur != after {
		return whole
	}
	return blocks
}

// lineHunk is one changed region: a[a0:a1] becomes b[b0:b1].
type lineHunk struct{ a0, a1, b0, b1 int }

// splitLinesKeep splits s into lines that keep their "\n".
func splitLinesKeep(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lineHunks is a line diff: common prefix and suffix trimmed, then a longest
// common subsequence over the middle. Hunks closer than twice the context are
// merged, so no two blocks' context overlaps.
func lineHunks(a, b []string) []lineHunk {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	q := 0
	for q < len(a)-p && q < len(b)-p && a[len(a)-1-q] == b[len(b)-1-q] {
		q++
	}
	am, bm := a[p:len(a)-q], b[p:len(b)-q]
	if len(am) == 0 && len(bm) == 0 {
		return nil
	}
	if len(am)*len(bm) > stageDiffMaxCells || len(am) == 0 || len(bm) == 0 {
		return []lineHunk{{p, len(a) - q, p, len(b) - q}}
	}
	// lcs[i][j] = LCS length of am[i:], bm[j:].
	n, m := len(am), len(bm)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var hunks []lineHunk
	var cur *lineHunk
	i, j := 0, 0
	open := func() {
		if cur == nil {
			cur = &lineHunk{p + i, p + i, p + j, p + j}
		}
	}
	shut := func() {
		if cur != nil {
			cur.a1, cur.b1 = p+i, p+j
			hunks = append(hunks, *cur)
			cur = nil
		}
	}
	for i < n || j < m {
		switch {
		case i < n && j < m && am[i] == bm[j]:
			shut()
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			open()
			j++
		default:
			open()
			i++
		}
	}
	shut()

	merged := hunks[:0]
	for _, h := range hunks {
		if len(merged) > 0 && h.a0-merged[len(merged)-1].a1 <= 2*stageDiffContext {
			last := &merged[len(merged)-1]
			last.a1, last.b1 = h.a1, h.b1
			continue
		}
		merged = append(merged, h)
	}
	return merged
}

// workingCopySource is the project a turn in this mode copies, or "" for a
// turn with no working copy: plan and spec modes, which run nothing (spec mode
// writes one new Markdown file, reviewed as a proposal), and a config that
// switched it off.
func (s *Server) workingCopySource(mode string) string {
	if isPlanMode(mode) || isSpecMode(mode) || s.cfg == nil || s.cfg.MCP.NoWorkingCopy {
		return ""
	}
	root, err := s.realWorkspaceRoot()
	if err != nil {
		return ""
	}
	return root
}

// newTurnSink makes one agent turn's proposal sink and returns the messages to
// send with it. THE WORKING COPY (see above): edits land in a private copy the
// agent can read back and test, and the user reviews the net diff at the end.
// Shared by runAgentTurn and the task-success eval, so the eval measures the
// turn the product runs.
//
// spec is the turn's active spec, or nil. A /spec turn may write only under
// specs/; a /spec check turn grades against spec and keeps nothing it changed.
func (s *Server) newTurnSink(mode string, spec *activeSpec, messages []chatMessage) (*proposalSink, []chatMessage) {
	sink := &proposalSink{
		stageFrom: s.workingCopySource(mode),
		spec:      spec,
		specOnly:  isSpecMode(mode),
		checking:  isCheckMode(mode),
		building:  isBuildMode(mode),
	}
	// Not in a check: it edits nothing, and its own directive already says its
	// commands run in a throwaway copy.
	if sink.stageFrom != "" && !sink.checking {
		messages = withSystemNote(messages, workingCopyDirective)
	}
	return sink, messages
}

// workingCopyDirective tells the model what the working copy means for how it
// should work. In the SYSTEM message, for the reasons planModeDirective gives,
// and only on a turn that has a working copy -- the sentence is false on any
// other.
const workingCopyDirective = "YOUR EDITS TAKE EFFECT IN A WORKING COPY. propose_edit applies each edit " +
	"at once to a private copy of the project for this turn: read_file shows your changes and " +
	"sandbox_exec builds and tests them, while the user's real files stay untouched until they review " +
	"all of your changes when you finish. So work the way an engineer does: after changing code, " +
	"build it or run the relevant tests with sandbox_exec, read what they say, and fix what fails " +
	"before you finish. When you finish, say what you ran and how it went -- and if you could not " +
	"run anything, say that instead of implying the change was tested."

// withSystemNote returns messages with note appended to the system message.
// A copy: the caller's slice is reused across phases, and text added to it in
// place would accumulate.
func withSystemNote(messages []chatMessage, note string) []chatMessage {
	if len(messages) == 0 || messages[0].Role != "system" {
		return messages
	}
	out := make([]chatMessage, len(messages))
	copy(out, messages)
	out[0].Content += "\n\n" + note
	return out
}

// describeStageRefusal is the degradation a user sees when the copy could not
// be made and the turn fell back to proposing against the real project.
func describeStageRefusal(err error) protocol.Degradation {
	detail := fmt.Sprintf("this turn could not make its private working copy of the project (%v), so "+
		"its edits are proposals the agent cannot test before you review them", err)
	if errors.Is(err, errStageTooLarge) {
		detail = fmt.Sprintf("this project is larger than %d files or %d MB, so this turn works without "+
			"a private working copy: its edits are proposals it cannot test before you review them",
			stageMaxFiles, stageMaxBytes>>20)
	}
	return protocol.Degradation{Component: protocol.DegradedWorkingCopy, Detail: detail}
}
