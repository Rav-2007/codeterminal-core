//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// CTRL+C AT THE KEY PROMPT GIVES THE TERMINAL ITS ECHO BACK. A signal runs no
// deferred function, so withoutEcho's restore never ran: Ctrl+C at
// `mochiii-daemon connect`'s prompt left the user's shell with echo off (FOUND
// 2026-10-01, on a real pty). Proved the same way here: this test re-runs
// itself on a pseudo-terminal that is its controlling terminal, waits at an
// echo-off prompt, and presses Ctrl+C.
//
// Neuter check: drop the signal goroutine from withoutEcho, and ECHO is off
// after the process dies.
func TestCtrlCAtTheKeyPromptRestoresEcho(t *testing.T) {
	if os.Getenv("MOCHIII_ECHO_PROBE") == "1" {
		withoutEcho(func() {
			fmt.Println("PROMPT-READY")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		})
		os.Exit(0)
	}

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	defer func() { _ = master.Close() }()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skipf("cannot unlock the pseudo-terminal: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skipf("cannot name the pseudo-terminal: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pseudo-terminal: %v", err)
	}
	defer func() { _ = slave.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCtrlCAtTheKeyPromptRestoresEcho$")
	cmd.Env = append(os.Environ(), "MOCHIII_ECHO_PROBE=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	ready := make(chan bool, 1)
	go func() {
		var seen strings.Builder
		buf := make([]byte, 4096)
		for {
			k, err := master.Read(buf)
			seen.Write(buf[:k])
			if strings.Contains(seen.String(), "PROMPT-READY") {
				ready <- true
				return
			}
			if err != nil {
				ready <- false
				return
			}
		}
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the probe ended before its prompt")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the probe never reached its prompt")
	}
	termios := func() *unix.Termios {
		tio, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		return tio
	}
	if termios().Lflag&unix.ECHO != 0 {
		t.Fatal("echo was on at the prompt; the test proves nothing")
	}

	if _, err := master.Write([]byte{0x03}); err != nil { // Ctrl+C
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		t.Fatal("Ctrl+C did not end the prompt")
	}
	if termios().Lflag&unix.ECHO == 0 {
		t.Error("Ctrl+C at the prompt left the terminal with echo OFF")
	}
}
