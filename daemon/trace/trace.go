//go:build trace

package trace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// maxLineBytes is the schema's line cap. A line is built from a fixed template
// and values of bounded length, so it is about 200 bytes; the cap is checked
// anyway, because a cap that is only true by construction is a cap nobody
// notices when the construction changes.
const maxLineBytes = 512

// maxFileBytes bounds one daemon's trace file. Past it events are dropped and
// counted, as jsonlSink does rather than grow without limit.
var maxFileBytes int64 = 64 << 20

// Config is what the daemon hands the recorder. The recorder owns no path and
// opens no file by any means of its own: Dir says where the trace directory
// goes (protocol.SocketDir), and Open is the one way a file is opened
// (openNoFollow, which refuses a symlink at the leaf).
type Config struct {
	Dir  func() (string, error)
	Open func(path string, flag int, perm os.FileMode) (*os.File, error)
	Logf func(format string, args ...any)
}

type sink struct {
	cfg Config

	mu      sync.Mutex
	f       *os.File
	failed  bool
	size    int64
	dropped uint64
}

var active atomic.Pointer[sink]

// Enable turns the recorder on. It opens nothing: the directory and the file
// are made when the first event is written, so a daemon that is started with
// MOCHIII_TRACE=1 and never asked anything leaves no trace of one.
func Enable(cfg Config) {
	if cfg.Dir == nil || cfg.Open == nil {
		return
	}
	active.CompareAndSwap(nil, &sink{cfg: cfg})
}

// Reset turns the recorder off and closes its file. It exists for tests.
func Reset() {
	if s := active.Swap(nil); s != nil {
		s.mu.Lock()
		if s.f != nil {
			_ = s.f.Close()
		}
		s.mu.Unlock()
	}
}

// On reports whether the recorder is enabled.
func On() bool { return active.Load() != nil }

// Dropped is how many lines were not written because of the size bound or a
// write error.
func Dropped() uint64 {
	s := active.Load()
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *sink) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

// open makes the directory (0700) and opens the file (0600, no symlink). Every
// refusal is logged once, to the daemon's own log, and leaves the recorder
// silent for the rest of the run: a trace that cannot be written safely is not
// written.
func (s *sink) open() bool {
	parent, err := s.cfg.Dir()
	if err != nil {
		s.logf("trace: off: %v", err)
		return false
	}
	dir := filepath.Join(parent, "trace")
	if err := ensurePrivateDir(dir); err != nil {
		s.logf("trace: off: %v", err)
		return false
	}
	path := filepath.Join(dir, "daemon-"+strconv.Itoa(os.Getpid())+".jsonl")
	f, err := s.cfg.Open(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		s.logf("trace: off: cannot open %s: %v", path, err)
		return false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0) {
		_ = f.Close()
		s.logf("trace: off: %s is not a private regular file", path)
		return false
	}
	s.f, s.size = f, fi.Size()
	s.logf("trace: writing to %s", path)
	return true
}

// ensurePrivateDir creates dir at 0700 or accepts one that is already a real
// directory with no group or other access. It does not chmod: a directory
// somebody else loosened is refused, not repaired.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New(dir + " is not a directory")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return errors.New(dir + " is accessible to other users")
	}
	return nil
}

func (s *sink) write(line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return
	}
	if s.f == nil && !s.open() {
		s.failed = true
		return
	}
	if len(line) > maxLineBytes || s.size+int64(len(line)) > maxFileBytes {
		s.dropped++
		return
	}
	n, err := s.f.Write(line)
	s.size += int64(n)
	if err != nil {
		s.dropped++
	}
}

// Turn is one request's trace: the id the daemon minted for it, the hash of the
// session that sent it, and the counters that number its lines and its calls.
type Turn struct {
	s       *sink
	id      string
	sid     string
	started time.Time

	mu    sync.Mutex
	n     int
	calls int
}

// NewTurn starts a trace for one request, or returns nil when the recorder is
// off. The id is random and the daemon's own. The session name is the client's:
// it is recorded only as the first 8 hex digits of its SHA-256.
func NewTurn(session string) *Turn {
	s := active.Load()
	if s == nil {
		return nil
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil
	}
	t := &Turn{s: s, id: hex.EncodeToString(b[:]), started: time.Now()}
	if session != "" {
		sum := sha256.Sum256([]byte(session))
		t.sid = hex.EncodeToString(sum[:4])
	}
	return t
}

type turnKey struct{}
type callKey struct{}

// With puts the turn on a context. A nil turn leaves the context alone.
func With(ctx context.Context, t *Turn) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, turnKey{}, t)
}

// From is the turn a context carries, or nil.
func From(ctx context.Context) *Turn {
	t, _ := ctx.Value(turnKey{}).(*Turn)
	return t
}

// WithCall and CallFrom carry the call counter of the tool call in flight, so
// the approval prompt, which only has a context, can say which call it is for.
func WithCall(ctx context.Context, n int) context.Context {
	if n == 0 { // no turn, so no counter: nothing to carry, and no allocation when tracing is off
		return ctx
	}
	return context.WithValue(ctx, callKey{}, n)
}

func CallFrom(ctx context.Context) int {
	n, _ := ctx.Value(callKey{}).(int)
	return n
}

// NextCall returns the next number in this turn's call counter. Call ids that
// models send are never recorded; this is the id.
func (t *Turn) NextCall() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	return t.calls
}

// Emit writes one line. It is the only way a line is written, and it validates
// every field: a stage or status outside the enums writes nothing; an error
// class appears only on a failure, and a failure without one is "other"; a
// negative duration or call number is zero. The line is built from a fixed
// template, so what is in it is those fields and nothing else.
func (t *Turn) Emit(e Event) {
	if t == nil {
		return
	}
	if e.Stage == stageInvalid || e.Stage >= numStages || e.Status == statusInvalid || e.Status >= numStatuses {
		return
	}
	switch {
	case e.Status != StatusFail:
		e.Err = ClassNone
	case e.Err == ClassNone || e.Err >= numClasses:
		e.Err = ClassOther
	}
	if e.Dur < 0 {
		e.Dur = 0
	}
	if e.Call < 0 {
		e.Call = 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	b := make([]byte, 0, 256)
	b = append(b, `{"v":1,"t":`...)
	b = strconv.AppendInt(b, time.Now().UnixMilli(), 10)
	b = append(b, `,"src":"daemon","turn":"`...)
	b = append(b, t.id...)
	b = append(b, `","n":`...)
	b = strconv.AppendInt(b, int64(t.n), 10)
	b = append(b, `,"stage":"`...)
	b = append(b, stageNames[e.Stage]...)
	b = append(b, `","status":"`...)
	b = append(b, statusNames[e.Status]...)
	b = append(b, `","dur_ms":`...)
	b = strconv.AppendInt(b, e.Dur.Milliseconds(), 10)
	b = append(b, `,"tool":"`...)
	b = append(b, e.Tool.s...)
	b = append(b, `","call":`...)
	b = strconv.AppendInt(b, int64(e.Call), 10)
	b = append(b, `,"err":"`...)
	b = append(b, classNames[e.Err]...)
	b = append(b, '"')
	if t.n == 1 && t.sid != "" {
		b = append(b, `,"sid":"`...)
		b = append(b, t.sid...)
		b = append(b, '"')
	}
	b = append(b, "}\n"...)
	t.s.write(b)
}

// Since is the time since the turn began, for the result line.
func (t *Turn) Since() time.Duration {
	if t == nil {
		return 0
	}
	return time.Since(t.started)
}

// ModelCall is one model call's trace: a start, at most one first token, and an
// end.
type ModelCall struct {
	t     *Turn
	start time.Time
	first atomic.Bool
}

// BeginModelCall writes the start line and returns the handle, or nil when the
// turn is nil. The token callback runs once per token, so its off path is
// `if mc != nil`, and its on path is one compare-and-swap.
func (t *Turn) BeginModelCall() *ModelCall {
	if t == nil {
		return nil
	}
	t.Emit(Event{Stage: StageModelCall, Status: StatusStart})
	return &ModelCall{t: t, start: time.Now()}
}

// Token records the first token of the call, once.
func (m *ModelCall) Token() {
	if m == nil || !m.first.CompareAndSwap(false, true) {
		return
	}
	m.t.Emit(Event{Stage: StageFirstToken, Status: StatusOK, Dur: time.Since(m.start)})
}

// End writes the call's end line: ok for ClassNone, a failure with the class
// otherwise.
func (m *ModelCall) End(c Class) {
	if m == nil {
		return
	}
	st := StatusOK
	if c != ClassNone {
		st = StatusFail
	}
	m.t.Emit(Event{Stage: StageModelCall, Status: st, Dur: time.Since(m.start), Err: c})
}

// Span is a stage that takes time: a start line when it begins and an ok, fail
// or skip line, with the duration, when it ends.
type Span struct {
	t     *Turn
	stage Stage
	tool  Tool
	call  int
	start time.Time
}

// Begin writes the start line and returns the span, or nil when the turn is
// nil.
func (t *Turn) Begin(stage Stage, tool Tool, call int) *Span {
	if t == nil {
		return nil
	}
	t.Emit(Event{Stage: stage, Status: StatusStart, Tool: tool, Call: call})
	return &Span{t: t, stage: stage, tool: tool, call: call, start: time.Now()}
}

// End writes the end line. The class is written only for a failure.
func (s *Span) End(st Status, c Class) {
	if s == nil {
		return
	}
	s.t.Emit(Event{Stage: s.stage, Status: st, Dur: time.Since(s.start), Tool: s.tool, Call: s.call, Err: c})
}
