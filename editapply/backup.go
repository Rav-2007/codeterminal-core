package editapply

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// backupSessionsToKeep is the fixed number of most-recent backup session
// dirs retained per workspace; see pruneBackupSessions. Not configurable
// this slice.
const backupSessionsToKeep = 5

// NewBackupSessionDir creates a fresh, uniquely-named directory under
// .codeterminal/backups for one apply run (CLI or TUI). That parent
// directory is already covered by chunker.go's ignoredDirNames and the RAG
// .gitignore entry, so backups are automatically excluded from indexing and
// git.
//
// After creating the new dir, it prunes old session dirs down to the newest
// backupSessionsToKeep -- this is the ONE hook point for retention, so the
// CLI, TUI, and daemon (the three callers of this function) all get
// self-maintaining bounded backups for free without each needing its own
// prune call.
func NewBackupSessionDir(realWorkspaceRoot string) (string, error) {
	base := filepath.Join(realWorkspaceRoot, ".codeterminal", "backups")
	ts := time.Now().Format("20060102-150405")
	dir := filepath.Join(base, ts)
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", ts, suffix))
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	pruneBackupSessions(base, backupSessionsToKeep)
	return dir, nil
}

// pruneBackupSessions deletes all but the `keep` most-recent session dirs
// directly inside backupsRoot. Session dir names are zero-padded timestamps
// (see NewBackupSessionDir), so lexical sort == chronological order, and
// since this always runs immediately after a new dir is created, that new
// dir is always the lexically-newest entry and is therefore always kept --
// no self-pruning is possible.
//
// Every failure mode here is non-fatal and silent-but-logged: a workspace
// with a pruning problem (a stray non-dir file, a permission error, a dir
// already removed by a concurrent CLI/TUI/daemon prune against the same
// workspace) must never block the apply run that triggered it -- the
// actual edit and its own backup have already succeeded by the time this
// runs.
func pruneBackupSessions(backupsRoot string, keep int) {
	entries, err := os.ReadDir(backupsRoot)
	if err != nil {
		return // nothing to prune (or root doesn't exist yet) -- not fatal
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name()) // stray non-dir entries are skipped silently
		}
	}
	sort.Strings(names)
	if len(names) <= keep {
		return
	}

	for _, name := range names[:len(names)-keep] {
		candidate := filepath.Join(backupsRoot, name)
		// Defense-in-depth only: backupsRoot is always the locally-computed
		// <realWorkspaceRoot>/.codeterminal/backups (never client-supplied
		// input), and name always comes from os.ReadDir, which can never
		// return "." or ".." or anything containing a path separator -- so
		// this join cannot escape backupsRoot by construction. This
		// assertion is pure insurance against a future refactor loosening
		// that guarantee; it should never actually trigger. Deliberately
		// NOT reusing the daemon's isWorkspaceBackupSessionDir here -- that
		// function validates externally-supplied, potentially adversarial
		// paths (a different threat model), and editapply must not depend
		// on daemon.
		if filepath.Dir(candidate) != backupsRoot {
			log.Printf("editapply: refusing to prune %q (would escape backups root %q)", candidate, backupsRoot)
			continue
		}
		if err := os.RemoveAll(candidate); err != nil {
			log.Printf("editapply: pruning backup session %q: %v", candidate, err)
		}
	}
}

// BackupOriginal saves p's pre-edit content under backupDir/before/<relpath>
// — the copy `edits undo` restores from. It is idempotent per (backupDir,
// file): if that file's original has already been captured in this backup
// session (e.g. a second edit block in the same run touching the same
// file), later calls are no-ops. This guarantees before/<relpath> always
// holds the file's content from before ANY block in the run touched it,
// never an intermediate state from a prior block in the same run.
func BackupOriginal(backupDir, realWorkspaceRoot string, p *PreparedEdit) error {
	rel, err := filepath.Rel(realWorkspaceRoot, p.TargetPath)
	if err != nil {
		return err
	}
	dest := filepath.Join(backupDir, "before", rel)
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeBackupCopy(backupDir, "before", realWorkspaceRoot, p.TargetPath, []byte(p.Original), p.FileMode)
}

// BackupAfter records p's post-edit content under backupDir/after/<relpath>.
// It's (re)written for every write to a given path in this run, so by the
// time the run finishes it holds each file's final on-disk content — the
// baseline `edits undo` compares the file's current content against to
// detect whether it was touched again after this apply run.
//
// Apply calls backupAfterReversible instead, since it records the snapshot
// before the write it describes and must be able to take it back if that
// write fails. This exported entry point is the plain form, kept for callers
// that record a snapshot for a write that has already landed.
func BackupAfter(backupDir, realWorkspaceRoot string, p *PreparedEdit) error {
	return writeBackupCopy(backupDir, "after", realWorkspaceRoot, p.TargetPath, []byte(p.NewContent), p.FileMode)
}

// backupAfterReversible is BackupAfter plus an undo of itself. It captures
// whatever after/<relpath> held first, then overwrites it, and returns a
// rollback that puts the previous state back — the previous content for a file
// an earlier block in this run already applied, or removal of the file
// entirely when this is the first block to touch it.
//
// Rollback is best-effort by construction: it only ever restores bytes read
// moments earlier from the backup dir, and it runs on a path where the caller
// is already returning an error. A failure to roll back leaves the snapshot
// disagreeing with disk, which undo interprets conservatively (the file is
// reported guarded and left alone rather than silently overwritten), so there
// is no outcome here that can lose user data.
func backupAfterReversible(backupDir, realWorkspaceRoot string, p *PreparedEdit) (rollback func(), err error) {
	rel, err := filepath.Rel(realWorkspaceRoot, p.TargetPath)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(backupDir, "after", rel)

	previous, readErr := os.ReadFile(dest)
	switch {
	case readErr == nil:
		rollback = func() { os.WriteFile(dest, previous, p.FileMode) }
	case os.IsNotExist(readErr):
		rollback = func() { os.Remove(dest) }
	default:
		return nil, readErr
	}

	if err := BackupAfter(backupDir, realWorkspaceRoot, p); err != nil {
		return nil, err
	}
	return rollback, nil
}

// createdManifestName is the session-level record of which files the apply run
// brought into existence, as opposed to which it merely changed. It lives
// beside before/ and after/ rather than inside them, so it is never mistaken
// for a backed-up file by undo's walk of before/.
//
// The distinction cannot be recovered from the snapshots themselves, and that
// was the bug (Fix C). A created file's before/ snapshot is a 0-byte file,
// which is indistinguishable from the snapshot of a file that existed and was
// empty -- so undo restored the empty snapshot, reported "1 file restored", and
// left a 0-byte file standing where the correct answer was no file at all. The
// report disagreed with disk, which is the exact failure class Tier 1 set out
// to kill.
//
// Format is one workspace-relative path per line. Deliberately dumb: it is
// written by this package and consumed as a SET MEMBERSHIP TEST ONLY -- undo
// derives every path it acts on from its own walk of before/ and its own
// confinement check, and never from a line in this file. A corrupted or
// hand-edited manifest can therefore only cause a created file to be restored
// as an empty file (the old behaviour) and can never direct a delete at a path
// undo would not otherwise have been reverting.
const createdManifestName = "created-files"

// recordCreatedReversible notes that p's target did not exist before this run,
// and returns an undo of that note.
//
// It is reversible for the same reason backupAfterReversible is: it runs BEFORE
// the write it describes (Fix 1 ordering -- every fallible bookkeeping step
// happens while the workspace is still untouched), so a write that then fails
// must be able to take the claim back. A stale "this run created it" entry for
// a file the run never wrote would tell undo to delete a file this run is not
// responsible for.
func recordCreatedReversible(backupDir, realWorkspaceRoot string, p *PreparedEdit) (rollback func(), err error) {
	rel, err := filepath.Rel(realWorkspaceRoot, p.TargetPath)
	if err != nil {
		return nil, err
	}
	manifest := filepath.Join(backupDir, createdManifestName)

	previous, readErr := os.ReadFile(manifest)
	switch {
	case readErr == nil:
		rollback = func() { os.WriteFile(manifest, previous, 0644) }
	case os.IsNotExist(readErr):
		rollback = func() { os.Remove(manifest) }
	default:
		return nil, readErr
	}

	for _, line := range strings.Split(string(previous), "\n") {
		if line == rel {
			return func() {}, nil // already recorded by an earlier block in this run
		}
	}
	if err := os.WriteFile(manifest, append(previous, []byte(rel+"\n")...), 0644); err != nil {
		return nil, err
	}
	return rollback, nil
}

// CreatedInSession reports which workspace-relative paths the apply run that
// wrote sessionDir brought into existence. Undoing those means REMOVING them,
// not restoring their (empty) before/ snapshot.
//
// A session with no manifest -- one written before this record existed, or one
// in which nothing was created -- yields an empty set and no error, so undo
// falls back to restore-everything, which is exactly right for a run that
// created nothing.
func CreatedInSession(sessionDir string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, createdManifestName))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	created := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			created[line] = true
		}
	}
	return created, nil
}

func writeBackupCopy(backupDir, subdir, realWorkspaceRoot, targetPath string, content []byte, mode os.FileMode) error {
	rel, err := filepath.Rel(realWorkspaceRoot, targetPath)
	if err != nil {
		return err
	}
	dest := filepath.Join(backupDir, subdir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, mode)
}
