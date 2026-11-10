//go:build trace && unix

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"mochiii/daemon/trace/tracecheck"
)

// startTraced starts the traced daemon with the given extra environment.
func startTraced(t *testing.T, model *e2eModel, files map[string]string, env ...string) *e2eDaemon {
	t.Helper()
	e2eExtraEnv = env
	t.Cleanup(func() { e2eExtraEnv = nil })
	return startE2E(t, model, files, nil)
}

// 4. WITH THE SWITCH OFF: no directory, no file. The daemon here HAS the
// recorder compiled in; only MOCHIII_TRACE decides, and only the exact value 1
// turns it on.
func TestWithTheSwitchOffThereIsNoTraceDirectoryAndNoFile(t *testing.T) {
	for _, env := range [][]string{nil, {"MOCHIII_TRACE="}, {"MOCHIII_TRACE=0"}, {"MOCHIII_TRACE=true"}, {"MOCHIII_TRACE=yes"}, {"MOCHIII_TRACE=1 "}} {
		t.Run(fmt.Sprint(env), func(t *testing.T) {
			model := newE2EModel(t, byToolResults(toolCallSSE("c1", "builtin__read_file", `{"path":"greet.go"}`), textSSE("ok")))
			d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, env...)
			if turn := d.prompt(t, "read it", e2eApproveAll); turn.done.Error != "" {
				t.Fatalf("the turn failed: %s", turn.done.Error)
			}
			if _, err := os.Stat(traceDirOf(t)); err == nil {
				t.Errorf("%s exists with the switch off", traceDirOf(t))
			}
			var found []string
			_ = filepath.WalkDir(d.runtimeDir, func(p string, _ os.DirEntry, _ error) error {
				if strings.HasSuffix(p, ".jsonl") || strings.Contains(p, "trace") {
					found = append(found, p)
				}
				return nil
			})
			if len(found) != 0 {
				t.Errorf("files that look like a trace with the switch off: %v", found)
			}
			if strings.Contains(d.log.String(), "trace:") {
				t.Errorf("the daemon logged about tracing with the switch off:\n%s", d.log.String())
			}
		})
	}
}

// 3. A symlink planted where the daemon's trace file goes is refused: the daemon
// does not write through it, does not replace it, and says so once in its log.
func TestASymlinkedTraceFileIsRefusedByTheRealDaemon(t *testing.T) {
	model := newE2EModel(t, byToolResults(toolCallSSE("c1", "builtin__read_file", `{"path":"greet.go"}`), textSSE("ok")))
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1")
	dir := traceDirOf(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "daemon-"+strconv.Itoa(d.pid)+".jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if turn := d.prompt(t, "read it", e2eApproveAll); turn.done.Error != "" {
		t.Fatalf("a refused trace must not fail the turn: %s", turn.done.Error)
	}
	if b, _ := os.ReadFile(target); string(b) != "untouched\n" {
		t.Fatalf("the daemon wrote through the symlink: %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", fi, err)
	}
	if n := strings.Count(d.log.String(), "trace: off"); n != 1 {
		t.Errorf("the refusal was logged %d times, want once:\n%s", n, d.log.String())
	}
}

// runScenario runs one task through the traced daemon and returns the trace file.
func runScenario(t *testing.T, steps ...[]string) []byte {
	t.Helper()
	model := newE2EModel(t, byToolResults(steps...))
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1")
	turn := d.prompt(t, "make the greeting say Howdy", e2eApproveAll)
	if turn.done.Error != "" {
		t.Fatalf("the turn failed: %s", turn.done.Error)
	}
	_, files := traceTree(t, traceDirOf(t))
	if len(files) != 1 {
		t.Fatalf("%d trace files, want 1", len(files))
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var successSteps = [][]string{
	toolCallSSE("c1", "builtin__read_file", `{"path":"greet.go"}`),
	toolCallSSE("c2", "builtin__propose_edit", `{"path":"greet.go","search":"\"Hello\"","replace":"\"Howdy\""}`),
	withUsage(textSSE("Changed the greeting."), 1200, 34, 0.0021),
}

var toolFailureSteps = [][]string{
	toolCallSSE("c1", "builtin__read_file", `{"path":"does_not_exist.go"}`),
	withUsage(textSSE("That file does not exist; I stopped there."), 900, 20, 0.0012),
}

var (
	reTime = regexp.MustCompile(`"t":\d+`)
	reDur  = regexp.MustCompile(`"dur_ms":\d+`)
	reTurn = regexp.MustCompile(`"turn":"[0-9a-f]{8}"`)
	reSid  = regexp.MustCompile(`,"sid":"[0-9a-f]{8}"`)
)

// normalizeTrace makes a real trace comparable and replayable: the turn id is a
// constant, the clock starts at a fixed instant and advances 100 ms a line, and
// a stage that takes time takes 100 ms. What it keeps is everything an emitter
// decides: the order, the stages, the statuses, the subjects, the call numbers
// and the classes.
func normalizeTrace(raw []byte) string {
	const base = 1731234567000
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var out strings.Builder
	for i, l := range lines {
		l = reTime.ReplaceAllString(l, `"t":`+strconv.Itoa(base+100*i))
		l = reTurn.ReplaceAllString(l, `"turn":"a1b2c3d4"`)
		l = reSid.ReplaceAllString(l, "")
		dur := "0"
		timed := strings.Contains(l, `"status":"ok"`) || strings.Contains(l, `"status":"fail"`)
		for _, s := range []string{"first_token", "tool_requested", "approval_result", `"stage":"request"`} {
			if strings.Contains(l, s) {
				timed = false
			}
		}
		if timed {
			dur = "100"
			if strings.Contains(l, `"stage":"result"`) {
				dur = strconv.Itoa(100 * (len(lines) - 1))
			}
		}
		out.WriteString(reDur.ReplaceAllString(l, `"dur_ms":`+dur))
		out.WriteByte('\n')
	}
	return out.String()
}

// The golden trace is what the daemon writes for a successful task, normalized.
// It is the one example core owns: the viewer's tests can replay it, and this
// test is what keeps it from drifting from what the emitters do. Regenerate it
// with MOCHIII_UPDATE_TRACE_GOLDEN=1 after a deliberate change to the emit
// points, and read the diff.
func TestTheGoldenTraceIsWhatTheDaemonWrites(t *testing.T) {
	got := normalizeTrace(runScenario(t, successSteps...))
	schema, err := tracecheck.Load("trace/trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if errs := schema.CheckFile([]byte(got)); len(errs) != 0 {
		t.Fatalf("the normalized trace does not fit the schema: %v", errs[0])
	}
	const golden = "trace/testdata/golden-success.jsonl"
	if os.Getenv("MOCHIII_UPDATE_TRACE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("the daemon's trace for a successful task is not the golden one.\n--- golden\n%s\n--- got\n%s", want, got)
	}
	if errs := schema.CheckFile(want); len(errs) != 0 {
		t.Errorf("the golden trace does not fit the schema: %v", errs[0])
	}
}

// MOCHIII_TRACE_EVIDENCE_DIR=<dir> writes two real daemon-only traces there, for
// the viewer to read: a successful task and one whose tool fails. Without it this
// test does nothing.
func TestWriteTraceEvidence(t *testing.T) {
	dir := os.Getenv("MOCHIII_TRACE_EVIDENCE_DIR")
	if dir == "" {
		t.Skip("set MOCHIII_TRACE_EVIDENCE_DIR to write the evidence traces")
	}
	for name, steps := range map[string][][]string{"success": successSteps, "tool-failure": toolFailureSteps} {
		raw := runScenario(t, steps...)
		if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
