//go:build trace

package tuitrace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// run puts the process's runtime dir somewhere private and short (a unix socket
// path has a length limit, and the trace dir lives beside the sockets), turns the
// switch on and returns the directory the trace file will be in.
func run(t *testing.T, switchValue string) (traceDir string) {
	t.Helper()
	Reset()
	rt, err := os.MkdirTemp("/tmp", "tt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Reset(); _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("MOCHIII_TRACE", switchValue)
	t.Setenv("TMPDIR", rt)
	dir, err := protocol.SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "trace")
}

func traceFile(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("tui-%d.jsonl", os.Getpid()))
}

func TestOnlyTheExactValueOneTurnsTracingOn(t *testing.T) {
	for _, off := range []string{"", "0", "true", "yes", "1 ", " 1", "01", "on"} {
		run(t, off)
		if Begin() != nil {
			t.Errorf("MOCHIII_TRACE=%q turned tracing on", off)
		}
	}
	run(t, "1")
	if Begin() == nil {
		t.Error("MOCHIII_TRACE=1 did not turn tracing on")
	}
}

func TestWithTheSwitchOffThereIsNoDirectoryAndNoFile(t *testing.T) {
	dir := run(t, "")
	tr := Begin()
	tr.Emit(StageDial, StatusOK, time.Millisecond, KindPrompt, ClassNone) // a nil Turn is safe
	_ = tr.ID()
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("%s exists with the switch off", dir)
	}
	if tr.ID() != "" {
		t.Errorf("a nil turn has the id %q", tr.ID())
	}
}

func TestNothingIsCreatedUntilTheFirstLine(t *testing.T) {
	dir := run(t, "1")
	tr := Begin()
	_ = tr.ID()
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("%s exists before any line was written", dir)
	}
	tr.Emit(StageDial, StatusStart, 0, KindPrompt, ClassNone)
	if _, err := os.Stat(traceFile(dir)); err != nil {
		t.Errorf("the first line made no file: %v", err)
	}
}

func TestTheIDIsOneTheProtocolAccepts(t *testing.T) {
	run(t, "1")
	seen := map[protocol.TraceID]bool{}
	for i := 0; i < 200; i++ {
		id := Begin().ID()
		if !protocol.ValidTraceID(string(id)) {
			t.Fatalf("Begin minted %q, which the daemon would ignore", id)
		}
		seen[id] = true
	}
	if len(seen) < 190 {
		t.Errorf("200 turns got only %d distinct ids", len(seen))
	}
}

func TestEveryLineFitsTheSchemaAndNoStageOrStatusOrClassCombinationBreaksIt(t *testing.T) {
	s := loadSchema(t)
	dir := run(t, "1")
	tr := Begin()
	subjects := []Subject{NoSubject, KindPrompt, UnknownCommand, UnknownTool, Command("fix"), Tool("builtin__read_file"),
		Tool(strings.Repeat("x", 64)), Tool("plainzebra_toolnamecanary_90ab12")}
	for st := stageInvalid; st <= numStages; st++ {
		for status := statusInvalid; status <= numStatuses; status++ {
			for c := ClassNone; c <= numClasses; c++ {
				tr.Emit(st, status, -5*time.Second, subjects[int(st)%len(subjects)], c)
			}
		}
	}
	tr.Emit(StageResultRendered, StatusFail, 1<<40*time.Millisecond, Tool(strings.Repeat("y", 64)), ClassHandshakeRejected)
	b, err := os.ReadFile(traceFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if errs := checkFile(s, b); len(errs) != 0 {
		t.Fatalf("%d bad lines, first: %v", len(errs), errs[0])
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	// numStages-1 stages x (numStatuses-1) statuses x (numClasses) classes, less nothing.
	if want := int(numStages-1)*int(numStatuses-1)*int(numClasses+1) + 1; len(lines) != want {
		t.Errorf("%d lines, want %d: an invalid stage or status must write nothing and every valid one must write a line", len(lines), want)
	}
	for _, l := range lines {
		if len(l) > 512 {
			t.Errorf("a line of %d bytes: %s", len(l), l)
		}
		if strings.Contains(l, `"status":"ok"`) && !strings.Contains(l, `"err":""`) || strings.Contains(l, `"dur_ms":-`) {
			t.Errorf("an err on a line that did not fail, or a negative duration: %s", l)
		}
		if strings.Contains(l, `"status":"fail"`) && strings.Contains(l, `"err":""`) {
			t.Errorf("a failure with no class: %s", l)
		}
	}
	// The sequence numbers are the turn's own and count from 1.
	if !strings.Contains(lines[0], `"n":1,`) || !strings.Contains(lines[len(lines)-1], fmt.Sprintf(`"n":%d,`, len(lines))) {
		t.Errorf("sequence numbers: first %s last %s", lines[0], lines[len(lines)-1])
	}
}

func TestTheFileAndTheDirectoryArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes differ")
	}
	dir := run(t, "1")
	Begin().Emit(StageDial, StatusOK, 0, KindPrompt, ClassNone)
	for p, want := range map[string]os.FileMode{dir: 0o700, traceFile(dir): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s is %v (%v), want %o", p, fi, err, want)
		}
	}
}

func TestASymlinkAtTheFilesNameIsRefusedAndNothingIsWrittenThroughIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	dir := run(t, "1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, traceFile(dir)); err != nil {
		t.Fatal(err)
	}
	tr := Begin()
	tr.Emit(StageDial, StatusOK, 0, KindPrompt, ClassNone)
	tr.Emit(StageRequestSent, StatusStart, 0, KindPrompt, ClassNone)
	if b, _ := os.ReadFile(victim); string(b) != "untouched\n" {
		t.Fatalf("a line was written through the symlink: %q", b)
	}
	if fi, err := os.Lstat(traceFile(dir)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", fi, err)
	}
}

func TestALooseDirectoryIsRefusedNotRepaired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes differ")
	}
	dir := run(t, "1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Begin().Emit(StageDial, StatusOK, 0, KindPrompt, ClassNone)
	if _, err := os.Stat(traceFile(dir)); err == nil {
		t.Error("a file was written in a directory other users can read")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
		t.Errorf("the directory was chmod-ed to %o: refused, not repaired", fi.Mode().Perm())
	}
}

var reTurnOf = regexp.MustCompile(`"turn":"([0-9a-f]{8})"`)

func TestTwoTurnsKeepTheirOwnIDsAndCounters(t *testing.T) {
	dir := run(t, "1")
	a, b := Begin(), Begin()
	a.Emit(StageDial, StatusOK, 0, KindPrompt, ClassNone)
	b.Emit(StageDial, StatusOK, 0, KindPrompt, ClassNone)
	a.Emit(StageRequestSent, StatusStart, 0, KindPrompt, ClassNone)
	raw, _ := os.ReadFile(traceFile(dir))
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	ids := []string{reTurnOf.FindStringSubmatch(lines[0])[1], reTurnOf.FindStringSubmatch(lines[1])[1], reTurnOf.FindStringSubmatch(lines[2])[1]}
	if ids[0] != string(a.ID()) || ids[1] != string(b.ID()) || ids[2] != string(a.ID()) || ids[0] == ids[1] {
		t.Errorf("turn ids %v for a=%s b=%s", ids, a.ID(), b.ID())
	}
	if !strings.Contains(lines[2], `"n":2,`) || !strings.Contains(lines[1], `"n":1,`) {
		t.Errorf("each turn numbers its own lines:\n%s", strings.Join(lines, "\n"))
	}
}
