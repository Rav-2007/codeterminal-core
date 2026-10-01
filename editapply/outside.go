package editapply

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WRITING OUTSIDE THE WORKSPACE -- INSIDE THE USER'S HOME, AFTER REVIEW.
//
// "Create a folder Go_chii on my Desktop" could not be done: every edit was
// confined to the workspace root. An edit may now name an absolute or ~/ path
// in the user's home folder, and it is handled by the SAME pipeline as any
// other edit, with the home folder standing in for the workspace root:
//
//   - PrepareEdit's confinement, symlink resolution, protected-directory and
//     secret-name refusals all apply unchanged, relative to home. Nothing
//     outside home is reachable at all.
//   - On top of that, outsideWriteRefusal refuses every place where a write
//     would make something RUN later, or reach a credential: any hidden
//     component (~/.bashrc, ~/.profile, ~/.config/autostart, ~/.local/bin,
//     ~/.ssh, a project's .git/hooks or .github/workflows), ~/bin (on PATH on
//     most distributions), ~/Library (macOS login items) and ~/AppData (the
//     Windows Startup folder), and *.desktop launchers. Checked on the path as
//     written AND on the resolved path, so a symlink cannot launder it.
//   - Nothing is written until the user approves the diff -- the same review
//     every edit already goes through. Callers show OutsideRoot-bearing edits
//     with a banner.
//   - Apply locks on the PROJECT's lock (the home folder's would live in
//     ~/.mochiii, beside the stored API key) and backs the file up into the
//     project's undo session under OutsideBackupSubdir, so `edits undo`
//     restores it with everything else from that run.
//
// Commands (sandbox_exec) are NOT widened by any of this.

// OutsideBackupSubdir is where an outside edit's backups live inside a backup
// session: <session>/home/{before,after,...}, relative to the home folder.
const OutsideBackupSubdir = "home"

// errOutsideHome is returned for an absolute path outside the home folder.
var errOutsideHome = errors.New("Mochiii only writes inside your home folder")

// RealHomeDir is the user's home folder, symlinks resolved, so it compares
// byte-for-byte with resolved target paths.
func RealHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("finding your home folder: %v", err)
	}
	return ResolveRealWorkspaceRoot(home)
}

// expandUserPath turns "~", "~/x" or "/abs" into an absolute path and reports
// true; a relative path reports false (it means the workspace, as always).
func expandUserPath(p, home string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~")), true
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), true
	}
	return "", false
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// outsideWriteRefusal returns why a home-relative path may never be written,
// or "" when it may (after review).
func outsideWriteRefusal(relToHome string) string {
	parts := SplitComponents(filepath.Clean(relToHome))
	for i, part := range parts {
		if strings.HasPrefix(part, ".") {
			return fmt.Sprintf("%q is a hidden file or folder; Mochiii never writes those outside the project "+
				"(they hold settings, credentials and start-up scripts)", part)
		}
		if i == 0 {
			switch NormalizeComponent(part) {
			case "bin":
				return "~/bin holds programs on your PATH; Mochiii never writes there"
			// The other platforms' start-up places, which hide behind no dot.
			// FOUND 2026-10-01: a login item in ~/Library/LaunchAgents (macOS)
			// or the Startup folder under ~/AppData (Windows) passed.
			case "library":
				return "~/Library holds app settings and login items; Mochiii never writes there"
			case "appdata":
				return "~/AppData holds app settings and the Startup folder; Mochiii never writes there"
			}
		}
	}
	if strings.EqualFold(filepath.Ext(relToHome), ".desktop") {
		return "a .desktop file is an app launcher; Mochiii never writes those outside the project"
	}
	return ""
}

// PrepareEditAnywhere is PrepareEdit for an edit whose path may point outside
// the workspace. A relative path, or an absolute one inside the workspace, is
// prepared against the workspace exactly as PrepareEdit does. An absolute or
// ~/ path in the home folder is prepared against the home folder, refused per
// outsideWriteRefusal, and returned with OutsideRoot set and Block.FilePath
// shown as "~/...". Anything else is refused.
func PrepareEditAnywhere(realWorkspaceRoot string, block EditBlock) (*PreparedEdit, error) {
	home, err := RealHomeDir()
	if err != nil {
		return nil, err
	}
	abs, isAbs := expandUserPath(block.FilePath, home)
	if !isAbs {
		return PrepareEdit(realWorkspaceRoot, block)
	}
	resolvedAbs := resolveExistingPrefix(abs)
	if within(realWorkspaceRoot, resolvedAbs) {
		rel, err := filepath.Rel(realWorkspaceRoot, resolvedAbs)
		if err != nil {
			return nil, err
		}
		inside := block
		inside.FilePath = rel
		return PrepareEdit(realWorkspaceRoot, inside)
	}
	if !within(home, abs) || !within(home, resolvedAbs) {
		return nil, fmt.Errorf("%s: %w", block.FilePath, errOutsideHome)
	}

	rel, err := filepath.Rel(home, abs)
	if err != nil {
		return nil, err
	}
	if why := outsideWriteRefusal(rel); why != "" {
		return nil, fmt.Errorf("%s: %s", block.FilePath, why)
	}

	outside := block
	outside.FilePath = rel
	prepared, err := PrepareEdit(home, outside)
	if err != nil {
		// PrepareEdit names the path relative to home ("Desktop/x.go"), which
		// reads as a PROJECT path -- to the model most of all. Say which file.
		return nil, fmt.Errorf("~%c%s: %w", filepath.Separator, rel, err)
	}
	// THE RESOLVED PATH IS WHAT GETS WRITTEN, so it is judged again: a
	// harmless-looking ~/Desktop/notes that is a symlink to ~/.bashrc is
	// refused here, and one that leads back into the project is sent there.
	resolvedRel, err := filepath.Rel(home, prepared.TargetPath)
	if err != nil {
		return nil, err
	}
	if why := outsideWriteRefusal(resolvedRel); why != "" {
		return nil, fmt.Errorf("%s: %s", block.FilePath, why)
	}
	if within(realWorkspaceRoot, prepared.TargetPath) {
		return nil, fmt.Errorf("%s leads into this project; name it by its path in the project instead", block.FilePath)
	}
	prepared.OutsideRoot = home
	prepared.Block.FilePath = "~" + string(filepath.Separator) + rel
	return prepared, nil
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of
// path and re-appends the rest, so a file that does not exist yet is still
// judged by where its existing parent really is.
func resolveExistingPrefix(path string) string {
	rest := ""
	cur := path
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// outsideApplyTarget reports, for a prepared edit, the root its backups are
// relative to and the backup directory to use.
func outsideApplyTarget(realWorkspaceRoot, backupDir string, p *PreparedEdit) (root, dir string) {
	if p.OutsideRoot == "" {
		return realWorkspaceRoot, backupDir
	}
	return p.OutsideRoot, filepath.Join(backupDir, OutsideBackupSubdir)
}

// OutsideWriteRefusal is outsideWriteRefusal for callers outside the package
// (undo re-checks every home path it is about to restore).
func OutsideWriteRefusal(relToHome string) string { return outsideWriteRefusal(relToHome) }
