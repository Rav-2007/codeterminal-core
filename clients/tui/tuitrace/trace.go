//go:build trace

package tuitrace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"mochiii/protocol"
)

// maxLineBytes is the schema's line cap, the daemon's too. A line is built from a
// fixed template and values of bounded length, so it is about 200 bytes; the cap
// is checked anyway, because a cap that is only true by construction is one nobody
// notices when the construction changes.
const maxLineBytes = 512

// maxFileBytes bounds one client's trace file. Past it events are dropped, as
// the daemon's recorder does, rather than grow without limit.
const maxFileBytes int64 = 64 << 20

// sink is the process's one trace file, tui-<pid>.jsonl, in the same directory the
// daemon's goes (protocol.SocketDir()/trace). One writer per file: the viewer
// merges the daemon's and this one's by turn id and time.
//
// THE SECOND SWITCH. Begin turns tracing on only when MOCHIII_TRACE is exactly
// "1" in this process's environment; nothing else does, not a flag, a config key
// or a request. Nothing is created until the first line is written, so a client
// that is started with the switch on and asked nothing leaves no trace of one.
type sink struct {
	mu      sync.Mutex
	f       *os.File
	failed  bool
	size    int64
	dropped uint64
}

var (
	activeMu sync.Mutex
	active   *sink
)

// Reset closes the process's trace file and forgets it. It exists for tests.
func Reset() {
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != nil {
		active.mu.Lock()
		if active.f != nil {
			_ = active.f.Close()
		}
		active.mu.Unlock()
		active = nil
	}
}

// Dropped is how many lines were not written because of the size bound or a write
// error.
func Dropped() uint64 {
	activeMu.Lock()
	s := active
	activeMu.Unlock()
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// open makes the directory (0700) and opens the file (0600, no symlink). A refusal
// leaves the recorder silent for the rest of the run: a trace that cannot be
// written safely is not written. The client has no log to say so in (its screen is
// the user's), so it says nothing; the missing file is the signal.
func (s *sink) open() bool {
	parent, err := protocol.SocketDir()
	if err != nil {
		return false
	}
	dir := filepath.Join(parent, "trace")
	if err := ensurePrivateDir(dir); err != nil {
		return false
	}
	path := filepath.Join(dir, "tui-"+strconv.Itoa(os.Getpid())+".jsonl")
	f, err := openNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0) {
		_ = f.Close()
		return false
	}
	s.f, s.size = f, fi.Size()
	return true
}

// ensurePrivateDir creates dir at 0700 or accepts one that is already a real
// directory with no group or other access. It does not chmod: a directory somebody
// else loosened is refused, not repaired. (The daemon's is the same function.)
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

// Turn is one turn's trace: the id the client minted for it and the counter that
// numbers its lines.
type Turn struct {
	s  *sink
	id string

	mu sync.Mutex
	n  int
}

// Begin starts a trace for one turn (a prompt, or a command that stays local), or
// returns nil when tracing is off. The id is random and the client's own: eight
// lower-case hex digits, the shape protocol.ValidTraceID accepts. It is the
// client's to choose, and the daemon uses it as the turn of its own lines.
func Begin() *Turn {
	if os.Getenv("MOCHIII_TRACE") != "1" {
		return nil
	}
	var b [protocol.TraceIDBytes / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil
	}
	id := hex.EncodeToString(b[:])
	if !protocol.ValidTraceID(id) {
		return nil
	}
	activeMu.Lock()
	if active == nil {
		active = &sink{}
	}
	s := active
	activeMu.Unlock()
	return &Turn{s: s, id: id}
}

// ID is the id to send the daemon in protocol.PromptRequest.Trace.
func (t *Turn) ID() protocol.TraceID {
	if t == nil {
		return ""
	}
	return protocol.TraceID(t.id)
}

// Emit writes one line. It is the only way a line is written, and it validates
// every field: a stage or status outside the enums writes nothing; an error class
// appears only on a failure, and a failure without one is "other"; a negative
// duration is zero. The line is built from a fixed template, so what is in it is
// those fields and nothing else.
func (t *Turn) Emit(stage Stage, status Status, dur time.Duration, sub Subject, class Class) {
	if t == nil {
		return
	}
	if stage == stageInvalid || stage >= numStages || status == statusInvalid || status >= numStatuses {
		return
	}
	switch {
	case status != StatusFail:
		class = ClassNone
	case class == ClassNone || class >= numClasses:
		class = ClassOther
	}
	if dur < 0 {
		dur = 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	b := make([]byte, 0, 256)
	b = append(b, `{"v":1,"t":`...)
	b = strconv.AppendInt(b, time.Now().UnixMilli(), 10)
	b = append(b, `,"src":"tui","turn":"`...)
	b = append(b, t.id...)
	b = append(b, `","n":`...)
	b = strconv.AppendInt(b, int64(t.n), 10)
	b = append(b, `,"stage":"`...)
	b = append(b, stageNames[stage]...)
	b = append(b, `","status":"`...)
	b = append(b, statusNames[status]...)
	b = append(b, `","dur_ms":`...)
	b = strconv.AppendInt(b, dur.Milliseconds(), 10)
	b = append(b, `,"tool":"`...)
	b = append(b, sub.s...)
	b = append(b, `","call":0,"err":"`...)
	b = append(b, classNames[class]...)
	b = append(b, `"}`+"\n"...)
	t.s.write(b)
}
