//go:build !windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs line through the user's shell in a process group of its
// own, so ending it ends everything it started.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	shell, login := shellPath()
	flag := "-c"
	if login {
		flag = "-lc"
	}
	cmd := exec.CommandContext(ctx, shell, flag, line)
	// Never nil: nil would hand it the daemon's environment, keys and all.
	cmd.Env = runCommandEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}
