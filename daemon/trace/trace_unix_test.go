//go:build trace && unix

package trace

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// openNoFollowForTest is the daemon's openNoFollow, in this package's words.
func openNoFollowForTest(path string, flag int, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flag|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(perm))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func TestTheDirectoryIs0700AndTheFileIs0600(t *testing.T) {
	// A permissive umask must not widen either.
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	r := enable(t)
	NewTurn("").Emit(Event{Stage: StageRequest, Status: StatusOK})
	for path, want := range map[string]os.FileMode{filepath.Join(r.dir, "trace"): 0o700, r.file(): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s is %o, want %o", path, got, want)
		}
	}
}

func TestASymlinkedFileIsRefusedAndItsTargetIsLeftAlone(t *testing.T) {
	r := enable(t)
	if err := os.Mkdir(filepath.Join(r.dir, "trace"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.file()); err != nil {
		t.Fatal(err)
	}
	tr := NewTurn("")
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK})
	tr.Emit(Event{Stage: StageResult, Status: StatusOK})
	if b, _ := os.ReadFile(target); string(b) != "untouched\n" {
		t.Fatalf("the trace was written through the symlink: %q", b)
	}
	if len(r.logs) != 1 || !strings.Contains(r.logs[0], "off") {
		t.Errorf("the refusal was not logged once: %q", r.logs)
	}
}

func TestADirectoryOthersCanReadIsRefused(t *testing.T) {
	r := enable(t)
	dir := filepath.Join(r.dir, "trace")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // the umask may have trimmed Mkdir's mode
		t.Fatal(err)
	}
	NewTurn("").Emit(Event{Stage: StageRequest, Status: StatusOK})
	if _, err := os.Stat(r.file()); err == nil {
		t.Fatal("a file was written into a directory other users can read")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("the refused directory was chmod'ed to %o; it must be left as found", fi.Mode().Perm())
	}
	if len(r.logs) != 1 {
		t.Errorf("logs: %q", r.logs)
	}
}

func TestAFileOthersCanReadIsRefused(t *testing.T) {
	r := enable(t)
	if err := os.Mkdir(filepath.Join(r.dir, "trace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.file(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.file(), 0o644); err != nil {
		t.Fatal(err)
	}
	NewTurn("").Emit(Event{Stage: StageRequest, Status: StatusOK})
	if fi, _ := os.Stat(r.file()); fi.Size() != 0 {
		t.Fatal("a line was written to a file other users can read")
	}
}
