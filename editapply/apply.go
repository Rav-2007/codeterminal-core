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
	Tier       MatchTier
	MatchNote  string // human-readable note on what normalization the match needed; "" when exact
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

	// Locate the SEARCH text in the file AS IT IS NOW, walking the tolerance
	// ladder (Fix 6). This is a search problem, not a safety check: whether the
	// file has since changed is decided separately and byte-exactly, by
	// VerifyUnchanged at write time. Keeping those two apart is what lets the
	// matcher be forgiving about invisible differences without ever making the
	// staleness check forgiving about anything.
	match, err := findSearch(original, block.Search, block.FilePath)
	if err != nil {
		return nil, err
	}

	newContent := original[:match.Start] + block.Replace + original[match.End:]
	startLine := strings.Count(original[:match.Start], "\n") + 1
	endLine := startLine + strings.Count(original[match.Start:match.End], "\n")

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
		Tier:       match.Tier,
		MatchNote:  match.Tier.Note(),
	}, nil
}

// VerifyUnchanged is the content-addressed staleness check: it re-reads the
// target and requires it to be byte-for-byte what PrepareEdit read. A prepared
// edit describes a splice into one exact sequence of bytes, so applying it to
// anything else would clobber whatever arrived in between.
//
// This is deliberately a SEPARATE check from locating the SEARCH text, and
// deliberately byte-exact (Fix 6). The matcher normalizes line endings,
// whitespace, indentation and unicode in order to FIND the passage the model
// meant in the file as it is now. None of that tolerance may reach here: a file
// whose line endings were rewritten, whose indentation was retabbed, or whose
// text was renormalized to NFD since the edit was prepared HAS changed, and a
// stale splice into it must be refused even though the matcher would happily
// consider the two forms equivalent. The only comparison this function performs
// is string equality on raw bytes; it calls nothing from match.go.
//
// Before this existed, nothing re-checked the file between PrepareEdit and the
// write. The daemon holds its per-workspace lock across both, so this always
// passes there; the TUI's review flow puts a human confirmation in between, and
// that is the window this closes.
func VerifyUnchanged(prepared *PreparedEdit) error {
	current, err := os.ReadFile(prepared.TargetPath)
	if err != nil {
		return fmt.Errorf("re-reading %s before writing: %w", prepared.Block.FilePath, err)
	}
	if string(current) != prepared.Original {
		return fmt.Errorf("%s changed since this edit was prepared; refusing to apply a stale edit", prepared.Block.FilePath)
	}
	return nil
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
	// Byte-exact staleness check, before any bookkeeping: a file that changed
	// since the edit was prepared must not be spliced into. See VerifyUnchanged
	// for why this is separate from — and stricter than — the match ladder.
	if err := VerifyUnchanged(prepared); err != nil {
		return err
	}
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
//
// Confinement to the workspace is necessary but not sufficient (Fix 3): the
// indexer also prunes whole directories it will never show the model — VCS
// internals, this product's own backups and logs, credential dirs — and the
// writer must refuse to write into the same set, or model output can reach
// state that is executed (.git/hooks/*), trusted (.git/config), or relied on
// for recovery (.codeterminal/backups/.../before/*). See ProtectedDirNames.
//
// That check runs TWICE, deliberately. Once on the path as written, so the
// refusal is clear and reason-bearing whether or not the target exists; and
// once on the fully resolved path, so an in-tree symlink (innocent/ -> .git/)
// cannot launder the write. The resolved check is the load-bearing one; the
// first only improves the message.
func ResolveSafeTargetPath(realWorkspaceRoot, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q is absolute; edits must target workspace-relative paths", relPath)
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", relPath)
	}

	if component := ProtectedDirComponent(cleaned); component != "" {
		return "", refuseProtectedDir(relPath, component)
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

	if component := ProtectedDirComponent(rel); component != "" {
		return "", refuseProtectedDir(relPath, component)
	}

	if MatchesSecretName(filepath.Base(realFull)) {
		return "", fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	return realFull, nil
}
