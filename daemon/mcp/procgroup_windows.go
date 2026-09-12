//go:build windows

package mcp

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows backend for "kill the server and everything it spawned". See
// procgroup.go for what this exists to stop and which badserver mode proves it.
//
// NOT RUN ON HARDWARE. Compile-verified for windows/amd64 and reasoned against
// the Win32 documentation; no Windows machine has executed it. Labelled the way
// peerauth_darwin.go is, and it stays labelled until CI has a Windows runner
// (launch plan Stage 2.3).
//
// WHY A JOB OBJECT AND NOT A PROCESS GROUP. Windows has process groups, but they
// only carry console control events (Ctrl+C / Ctrl+Break) — a console-less child,
// or one that ignores the event, is untouched, and there is no "signal every
// member" primitive. A JOB OBJECT is the real analogue: every process assigned to
// a job, and by default every process THOSE go on to create, belongs to it, and
// TerminateJobObject kills the whole set in one call.
//
// It is strictly STRONGER than the POSIX version in one respect worth stating:
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE means the OS tears the tree down when the
// last handle to the job closes, which includes this daemon dying abruptly. The
// POSIX side has no equivalent — a SIGKILLed daemon leaves the server group
// running. Same guarantee at the seam, better guarantee on daemon crash.

// processGroup owns the job handle for one server's process tree.
type processGroup struct {
	job windows.Handle
}

// prepare creates the job before the child starts.
//
// CREATE_SUSPENDED is deliberate and is the whole reason adopt exists as a
// separate step. There is a window between CreateProcess and
// AssignProcessToJobObject in which an unassigned child could fork a
// grandchild that never joins the job — exactly the orphan this is meant to
// prevent. Starting suspended and resuming only after assignment closes it, so
// the child executes no instruction outside the job.
func (g *processGroup) prepare(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		// Without a job the tree cannot be guaranteed dead. Leave g.job zero;
		// adopt reports it, and Connect fails the server rather than running it
		// with a teardown guarantee it cannot honour.
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
		return
	}

	g.job = job
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
}

// adopt assigns the started-but-suspended child to the job and then lets it run.
//
// A failure here is fatal to the server rather than survivable: running an
// unconfined third-party subprocess whose descendants cannot be reaped is the
// state M4 was filed about, and it is not worth a working server.
func (g *processGroup) adopt(cmd *exec.Cmd) error {
	if g.job == 0 {
		return fmt.Errorf("could not create a job object to contain the server's process tree")
	}
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}

	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("opening server process to contain it: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	if err := windows.AssignProcessToJobObject(g.job, h); err != nil {
		return fmt.Errorf("assigning server to its job object: %w", err)
	}

	// Only now is it safe for the child to execute anything: any grandchild it
	// creates from here inherits the job.
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		return fmt.Errorf("resuming server after containing it: %w", err)
	}
	return nil
}

// resumeProcess lets a CREATE_SUSPENDED child run.
//
// Windows has no documented "resume this process" call — only ResumeThread — so
// a freshly created process is resumed by resuming its threads, of which a
// suspended child has exactly one. The snapshot is filtered by owner pid because
// CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD) enumerates every thread on the
// machine regardless of the pid argument, which is a documented quirk and an
// easy way to resume something else by accident.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if oerr != nil {
			continue
		}
		_, _ = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("no threads found for pid %d to resume", pid)
	}
	return nil
}

// killAll terminates every process in the job — the server and everything it
// spawned, to any depth. The pid is unused: the job, not the pid, is the set.
//
// Errors are discarded on purpose, matching the POSIX side: the failure mode
// that matters is "already gone", which is the outcome being asked for.
func (g *processGroup) killAll(pid int) {
	if g.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(g.job, 1)
}

// release drops the job handle. With KILL_ON_JOB_CLOSE set, this is also the
// backstop that reaps the tree if killAll was never reached.
func (g *processGroup) release() {
	if g.job != 0 {
		_ = windows.CloseHandle(g.job)
		g.job = 0
	}
}

// processAlive reports whether a pid is still running.
//
// The POSIX version probes with signal 0; Windows has no such probe, so this
// opens the process and asks for its exit code. STILL_ACTIVE (259) is the
// documented "has not exited" value. x/sys/windows does not export the
// constant, so it is named here rather than left as a bare literal.
const stillActive = 259

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killProcess terminates one process, not its job. TerminateProcess is the
// closest Windows has to SIGKILL: it is not negotiable and cannot be handled.
func killProcess(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.TerminateProcess(h, 1)
}
