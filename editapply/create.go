package editapply

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// newFileMode is the permission a created file gets. There is no existing file
// to inherit from, and an edit block is untrusted model output, so this is a
// fixed, unremarkable default rather than anything the block can influence —
// in particular nothing here can produce an executable file.
const newFileMode os.FileMode = 0644

// IsEmptySearch reports whether a block's SEARCH section is empty, which is the
// marker for "this file's content is, or should be, nothing".
//
// Empty SEARCH used to mean two different things by accident (the A8 finding):
// on an EMPTY file, byte-exact matching found the empty string once and
// silently inserted; on a NON-EMPTY file it found it everywhere and refused as
// ambiguous; and on a path that did not exist yet the whole thing died in
// EvalSymlinks with "lstat ...: no such file or directory", an internal fault
// shown to the user as if it were a refusal reason. None of that was designed.
//
// It now means exactly one thing, in all three cases: write REPLACE as the
// file's entire content, creating the file if it is not there. That is a
// refusal when the file exists and already has content, since overwriting it
// wholesale is never what an edit block should be able to ask for by omission.
//
// Whitespace-only counts as empty: a model that emitted a stray blank line for
// its SEARCH section meant the same thing.
func IsEmptySearch(search string) bool {
	return strings.TrimSpace(search) == ""
}

// resolveSafeNewPath is ResolveSafeTargetPath's counterpart for a leaf that
// does not exist yet. EvalSymlinks cannot be used on the full path — it fails
// on the missing component, which is exactly the bug that made file creation
// impossible — so this resolves the deepest EXISTING ancestor, requires that to
// land inside the workspace root, and re-joins the not-yet-existing remainder.
//
// This mirrors the FAIL-2 pattern in daemon/apply_cmd.go's confinedRestorePath,
// which solved the same problem for restoring a deleted file. The components
// below the resolved ancestor do not exist AT PREPARE TIME, so they cannot be
// pre-planted symlinks then; the intermediate ones are created as real
// directories by the writer. The LEAF is the exception: it can be swapped for a
// symlink between prepare and write (the TUI's human-confirm window), which is
// why the write itself no longer trusts resolution alone — Apply commits through
// writeFileAtomicNoFollow, which refuses a symlinked leaf and commits by an
// atomic rename that replaces a link rather than following it (C2).
func resolveSafeNewPath(realWorkspaceRoot, cleaned string) (string, error) {
	full := filepath.Join(realWorkspaceRoot, cleaned)

	ancestor := full
	var suffix []string
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("path %q has no existing ancestor within the workspace root", cleaned)
		}
		suffix = append([]string{filepath.Base(ancestor)}, suffix...)
		ancestor = parent
	}

	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", cleaned, err)
	}
	rel, err := filepath.Rel(realWorkspaceRoot, realAncestor)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace root", cleaned)
	}
	// The resolved ancestor is re-checked against the protected set: a symlinked
	// directory inside the tree must not become a way to CREATE a git hook any
	// more than it is a way to overwrite one (Fix 3).
	if component := ProtectedDirComponent(rel); component != "" {
		return "", refuseProtectedDir(cleaned, component)
	}

	return filepath.Join(append([]string{realAncestor}, suffix...)...), nil
}

// prepareCreate builds the PreparedEdit for "write REPLACE as this file's whole
// content". targetPath is already confinement-checked; exists says whether
// there is a file there now (an existing but empty file takes this same path,
// which is the point — one meaning, not two).
func prepareCreate(block EditBlock, targetPath string, exists bool) (*PreparedEdit, error) {
	// The same gate the edit path applies, for the same reason and with the
	// same wording (M7). Creating a .go file that does not parse used to be
	// merely annotated while editing one into that state was refused, so a
	// model whose edit was rejected could land the identical bytes by sending
	// them with an empty SEARCH section instead.
	if err := refuseIfUnparseable(block.FilePath, block.Replace); err != nil {
		return nil, err
	}

	mode := newFileMode
	if exists {
		info, err := os.Stat(targetPath)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", block.FilePath, err)
		}
		mode = info.Mode()
	}

	return &PreparedEdit{
		Block:      block,
		TargetPath: targetPath,
		Original:   "",
		NewContent: block.Replace,
		StartLine:  1,
		EndLine:    1,
		FileMode:   mode,
		SyntaxNote: syntaxNoteFor(block.FilePath, block.Replace),
		Tier:       MatchExact,
		Creates:    !exists,
	}, nil
}
