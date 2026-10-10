//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A PIPE PLANTED AS A SHARE MUST NOT HANG THE COUNT. Opening a named pipe to
// read waits until something opens it to write, and the day's total is read
// before every model call: one stray file would stop every turn of every
// project, with nothing on screen to say why.
func TestAPlantedPipeDoesNotHangTheCount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spend")
	l, _ := testLedger(t, dir, "a")
	l.add("s", 100, 0)
	if err := syscall.Mkfifo(filepath.Join(dir, l.own.Day, "other.json"), 0o600); err != nil {
		t.Skipf("making a named pipe: %v", err)
	}
	done := make(chan int, 1)
	go func() { done <- l.status("s").Day.Tokens }()
	select {
	case got := <-done:
		if got != 100 {
			t.Errorf("the day is %d tokens with a pipe in its folder, want this daemon's own 100", got)
		}
	case <-time.After(5 * time.Second):
		// Unblock the reader this test left waiting, so the test binary can exit.
		if w, err := os.OpenFile(filepath.Join(dir, l.own.Day, "other.json"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("reading the day's total waited on a named pipe planted in its folder")
	}
}
