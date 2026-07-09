package editapply

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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
// It's (re)written after every write to a given path in this run, so by the
// time the run finishes it holds each file's final on-disk content — the
// baseline `edits undo` compares the file's current content against to
// detect whether it was touched again after this apply run.
func BackupAfter(backupDir, realWorkspaceRoot string, p *PreparedEdit) error {
	return writeBackupCopy(backupDir, "after", realWorkspaceRoot, p.TargetPath, []byte(p.NewContent), p.FileMode)
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
