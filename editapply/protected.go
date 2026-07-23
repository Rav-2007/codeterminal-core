package editapply

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ProtectedDirNames are directory names an edit block may never write into, at
// any depth. The path-safety leg of the safety tripod used to gate on shape
// alone — absolute? ".."? escapes the root? secret basename? — which let model
// output write anywhere inside the workspace, including directories the indexer
// prunes outright and therefore never showed the model in the first place.
//
// These are not "noise" exclusions; every entry is state that is executed,
// trusted, or relied on for recovery:
//
//	.git, .hg, .svn, .bzr  VCS internals. Hooks are executable code the VCS runs
//	                       on the next commit, and config carries hooksPath and
//	                       filter/diff commands it also executes — a writable
//	                       .git/ is arbitrary code execution from model output.
//	.codeterminal          This product's own state: backups/ holds the pre-edit
//	                       copies undo restores from, so an edit that can rewrite
//	                       them defeats the safety net that makes every other
//	                       edit recoverable; logs/ is the operational record.
//	.ssh, .aws             Credential directories. MatchesSecretName already
//	                       covers the common key basenames, but that gate is
//	                       basename-only and cannot see the directory context.
//
// Deliberately NOT included: the indexer's build-output exclusions
// (node_modules, vendor, dist, build, target, out, .next, __pycache__, .venv,
// venv). Those are skipped as noise, not danger — editing vendored or generated
// code is unusual but legitimate, and refusing it would cost a real capability
// to prevent nothing. The indexer keeps pruning them; see chunker.go.
var ProtectedDirNames = map[string]bool{
	".git":          true,
	".hg":           true,
	".svn":          true,
	".bzr":          true,
	".codeterminal": true,
	".ssh":          true,
	".aws":          true,
}

// IsProtectedDirName reports whether name (a single path component) is a
// protected directory, compared CASE-INSENSITIVELY.
//
// The literal map lookup this centralizes (ProtectedDirNames[part]) was
// case-sensitive. On a case-insensitive filesystem (macOS default APFS, Windows
// NTFS) the OS resolves ".GIT" / ".Git" / ".SSH" to the same inode as the
// lowercase name, so a case-varied component sailed past the guard while still
// reaching the real directory — re-opening, for the edit WRITER, the exact
// ".git/hooks" arbitrary-code-execution path Tier 3 closed for the lowercase
// form, and, for the INDEXER, letting real VCS/credential internals be read into
// the index. All ProtectedDirNames keys are lowercase, so folding the input is
// symmetric. On a genuinely case-sensitive filesystem the fold is merely
// redundant: no legitimate source directory is a case-variant of .git/.ssh/etc.,
// so nothing that should be indexed or edited is newly refused.
func IsProtectedDirName(name string) bool {
	return ProtectedDirNames[strings.ToLower(name)]
}

// ProtectedDirComponent returns the first component of a workspace-relative
// path that names a protected directory, or "" if the path is clear. The path
// is checked component by component, so a protected directory is refused at any
// depth, and a file whose name merely resembles one (notgit/, gitignore.txt) is
// not. Matching is case-insensitive (see IsProtectedDirName), so ".GIT/hooks"
// is refused exactly like ".git/hooks".
func ProtectedDirComponent(relPath string) string {
	cleaned := filepath.Clean(relPath)
	if cleaned == "." {
		return ""
	}
	for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
		if IsProtectedDirName(part) {
			return part
		}
	}
	return ""
}

// refuseProtectedDir turns a protected-directory hit into the descriptive
// refusal the parser's error convention calls for — shown to the user verbatim,
// naming what was refused and why, never a bare system fault.
func refuseProtectedDir(relPath, component string) error {
	return fmt.Errorf("path %q is inside %s/, which holds version-control, credential, or undo state; refusing to edit it", relPath, component)
}
