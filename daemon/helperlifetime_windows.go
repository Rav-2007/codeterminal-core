//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// THE EMBEDDER HELPER ENDS WITH ITS DAEMON ON WINDOWS (2026-10-18).
//
// Windows has no SIGTERM to send another process. Every way a daemon is stopped
// there -- `mochiii --stop`, `mochiii --index`, the VS Code extension
// restarting it, the installer upgrading it -- is TerminateProcess, which runs
// none of the daemon's shutdown. HelperProcess.Stop therefore never ran, and the
// helper, a separate process holding the loaded model, stayed until logoff: one
// more for every stop. Nothing showed it while the helper could not be found on
// Windows at all (helperpath.go's missing .exe); the fix that turned code search
// on there is what would have started the leak.
//
// A JOB OBJECT WITH KILL_ON_JOB_CLOSE, whose only handle this daemon holds.
// However the daemon ends, Windows closes that handle and kills every process in
// the job -- the same mechanism daemon/mcp/procgroup_windows.go uses for MCP
// servers. Unlike there, the helper is not started suspended: it is this
// project's own binary and starts no processes, so the only gap is the daemon
// dying in the instant between Start and AssignProcessToJobObject.
//
// A FAILURE TO BIND IS LOGGED, NOT FATAL. Code search whose helper might outlive
// a killed daemon is worth more than no code search; an MCP server, which is
// third-party code, is refused instead.

// helperJob is the one job every helper this daemon starts joins.
var helperJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

func daemonLifetimeJob() (windows.Handle, error) {
	helperJob.once.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			helperJob.err = fmt.Errorf("creating a job object: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(job)
			helperJob.err = fmt.Errorf("making the job object kill on close: %w", err)
			return
		}
		helperJob.handle = job
	})
	return helperJob.handle, helperJob.err
}

// bindToDaemonLifetime puts process pid into this daemon's job. The caller
// still holds the process's handle (an exec.Cmd before Wait), so pid cannot
// have been reused by another process.
func bindToDaemonLifetime(pid int) error {
	job, err := daemonLifetimeJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("opening process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("adding process %d to the job object: %w", pid, err)
	}
	return nil
}

// startHelperProcess starts the helper and ties its life to this daemon's.
func startHelperProcess(cmd *exec.Cmd, logger *log.Logger) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := bindToDaemonLifetime(cmd.Process.Pid); err != nil && logger != nil {
		logger.Printf("warning: the embedder helper (process %d) will outlive this daemon if it is killed: %v",
			cmd.Process.Pid, err)
	}
	return nil
}
