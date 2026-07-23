package editapply

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomicNoFollow writes data to path as the forward-edit counterpart of
// the undo path's hardened restore (daemon/apply_cmd.go's stageRestore +
// confinedRestorePath): a temp file in the target's own directory, chmod'd to
// perm, then atomically renamed into place — and a refusal to write through a
// symlink standing at the destination leaf.
//
// It replaces Apply's former plain os.WriteFile, which followed a leaf symlink
// (O_CREATE|O_TRUNC through the link) and truncated-then-wrote non-atomically.
// Two report findings are closed together, and independently:
//
//   - ESCAPE. A dangling or outward-pointing symlink planted at a to-be-created
//     target during the TUI's human-confirm window would let model-supplied
//     content be written OUTSIDE the workspace (~/.bashrc, autostart entries).
//     The atomic os.Rename below never follows the destination link — it
//     replaces the link itself — so the write can never reach the link's target;
//     the pre-write Lstat additionally refuses an anomalous symlink outright,
//     for a clear error rather than a silent replace. (The edit path is already
//     immune upstream: resolveSafeTarget hands back the EvalSymlinks-resolved
//     real path, never a symlink; this hardens the create path, where the leaf
//     does not exist at prepare time and VerifyUnchanged's ENOENT-through-a-
//     dangling-link case previously let a planted link slip past.)
//   - CORRUPTION. os.WriteFile truncates first, so a crash or interruption
//     mid-write left a partial file that Apply reported as unwritten and undo
//     then refused to revert. The target here is only ever swapped by renaming a
//     fully-written temp, so an interrupted write leaves the original intact and
//     at worst an orphaned temp file, never a truncated target.
//
// editapply cannot import daemon (the undo writer's home), so this mirrors the
// pattern locally, exactly as create.go's resolveSafeNewPath already mirrors
// confinedRestorePath rather than sharing it.
func writeFileAtomicNoFollow(path string, data []byte, perm os.FileMode) error {
	// Refuse to write where a symlink stands. The atomic rename below already
	// declines to follow it, but a symlink where a regular file (or nothing)
	// belongs is anomalous — mirror the undo path's explicit leaf-symlink
	// refusal so the caller gets a reason, not a silently-replaced link. Lstat
	// does not follow the link itself.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to write through it", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".codeterminal-apply-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// From here on, any failure must not leave the temp file behind.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// os.CreateTemp always makes 0600; carry the mode the prepared edit recorded
	// (an existing file's mode, or newFileMode for a create), set before the
	// rename so the target never briefly appears with the wrong permissions.
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
