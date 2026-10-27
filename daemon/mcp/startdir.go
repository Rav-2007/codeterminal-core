package mcp

import "path/filepath"

// startDirIn is where a sandboxed command starts: cfg.StartDir, a folder inside
// the workspace that the caller has already confined to it, or the workspace
// root. bwrap's --chdir and docker's -w both set it; landlock runs the command
// where the caller's exec.Cmd.Dir says.
//
// A BUILD IS OFTEN RUN FROM A SUBFOLDER (2026-10-26): sandbox_exec's dir. Given
// cmd.Dir alone, bwrap went back to the root with --chdir, and the command ran
// there.
func startDirIn(cfg SandboxConfig, cleanWs string) string {
	if cfg.StartDir == "" {
		return cleanWs
	}
	return filepath.Clean(cfg.StartDir)
}
