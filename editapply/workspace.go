package editapply

import (
	"fmt"
	"path/filepath"
)

// ResolveRealWorkspaceRoot resolves workspace to an absolute, symlink-
// resolved path suitable for PrepareEdit's confinement check. Both the CLI
// (`edits apply`/`edits undo`) and the Mochiii TUI's --workspace flag call
// this, so an edit is confined identically no matter which client applied
// it.
func ResolveRealWorkspaceRoot(workspace string) (string, error) {
	absRoot, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", workspace, err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root %s: %w", workspace, err)
	}
	return realRoot, nil
}
