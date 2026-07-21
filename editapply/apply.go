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
	Creates    bool   // true when this edit brings a new file into existence (see IsEmptySearch)
}

// syntaxNoteFor describes what syntax checking applies to relPath, given the
// content that would be written. Shared by the edit and create paths so a
// created .go file is reported the same way an edited one is.
func syntaxNoteFor(relPath, content string) string {
	if strings.EqualFold(filepath.Ext(relPath), ".go") {
		if _, err := parser.ParseFile(token.NewFileSet(), relPath, content, parser.AllErrors); err == nil {
			return "go/parser OK"
		}
		return "go/parser reported errors"
	}
	return fmt.Sprintf("no syntax check applied (unsupported for %s)", describeExt(relPath))
}

// PrepareEdit runs the safety tripod's path-safety and exact-match legs,
// plus the best-effort syntax gate, for one block. It performs no I/O beyond
// reading the target file — no prompting, no backup, no write. A non-nil
// error is always a refusal reason meant to be shown to the user verbatim,
// matching the parser's existing descriptive-error convention (never a bare
// system fault). This is the single core both the CLI (daemon/apply_cmd.go)
// and the Mochiii TUI (clients/tui) call — neither keeps its own copy.
func PrepareEdit(realWorkspaceRoot string, block EditBlock) (*PreparedEdit, error) {
	targetPath, exists, err := resolveSafeTarget(realWorkspaceRoot, block.FilePath)
	if err != nil {
		return nil, err
	}

	// An empty SEARCH section means "this file's content is, or should be,
	// nothing" — create it, or fill it if it is there and empty (Fix 7). See
	// IsEmptySearch for why this used to be three different behaviours.
	if IsEmptySearch(block.Search) {
		if exists {
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", block.FilePath, err)
			}
			if len(data) > 0 {
				return nil, fmt.Errorf("%s already exists and is not empty; an empty SEARCH section means \"create this file\", so refusing to replace its whole content — send a SEARCH section naming the text to replace", block.FilePath)
			}
		}
		return prepareCreate(block, targetPath, exists)
	}

	if !exists {
		return nil, fmt.Errorf("%s does not exist; to create it, send an edit block with an empty SEARCH section and the file's content as REPLACE", block.FilePath)
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

	if strings.EqualFold(filepath.Ext(block.FilePath), ".go") {
		if _, err := parser.ParseFile(token.NewFileSet(), block.FilePath, newContent, parser.AllErrors); err != nil {
			return nil, fmt.Errorf("edit would make %s unparseable as Go: %w", block.FilePath, err)
		}
	}
	syntaxNote := syntaxNoteFor(block.FilePath, newContent)

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
		// A creating edit expects exactly this: nothing there. Anything else
		// appearing in the meantime is the same staleness failure as a changed
		// file, so absence is only acceptable when absence is what was prepared.
		if os.IsNotExist(err) && prepared.Creates {
			return nil
		}
		return fmt.Errorf("re-reading %s before writing: %w", prepared.Block.FilePath, err)
	}
	if prepared.Creates {
		return fmt.Errorf("%s was created by something else since this edit was prepared; refusing to overwrite it", prepared.Block.FilePath)
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
//	BackupOriginal -> BackupAfter -> [record created] -> write
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
	// Record that this file did not exist before the run, so undo removes it
	// rather than restoring its 0-byte before/ snapshot (Fix C). Reversible and
	// pre-write for the same reason the after/ snapshot is: bookkeeping that
	// outlives a failed write would have undo delete a file this run never
	// wrote.
	rollbackCreated := func() {}
	if prepared.Creates {
		rollbackCreated, err = recordCreatedReversible(backupDir, realWorkspaceRoot, prepared)
		if err != nil {
			rollbackAfter()
			return fmt.Errorf("recording %s as newly created: %w", prepared.Block.FilePath, err)
		}
	}
	// A creating edit may name a directory that does not exist yet. This is the
	// one mutation that precedes the write, and deliberately the last thing
	// before it: an empty directory is not file content, and leaving one behind
	// if the write then fails costs nothing and loses nothing.
	if prepared.Creates {
		if err := os.MkdirAll(filepath.Dir(prepared.TargetPath), 0755); err != nil {
			rollbackCreated()
			rollbackAfter()
			return fmt.Errorf("creating parent directories for %s: %w", prepared.Block.FilePath, err)
		}
	}
	if err := os.WriteFile(prepared.TargetPath, []byte(prepared.NewContent), prepared.FileMode); err != nil {
		rollbackCreated()
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
	path, exists, err := resolveSafeTarget(realWorkspaceRoot, relPath)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("resolving %s: no such file", relPath)
	}
	return path, nil
}

// resolveSafeTarget is ResolveSafeTargetPath's implementation, reporting
// separately whether the target exists rather than treating absence as a
// failure (Fix 7). Every gate below applies identically to a path that is about
// to be CREATED — a model must not be able to create a git hook or a
// credentials file any more than it can overwrite one — so the only thing the
// two cases differ in is how the leaf is resolved.
func resolveSafeTarget(realWorkspaceRoot, relPath string) (path string, exists bool, err error) {
	if filepath.IsAbs(relPath) {
		return "", false, fmt.Errorf("path %q is absolute; edits must target workspace-relative paths", relPath)
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("path %q escapes the workspace root", relPath)
	}

	if component := ProtectedDirComponent(cleaned); component != "" {
		return "", false, refuseProtectedDir(relPath, component)
	}
	if MatchesSecretName(filepath.Base(cleaned)) {
		return "", false, fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	full := filepath.Join(realWorkspaceRoot, cleaned)
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("resolving %s: %w", relPath, err)
		}
		// Not there yet: confine via the deepest existing ancestor instead.
		newPath, nerr := resolveSafeNewPath(realWorkspaceRoot, cleaned)
		if nerr != nil {
			return "", false, nerr
		}
		return newPath, false, nil
	}

	rel, err := filepath.Rel(realWorkspaceRoot, realFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("path %q resolves outside the workspace root", relPath)
	}

	if component := ProtectedDirComponent(rel); component != "" {
		return "", false, refuseProtectedDir(relPath, component)
	}

	if MatchesSecretName(filepath.Base(realFull)) {
		return "", false, fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	return realFull, true, nil
}
