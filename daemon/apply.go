package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// preparedEdit is one EditBlock after it has passed path-safety, exact-match,
// and (where applicable) syntax verification — everything needed to show a
// diff, ask for confirmation, and write it, with no further checks required.
type preparedEdit struct {
	block      EditBlock
	targetPath string // absolute, confinement-checked, resolved through symlinks
	original   string // full pre-edit file content
	newContent string // full post-edit file content
	startLine  int    // 1-indexed line where SEARCH begins in original
	endLine    int    // 1-indexed line where SEARCH ends in original
	fileMode   os.FileMode
	syntaxNote string // human-readable note on what syntax check ran (or didn't)
}

// prepareEdit runs the safety tripod's path-safety and exact-match legs,
// plus the best-effort syntax gate, for one block. It performs no I/O beyond
// reading the target file — no prompting, no backup, no write. A non-nil
// error is always a refusal reason meant to be shown to the user verbatim,
// matching the parser's existing descriptive-error convention (never a bare
// system fault).
func prepareEdit(realWorkspaceRoot string, block EditBlock) (*preparedEdit, error) {
	targetPath, err := resolveSafeTargetPath(realWorkspaceRoot, block.FilePath)
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

	return &preparedEdit{
		block:      block,
		targetPath: targetPath,
		original:   original,
		newContent: newContent,
		startLine:  startLine,
		endLine:    endLine,
		fileMode:   info.Mode(),
		syntaxNote: syntaxNote,
	}, nil
}

func describeExt(relPath string) string {
	if ext := filepath.Ext(relPath); ext != "" {
		return ext
	}
	return "files with no extension"
}

// resolveSafeTargetPath resolves relPath against realWorkspaceRoot (already
// itself resolved through symlinks) and refuses anything not confined to the
// workspace, mirroring ScanWorkspace's confinement approach in chunker.go:
// absolute paths and ".." components are rejected outright, and the fully
// resolved (symlinks-followed) path must still land inside
// realWorkspaceRoot. Files the indexer would secret-skip are refused too —
// an edit block is untrusted model output and must never rewrite
// credentials.
func resolveSafeTargetPath(realWorkspaceRoot, relPath string) (string, error) {
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

	if matchesSecretName(filepath.Base(realFull)) {
		return "", fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	return realFull, nil
}
