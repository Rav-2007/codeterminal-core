//go:build windows

package main

import (
	"context"

	"mochiii/daemon/mcp"
	"os/exec"
	"strconv"
	"syscall"
)

// shellCommand runs line through cmd.exe, and ending it ends the whole tree.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe")
	// Never nil: nil would hand it the daemon's environment, keys and all.
	cmd.Env = runCommandEnv()
	// Passed as written: cmd.exe parses its own command line.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /s /c "` + line + `"`}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.Env = mcp.ServerEnv(nil)
		return kill.Run()
	}
	return cmd
}
