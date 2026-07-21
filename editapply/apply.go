package editapply

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// PreparedEdit is one EditBlock after it has passed path-safety, exact-match,
// and (where applicable) syntax verification — everything needed to show a
// diff, ask for confirmation, and write it, with no further checks required.
type PreparedEdit struct {
	Block      EditBlock
	TargetPath string // absolute, confinement-checked, resolved through symlinks
	Original   string // full pre-edit file content
	NewContent string // full post-edit file content
	StartLine  int    // 1-indexed line where SEARCH begins in original
	EndLine    int    // 1-indexed line where SEARCH ends in original
	FileMode   os.FileMode
	SyntaxNote string // human-readable note on what syntax check ran (or didn't)
}

// PrepareEdit runs the safety tripod's path-safety and exact-match legs,
// plus the best-effort syntax gate, for one block. It performs no I/O beyond
// reading the target file — no prompting, no backup, no write. A non-nil
// error is always a refusal reason meant to be shown to the user verbatim,
// matching the parser's existing descriptive-error convention (never a bare
// system fault). This is the single core both the CLI (daemon/apply_cmd.go)
// and the Mochiii TUI (clients/tui) call — neither keeps its own copy.
func PrepareEdit(realWorkspaceRoot string, block EditBlock) (*PreparedEdit, error) {
	targetPath, err := ResolveSafeTargetPath(realWorkspaceRoot, block.FilePath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", block.FilePath, err)
	}
	original := string(data)

	n := strings.Count(original, block.Search)
	if n == 0 {
		return nil, fmt.Errorf("search text not found in %s", block.FilePath)
	}
	if n > 1 {
		return nil, fmt.Errorf("search text found %d times in %s; ambiguous, refusing", n, block.FilePath)
	}

	idx := strings.Index(original, block.Search)
	newContent := original[:idx] + block.Replace + original[idx+len(block.Search):]
	startLine := strings.Count(original[:idx], "\n") + 1
	endLine := startLine + strings.Count(block.Search, "\n")

	syntaxNote := fmt.Sprintf("no syntax check applied (unsupported for %s)", describeExt(block.FilePath))
	if strings.EqualFold(filepath.Ext(block.FilePath), ".go") {
		if _, err := parser.ParseFile(token.NewFileSet(), block.FilePath, newContent, parser.AllErrors); err != nil {
			return nil, fmt.Errorf("edit would make %s unparseable as Go: %w", block.FilePath, err)
		}
		syntaxNote = "go/parser OK"
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", block.FilePath, err)
	}

	return &PreparedEdit{
		Block:      block,
		TargetPath: targetPath,
		Original:   original,
		NewContent: newContent,
		StartLine:  startLine,
		EndLine:    endLine,
		FileMode:   info.Mode(),
		SyntaxNote: syntaxNote,
	}, nil
}

// Apply writes a prepared edit to disk and records its before/after backup
// snapshots. backupDir must already exist (see NewBackupSessionDir) -- Apply
// does not create it, since a caller applying multiple blocks in one run
// creates it once and reuses it across calls. BackupOriginal is idempotent per
// (backupDir, file), so calling Apply for two blocks that target the same file
// within one backupDir still captures the true pre-run original exactly once,
// regardless of how many blocks touch that file. This is the single core both
// the CLI (daemon/apply_cmd.go) and the Mochiii TUI (clients/tui/chat.go) use;
// neither keeps its own copy of the write+backup sequence.
//
// ORDERING IS LOAD-BEARING (Fix 1). Every fallible bookkeeping step runs while
// the workspace is still untouched, and the file write is the single, last act:
//
//	BackupOriginal -> BackupAfter -> write
//
// The order used to be BackupOriginal -> write -> BackupAfter, which meant a
// failure recording the post-apply snapshot (a full disk, a permission
// problem) returned an error to a caller whose file had ALREADY been mutated.
// The daemon reported applied:false, the TUI reported nothing applied, and
// undo then refused to revert the change because no after/ snapshot existed to
// match the file against -- an unreported, unrevertable mutation. Recording
// the snapshot first is a pure reorder: NewContent is fully determined by
// PrepareEdit, long before any byte is written.
//
// A caller must be able to trust the converse too: after/<file> is the
// baseline undo compares the file against, so it must never advertise content
// that is not on disk. If the write itself fails, the snapshot is rolled back
// to whatever it held before this call (the previous block's content in a
// multi-block run, or absent entirely).
func Apply(realWorkspaceRoot string, prepared *PreparedEdit, backupDir string) error {
	if err := BackupOriginal(backupDir, realWorkspaceRoot, prepared); err != nil {
		return fmt.Errorf("backing up %s: %w", prepared.Block.FilePath, err)
	}
	rollbackAfter, err := backupAfterReversible(backupDir, realWorkspaceRoot, prepared)
	if err != nil {
		return fmt.Errorf("recording post-apply snapshot for %s: %w", prepared.Block.FilePath, err)
	}
	if err := os.WriteFile(prepared.TargetPath, []byte(prepared.NewContent), prepared.FileMode); err != nil {
		rollbackAfter()
		return fmt.Errorf("writing %s: %w", prepared.Block.FilePath, err)
	}
	return nil
}

func describeExt(relPath string) string {
	if ext := filepath.Ext(relPath); ext != "" {
		return ext
	}
	return "files with no extension"
}

// ResolveSafeTargetPath resolves relPath against realWorkspaceRoot (already
// itself resolved through symlinks) and refuses anything not confined to the
// workspace, mirroring the indexer's ScanWorkspace confinement approach:
// absolute paths and ".." components are rejected outright, and the fully
// resolved (symlinks-followed) path must still land inside
// realWorkspaceRoot. Files the indexer would secret-skip are refused too —
// an edit block is untrusted model output and must never rewrite
// credentials.
func ResolveSafeTargetPath(realWorkspaceRoot, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q is absolute; edits must target workspace-relative paths", relPath)
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", relPath)
	}

	full := filepath.Join(realWorkspaceRoot, cleaned)
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", relPath, err)
	}

	rel, err := filepath.Rel(realWorkspaceRoot, realFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace root", relPath)
	}

	if MatchesSecretName(filepath.Base(realFull)) {
		return "", fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	return realFull, nil
}
