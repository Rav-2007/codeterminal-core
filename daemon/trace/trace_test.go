//go:build trace

package trace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	dir  string
	logs []string
	mu   sync.Mutex
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

// enable turns the recorder on over a fresh parent directory, with the same
// no-follow open the daemon passes.
func enable(t *testing.T) *recorder {
	t.Helper()
	Reset()
	r := &recorder{dir: filepath.Join(t.TempDir(), "run")}
	if err := os.Mkdir(r.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	Enable(Config{Dir: func() (string, error) { return r.dir, nil }, Open: openNoFollowForTest, Logf: r.logf})
	t.Cleanup(Reset)
	return r
}

func (r *recorder) file() string {
	return filepath.Join(r.dir, "trace", fmt.Sprintf("daemon-%d.jsonl", os.Getpid()))
}

func (r *recorder) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(r.file())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestEnableWritesNothingUntilTheFirstLine(t *testing.T) {
	r := enable(t)
	if !On() {
		t.Fatal("Enable did not turn the recorder on")
	}
	tr := NewTurn("")
	if _, err := os.Stat(filepath.Join(r.dir, "trace")); err == nil {
		t.Fatal("the trace directory exists before any event was written")
	}
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK, Tool: KindPrompt.Tool()})
	if _, err := os.Stat(r.file()); err != nil {
		t.Fatalf("no file after the first event: %v", err)
	}
}

// Every line is the template and nothing else, and every value the typed API
// can be given badly is made safe or dropped.
func TestEmitValidatesWhatItIsGiven(t *testing.T) {
	r := enable(t)
	schema := loadSchema(t)
	tr := NewTurn("a-session-name")
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK, Tool: KindPrompt.Tool(), Err: ClassAuth}) // err on an ok line: dropped
	tr.Emit(Event{Stage: StageToolRun, Status: StatusFail, Tool: ToolName("builtin__x"), Call: 2}) // a failure without a class: other
	tr.Emit(Event{Stage: StageToolRun, Status: StatusFail, Err: Class(250)})                       // a class outside the table: other
	tr.Emit(Event{Stage: StageModelCall, Status: StatusOK, Dur: -5 * time.Second, Call: -3})       // negative numbers: zero
	tr.Emit(Event{Stage: Stage(0), Status: StatusOK})                                              // no such stage: nothing written
	tr.Emit(Event{Stage: Stage(99), Status: StatusOK})
	tr.Emit(Event{Stage: StageResult, Status: Status(0)})
	tr.Emit(Event{Stage: StageResult, Status: Status(77)})
	tr.Emit(Event{Stage: StageToolRun, Status: StatusFail, Tool: ToolName("two words here"), Err: ClassToolFailed}) // not an id: unknown_tool

	lines := r.lines(t)
	if len(lines) != 5 {
		t.Fatalf("%d lines written, want 5 (the four invalid events write nothing):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for i, l := range lines {
		if err := schema.CheckLine([]byte(l)); err != nil {
			t.Errorf("line %d does not fit the schema: %v\n%s", i+1, err, l)
		}
	}
	for i, want := range []string{`"err":""`, `"err":"other"`, `"err":"other"`, `"dur_ms":0,"tool":"","call":0`, `"tool":"unknown_tool","call":0,"err":"tool_failed"`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %s, want it to contain %s", i+1, lines[i], want)
		}
	}
	// n counts the lines that were written.
	if !strings.Contains(lines[4], `"n":5`) {
		t.Errorf("sequence numbers skipped: %s", lines[4])
	}
}

func TestTheSessionIsOnlyEverItsHash(t *testing.T) {
	r := enable(t)
	const session = "session-name-CANARY-0123456789"
	tr := NewTurn(session)
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK, Tool: KindPrompt.Tool()})
	tr.Emit(Event{Stage: StageResult, Status: StatusOK})
	b, _ := os.ReadFile(r.file())
	sum := sha256.Sum256([]byte(session))
	want := hex.EncodeToString(sum[:4])
	if !bytes.Contains(b, []byte(`"sid":"`+want+`"`)) {
		t.Errorf("the first line does not carry sid %s:\n%s", want, b)
	}
	if bytes.Contains(b, []byte("CANARY")) || bytes.Contains(b, []byte(session)) {
		t.Errorf("the session name is in the trace:\n%s", b)
	}
	if n := bytes.Count(b, []byte(`"sid"`)); n != 1 {
		t.Errorf("sid on %d lines, want only the first", n)
	}
	// No session named: no sid at all.
	r2 := enable(t)
	NewTurn("").Emit(Event{Stage: StageRequest, Status: StatusOK})
	b2, _ := os.ReadFile(r2.file())
	if bytes.Contains(b2, []byte("sid")) {
		t.Errorf("a turn with no session carries a sid: %s", b2)
	}
}

func TestEachTurnHasItsOwnIDAndCounters(t *testing.T) {
	enable(t)
	a, b := NewTurn(""), NewTurn("")
	if a.id == b.id || len(a.id) != 8 {
		t.Errorf("turn ids %q and %q", a.id, b.id)
	}
	if a.NextCall() != 1 || a.NextCall() != 2 || b.NextCall() != 1 {
		t.Error("the call counter is per turn and starts at 1")
	}
}

func TestTheContextCarriesTheTurnAndTheCall(t *testing.T) {
	enable(t)
	tr := NewTurn("")
	ctx := With(context.Background(), tr)
	if From(ctx) != tr {
		t.Error("From did not return the turn that With stored")
	}
	if CallFrom(WithCall(ctx, 4)) != 4 || CallFrom(ctx) != 0 {
		t.Error("the call counter did not round-trip")
	}
	if WithCall(ctx, 0) != ctx {
		t.Error("call 0 means no turn: the context must be returned as it was")
	}
}

func TestSwitchedOffNothingIsNeededAndNothingIsMade(t *testing.T) {
	Reset()
	if On() || NewTurn("x") != nil {
		t.Fatal("the recorder is on without Enable")
	}
	Enable(Config{}) // missing dependencies: stays off
	if On() {
		t.Fatal("Enable without a Dir and an Open turned the recorder on")
	}
}

func TestFirstTokenIsWrittenOnceWhoeverAsks(t *testing.T) {
	r := enable(t)
	mc := NewTurn("").BeginModelCall()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				mc.Token()
			}
		}()
	}
	wg.Wait()
	mc.End(ClassNone)
	b, _ := os.ReadFile(r.file())
	if n := bytes.Count(b, []byte(`"stage":"first_token"`)); n != 1 {
		t.Errorf("%d first_token lines, want 1:\n%s", n, b)
	}
	if !bytes.Contains(b, []byte(`"stage":"model_call","status":"ok"`)) {
		t.Errorf("no model_call end line:\n%s", b)
	}
	mc2 := NewTurn("").BeginModelCall()
	mc2.End(ClassRateLimited)
	b, _ = os.ReadFile(r.file())
	if !bytes.Contains(b, []byte(`"status":"fail"`)) || !bytes.Contains(b, []byte(`"err":"rate_limited"`)) {
		t.Errorf("a failed model call is not recorded with its class:\n%s", b)
	}
}

func TestSpanWritesStartAndEnd(t *testing.T) {
	r := enable(t)
	tr := NewTurn("")
	sp := tr.Begin(StageApprovalWait, ToolName("builtin__propose_edit"), 3)
	time.Sleep(15 * time.Millisecond)
	sp.End(StatusOK, ClassDeniedUser) // a class on an ok end is not written
	lines := r.lines(t)
	if len(lines) != 2 || !strings.Contains(lines[0], `"status":"start"`) || !strings.Contains(lines[1], `"status":"ok"`) ||
		!strings.Contains(lines[1], `"call":3`) || !strings.Contains(lines[1], `"err":""`) {
		t.Fatalf("span lines:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[1], `"dur_ms":0,`) {
		t.Errorf("the end line carries no duration: %s", lines[1])
	}
}

// Many goroutines writing to one turn: every line whole, every sequence number
// once.
func TestConcurrentWritersKeepLinesWhole(t *testing.T) {
	r := enable(t)
	schema := loadSchema(t)
	tr := NewTurn("")
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				tr.Emit(Event{Stage: StageToolRun, Status: StatusOK, Tool: ToolName("builtin__read_file"), Call: tr.NextCall() + g*0})
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(r.file())
	if errs := schema.CheckFile(b); len(errs) != 0 {
		t.Fatalf("%d bad lines, first: %v", len(errs), errs[0])
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		i := strings.Index(l, `"n":`)
		n := l[i : i+strings.Index(l[i:], ",")]
		if seen[n] {
			t.Fatalf("sequence number %s written twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 800 {
		t.Errorf("%d lines, want 800", len(seen))
	}
}

func TestTheFileIsBoundedAndAnOversizeLineIsDropped(t *testing.T) {
	r := enable(t)
	tr := NewTurn("")
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK})
	s := active.Load()
	s.write(bytes.Repeat([]byte("x"), maxLineBytes+1)) // not something Emit can build; the cap is checked regardless
	if Dropped() != 1 {
		t.Errorf("Dropped() = %d after an oversize line, want 1", Dropped())
	}
	old := maxFileBytes
	t.Cleanup(func() { maxFileBytes = old })
	fi, _ := os.Stat(r.file())
	maxFileBytes = fi.Size() + 10
	tr.Emit(Event{Stage: StageResult, Status: StatusOK})
	if Dropped() != 2 {
		t.Errorf("Dropped() = %d after the size bound, want 2", Dropped())
	}
	if got := len(r.lines(t)); got != 1 {
		t.Errorf("%d lines in a file at its bound, want 1", got)
	}
}
