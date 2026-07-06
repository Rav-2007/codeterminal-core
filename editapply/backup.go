package editapply

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// NewBackupSessionDir creates a fresh, uniquely-named directory under
// .codeterminal/backups for one apply run (CLI or TUI). That parent
// directory is already covered by chunker.go's ignoredDirNames and the RAG
// .gitignore entry, so backups are automatically excluded from indexing and
// git.
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
	return dir, nil
}

// BackupOriginal saves p's pre-edit content under backupDir/before/<relpath>
// — the copy `edits undo` restores from. Called once per file per run, on
// its first write.
func BackupOriginal(backupDir, realWorkspaceRoot string, p *PreparedEdit) error {
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
