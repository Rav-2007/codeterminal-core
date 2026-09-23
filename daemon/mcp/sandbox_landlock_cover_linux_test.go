//go:build linux

package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The helper refuses bad input BEFORE it restricts or execs anything, returning
// the setup and not-found codes the shell and docker use. A broken invocation
// must never become a half-restricted exec.
func TestTheHelperRefusesBadInputBeforeExec(t *testing.T) {
	if code := sandboxExecMain([]string{"--bogus", "/x", "--", "true"}); code != exitSandboxSetup {
		t.Errorf("unknown flag: got %d, want %d", code, exitSandboxSetup)
	}
	if code := sandboxExecMain([]string{"--"}); code != exitSandboxSetup {
		t.Errorf("no command after --: got %d, want %d", code, exitSandboxSetup)
	}
	if code := sandboxExecMain([]string{"--", "mochiii-no-such-command-xyz"}); code != exitSandboxNotFound {
		t.Errorf("missing command: got %d, want %d", code, exitSandboxNotFound)
	}
}

// rightsFor maps each access class to rights and grants nothing for an unknown
// one -- the property that makes an unrecognised flag fail closed.
func TestRightsForCoversEveryClass(t *testing.T) {
	if rightsFor(accessDevice) == 0 {
		t.Error("the device class was granted no rights")
	}
	if rightsFor("not-a-class") != 0 {
		t.Error("an unknown access class was granted rights")
	}
}

// applySandbox refuses to pretend on a kernel with no Landlock, rather than
// installing half a sandbox and reporting success.
func TestApplySandboxRefusesWithoutLandlock(t *testing.T) {
	if err := applySandbox(landlockPolicy{}, 0, helperEgress{}); err == nil {
		t.Error("applySandbox accepted ABI 0")
	}
}

// addLandlockRule skips a path that is not on this host (as the bwrap backend
// skips binding it) and masks directory rights down to file rights on a regular
// file (the kernel rejects directory rights on a non-directory).
func TestAddLandlockRuleSkipsMissingAndMasksFiles(t *testing.T) {
	abi := requireLandlock(t)
	attr := unix.LandlockRulesetAttr{Access_fs: handledFSRights(abi)}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		t.Fatalf("create ruleset: %v", errno)
	}
	ruleset := int(fd)
	defer func() { _ = unix.Close(ruleset) }()

	if err := addLandlockRule(ruleset, filepath.Join(t.TempDir(), "absent"), landlockReadRights); err != nil {
		t.Errorf("a missing path was not skipped: %v", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addLandlockRule(ruleset, file, rightsFor(accessReadWrite)); err != nil {
		t.Errorf("a file given directory rights was not masked to file rights: %v", err)
	}
	// A path with a regular file where a directory should be fails to open with
	// ENOTDIR -- an error, distinct from the ENOENT that is silently skipped.
	if err := addLandlockRule(ruleset, filepath.Join(file, "under-a-file"), landlockReadRights); err == nil {
		t.Error("a path beneath a non-directory was not reported as an error")
	}
	// A rule that resolves to no rights (an unknown access class) adds nothing
	// and is not an error.
	if err := addLandlockRule(ruleset, t.TempDir(), 0); err != nil {
		t.Errorf("a zero-rights rule was not a no-op: %v", err)
	}
}

// restrictLandlock reports a rule it cannot add rather than restricting the
// thread with a partial ruleset. The bad rule fails before restrict_self, so
// the thread is never actually confined.
func TestRestrictLandlockPropagatesARuleError(t *testing.T) {
	abi := requireLandlock(t)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := landlockPolicy{Rules: []landlockRule{{Access: accessRead, Path: filepath.Join(file, "under-a-file")}}}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked; the thread dies at return
		_ = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
		done <- restrictLandlock(bad, abi)
	}()
	if err := <-done; err == nil {
		t.Error("restrictLandlock accepted a rule that could not be added")
	}
}

// assembleBPF rejects an out-of-range FALSE jump as well as a true one, rather
// than emitting a jump that silently lands on the wrong instruction.
func TestAssembleBPFRefusesBadFalseJumps(t *testing.T) {
	if _, err := assembleBPF([]bpfStep{{code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, jf: "nowhere"}}); err == nil {
		t.Error("a jf to a missing label assembled")
	}
}

// checkEnforced notices a HALF-applied sandbox: with the landlock domain in
// place but no seccomp filter, a file outside the policy is blocked yet a Unix
// socket can still be created -- so the probe's self-check must fail rather than
// certify the sandbox. This is what stops "presence is not capability" from
// passing a kernel that enforces only part of what was asked.
func TestCheckEnforcedNoticesAnUnblockedSocket(t *testing.T) {
	abi := requireLandlock(t)
	f := newFixture(t)
	secret := filepath.Join(f.outside, "secret") // written by newFixture, outside the policy
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread dies with its restrictions
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			done <- err
			return
		}
		if err := restrictLandlock(f.policy, abi); err != nil {
			done <- err
			return
		}
		// Landlock only, no seccomp: the socket is not blocked, so checkEnforced
		// must report the sandbox is incomplete.
		if err := checkEnforced(secret); err == nil {
			done <- errors.New("checkEnforced passed with Unix sockets unblocked")
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
