package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// helperLifetimeRoleArg, as os.Args[1], makes this test binary play a part in
// TestTheHelperEndsWithItsKilledDaemon instead of running tests. TestMain
// dispatches it before any setup, so a part that is killed, as every part here
// is, leaves nothing behind.
const helperLifetimeRoleArg = "__helper-lifetime-role"

// helperLifetimeRoleMain plays a stand-in daemon ("daemon") or a stand-in
// helper ("child"). The daemon starts two children -- one through
// startHelperProcess, as the real helper is started, and one with a plain
// Start as the control -- prints both process IDs, and waits to be killed.
func helperLifetimeRoleMain(args []string) int {
	if len(args) == 1 && args[0] == "child" {
		time.Sleep(2 * time.Minute)
		return 0
	}
	if len(args) != 1 || args[0] != "daemon" {
		fmt.Fprintf(os.Stderr, "unknown helper-lifetime role %q\n", args)
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	bound := exec.Command(self, helperLifetimeRoleArg, "child")
	if err := startHelperProcess(bound, log.New(os.Stderr, "", 0)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	control := exec.Command(self, helperLifetimeRoleArg, "child")
	if err := control.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = bound.Process.Kill()
		return 1
	}
	fmt.Printf("%d %d\n", bound.Process.Pid, control.Process.Pid)
	time.Sleep(2 * time.Minute)
	return 0
}

// TestTheHelperEndsWithItsKilledDaemon kills a stand-in daemon the way Windows
// stops every daemon -- TerminateProcess, no shutdown -- and requires the child
// it started through startHelperProcess to end with it.
//
// THE CONTROL CHILD IS WHAT MAKES A PASS MEAN SOMETHING. It is started by the
// same daemon without startHelperProcess and must SURVIVE the kill. If it died
// too, something else (a job the test runner put everything in, say) was doing
// the killing, and the bound child's death would prove nothing about the job.
func TestTheHelperEndsWithItsKilledDaemon(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only: elsewhere a daemon is stopped with SIGTERM and its shutdown stops the helper (HelperProcess.Stop)")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(self, helperLifetimeRoleArg, "daemon")
	var stderr bytes.Buffer
	daemon.Stderr = &stderr
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	pids := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			pids <- sc.Text()
		}
		close(pids)
	}()

	var bound, control int
	select {
	case line, ok := <-pids:
		if _, err := fmt.Sscanf(line, "%d %d", &bound, &control); !ok || err != nil {
			_ = daemon.Process.Kill()
			_ = daemon.Wait()
			t.Fatalf("the stand-in daemon printed %q, want two process IDs; its stderr: %s", line, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
		t.Fatalf("the stand-in daemon started no children within 30s; its stderr: %s", stderr.String())
	}
	t.Cleanup(func() {
		for _, pid := range []int{bound, control} {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})
	if !processAlive(bound) || !processAlive(control) {
		t.Fatalf("a child was not running before its daemon was killed (bound %v, control %v)",
			processAlive(bound), processAlive(control))
	}

	if err := daemon.Process.Kill(); err != nil {
		t.Fatalf("killing the stand-in daemon: %v", err)
	}
	_ = daemon.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for processAlive(bound) {
		if time.Now().After(deadline) {
			t.Fatalf("the child started through startHelperProcess (process %d) was still running 10s after "+
				"its daemon was killed: an orphaned helper. The daemon's stderr: %s", bound, stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !processAlive(control) {
		t.Fatalf("the control child, started without startHelperProcess, ended with its daemon too, so this " +
			"test cannot tell a helper bound to its daemon from an orphan")
	}
}
