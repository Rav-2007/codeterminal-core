package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ONE OVERSIZED MESSAGE COSTS ONE CALL, NOT THE SERVER.
//
// This replaces messageLimitReader, which counted the bytes since the last
// newline and, past the limit, failed the whole stream. That kept the memory
// bound -- the SDK's stdio decoder reads a message into one buffer, so the size
// of one message is the size of one allocation -- but it paid for it with the
// connection: a filesystem server asked to read a 5 MB file sent one reply over
// the limit, the stream died, and every tool of that server was gone for the
// turn. The model was told only that "the tool's server is unavailable".
//
// WHAT THIS DOES INSTEAD. The stdio transport is newline-delimited JSON, so a
// message is a line. frameReader holds one line at a time, up to the limit, and
// hands it to the decoder whole. A line that passes the limit is NOT kept:
//
//   - what was held is released, and the rest of the line is read and thrown away
//     as it arrives, so nothing over the limit is ever resident (the heap bound
//     is the limit itself, as before);
//   - while it is thrown away it is watched by a small scanner (frameScanner)
//     that keeps nothing but the top-level "id" and whether there is a "method",
//     and that works in whatever order the keys come -- the TypeScript SDK, which
//     most servers use, writes {"result":...,"jsonrpc":"2.0","id":3}, the id LAST;
//   - in its place the decoder is given a short JSON-RPC error response carrying
//     that id (codeResultTooLarge, with the size and the limit), so the call that
//     was waiting for the message fails with a clear reason and everything else
//     on the connection carries on.
//
// WHEN IT CANNOT BE ANSWERED. A message with no readable id (or one that is not
// a complete object) cannot be matched to a call, and a message with a method is
// a notification or a request FROM the server. A notification is dropped and
// logged. Anything else, and a message that never ends within maxDrainBytes,
// ends the stream exactly as the old reader did: there is then nothing safe to
// answer, and a server that cannot be read is not one to keep reading.
//
// THE FRAMING ASSUMPTION is the spec's and the old reader's: a message is one
// line. A line is delivered when its newline arrives (or at end of stream), and
// not before, because a line cannot be taken back once the decoder has seen it.
type frameReader struct {
	r      io.Reader
	max    int
	server string
	logf   func(format string, args ...any)

	buf   []byte // scratch for one read from r
	frame []byte // the line being held, never more than max bytes of it
	out   []byte // whole lines (and synthesised answers) waiting for the decoder

	skipping bool // the current line passed max: it is being read and dropped
	skipped  int64
	drainCap int64 // how much of one oversized line is read and dropped (maxDrainBytes)
	scan     frameScanner

	err error // sticky: once set, it is what every Read reports after out drains
}

// codeResultTooLarge is the JSON-RPC error code of the answer frameReader writes
// in place of an oversized message. It is in the range the spec leaves to the
// implementation (-32000 to -32099). A server that sends this code itself gets
// nothing but a "too large" notice with the numbers it claims.
const codeResultTooLarge = -32099

// maxDrainBytes bounds how much of one oversized message is read and dropped.
// Dropping costs time, not memory, but a server that never ends a line must not
// be able to keep the reader busy for ever. Far above any real reply (a 5 MB
// file is about 5 MB of JSON) and a fraction of a second at pipe speed.
const maxDrainBytes = 256 << 20

func newFrameReader(r io.Reader, max int, server string, logf func(string, ...any)) *frameReader {
	return &frameReader{r: r, max: max, server: server, logf: logf, buf: make([]byte, 32<<10), drainCap: maxDrainBytes}
}

func (f *frameReader) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		n, err := f.r.Read(f.buf)
		if n > 0 {
			f.consume(f.buf[:n])
		}
		if err != nil && f.err == nil {
			// End of stream. A last line with no newline is still a message.
			if !f.skipping && len(f.frame) > 0 {
				f.out = append(f.out, f.frame...)
			}
			f.frame, f.skipping = nil, false
			f.err = err
		}
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	if len(f.out) == 0 {
		f.out = nil // do not keep a large backing array alive between messages
	}
	return n, nil
}

// consume splits what arrived into lines and feeds each piece to add.
func (f *frameReader) consume(chunk []byte) {
	for len(chunk) > 0 && f.err == nil {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			f.add(chunk, false)
			return
		}
		f.add(chunk[:i+1], true)
		chunk = chunk[i+1:]
	}
}

// add takes one piece of a line; end says it carries the line's newline.
func (f *frameReader) add(seg []byte, end bool) {
	body := len(seg)
	if end {
		body--
	}
	if f.skipping {
		f.skipped += int64(body)
		f.scan.feed(seg)
		if f.skipped > f.drainCap {
			f.fail(fmt.Errorf("%w: server %s sent a message that did not end within %d bytes; "+
				"it was not read further", ErrServerUnavailable, f.server, f.drainCap))
			return
		}
		if end {
			f.finishOversized()
		}
		return
	}
	if len(f.frame)+body > f.max {
		// Over the limit: release what is held and drop the rest as it comes.
		f.skipping = true
		f.skipped = int64(len(f.frame) + body)
		f.scan = frameScanner{}
		f.scan.feed(f.frame)
		f.scan.feed(seg)
		f.frame = nil
		if end {
			f.finishOversized()
		}
		return
	}
	f.room(len(seg))
	f.frame = append(f.frame, seg...)
	if end {
		f.deliverFrame()
	}
}

// room makes sure the held line can take n more bytes without the append that
// follows growing it by the runtime's own steps. Those are small (a quarter at a
// time), so building a 2 MiB line left several times that behind as garbage for
// the collector, which a heap reading counts as much as a live byte. Doubling
// leaves at most about the line's own size, and the buffer is never made larger
// than a message within the limit (and its newline) can need.
func (f *frameReader) room(n int) {
	need := len(f.frame) + n
	if need <= cap(f.frame) {
		return
	}
	c := 2 * cap(f.frame)
	if c < 64<<10 {
		c = 64 << 10
	}
	if c < need {
		c = need
	}
	if top := f.max + 1; c > top && need <= top {
		c = top
	}
	grown := make([]byte, len(f.frame), c)
	copy(grown, f.frame)
	f.frame = grown
}

// deliverFrame moves the held line to the decoder's side. When nothing is
// waiting there the two buffers just swap, so a line up to the limit is not
// copied (and not held twice).
func (f *frameReader) deliverFrame() {
	if len(f.out) == 0 {
		f.out, f.frame = f.frame, nil
		return
	}
	f.out = append(f.out, f.frame...)
	f.frame = nil
}

// finishOversized decides what the decoder is given in place of a dropped line.
func (f *frameReader) finishOversized() {
	f.skipping = false
	size := f.skipped
	id, kind := f.scan.outcome()
	switch kind {
	case frameResponse:
		line, err := tooLargeAnswer(id, size, int64(f.max))
		if err != nil {
			f.fail(fmt.Errorf("%w: server %s sent a message larger than the %d byte limit and its "+
				"answer could not be written: %v", ErrServerUnavailable, f.server, f.max, err))
			return
		}
		f.logfSafe("mcp: server %s sent a %d byte message, over the %d byte limit; it was dropped and "+
			"the call waiting for it was failed (the server is still connected)", f.server, size, f.max)
		f.out = append(f.out, line...)
	case frameFromServer:
		f.logfSafe("mcp: server %s sent a %d byte notification or request, over the %d byte limit; "+
			"it was dropped", f.server, size, f.max)
	default:
		f.fail(fmt.Errorf("%w: server %s sent a single message larger than the %d byte limit that "+
			"could not be matched to a call (%d bytes); reading it in full is how an unconfined "+
			"subprocess exhausts this daemon's memory", ErrServerUnavailable, f.server, f.max, size))
	}
}

func (f *frameReader) fail(err error) {
	f.err = err
	f.frame, f.skipping = nil, false
}

func (f *frameReader) logfSafe(format string, args ...any) {
	if f.logf != nil {
		f.logf(format, args...)
	}
}

// tooLargeAnswer is the response written in place of an oversized one: the same
// id, no result, an error the client turns into ResultTooLargeError.
func tooLargeAnswer(id json.RawMessage, size, limit int64) ([]byte, error) {
	data, err := json.Marshal(map[string]int64{"size": size, "limit": limit})
	if err != nil {
		return nil, err
	}
	line, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}{
			Code:    codeResultTooLarge,
			Message: fmt.Sprintf("the message was %d bytes and the limit is %d bytes", size, limit),
			Data:    data,
		},
	})
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

type frameKind int

const (
	frameUnknown    frameKind = iota // not a message we can answer or safely ignore
	frameResponse                    // a reply to a request, with a usable id
	frameFromServer                  // a notification or request the server started
)

// frameScanner reads one JSON-RPC message as it goes by and remembers only what
// is needed to answer it: the top-level "id" and whether there is a top-level
// "method". It holds a few dozen bytes whatever the message's size, and does not
// care in what order the keys come.
//
// It is not a validator. It tracks strings, escapes and nesting well enough to
// know which key it is at; anything it does not understand sets bad, and bad
// means "cannot be answered", which is the old behaviour.
type frameScanner struct {
	started, done, bad bool
	depth              int

	inStr, esc bool

	// A string at depth 1 is a key when expectKey; its first bytes are kept to
	// tell "id" and "method" from the rest.
	expectKey bool
	inKey     bool
	key       [8]byte
	keyLen    int
	keyOver   bool // longer than any key we want, or escaped: not one of them
	pending   string
	want      string // "id" or "method": the key whose value is being read
	awaitVal  bool

	capStr, capScalar bool
	tok               [64]byte
	tokLen            int
	tokOver           bool

	id        json.RawMessage
	hasMethod bool
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

func (s *frameScanner) feed(b []byte) {
	for _, c := range b {
		if s.bad {
			return
		}
		switch {
		case s.inStr:
			s.inString(c)
		case s.done:
			if !isJSONSpace(c) {
				s.bad = true
			}
		default:
			s.outside(c)
		}
	}
}

func (s *frameScanner) outside(c byte) {
	if s.capScalar {
		if isJSONSpace(c) || c == ',' || c == '}' || c == ']' {
			s.endValue()
		} else {
			s.addTok(c)
			return
		}
	}
	if isJSONSpace(c) {
		return
	}
	if !s.started {
		s.started = true
		if c != '{' {
			s.bad = true
			return
		}
	}
	switch c {
	case '{', '[':
		s.depth++
		if s.depth == 1 {
			s.expectKey = true
		} else if s.depth == 2 && s.awaitVal {
			s.awaitVal = false // the value of id/method is a container: not usable
			if s.want == "id" {
				s.bad = true
			}
			s.want = ""
		}
	case '}', ']':
		s.depth--
		if s.depth == 0 {
			s.done = true
		} else if s.depth < 0 {
			s.bad = true
		}
	case '"':
		s.inStr, s.esc = true, false
		switch {
		case s.depth == 1 && s.expectKey:
			s.inKey, s.keyLen, s.keyOver, s.expectKey = true, 0, false, false
		case s.depth == 1 && s.awaitVal:
			s.awaitVal = false
			if s.want != "" {
				s.capStr, s.tokLen, s.tokOver = true, 0, false
				s.addTok('"')
			}
		}
	case ':':
		if s.depth == 1 {
			s.want, s.awaitVal = s.pending, true
			s.pending = ""
		}
	case ',':
		if s.depth == 1 {
			s.expectKey, s.want, s.awaitVal = true, "", false
		}
	default:
		if s.depth == 1 && s.awaitVal {
			s.awaitVal = false
			if s.want != "" {
				s.capScalar, s.tokLen, s.tokOver = true, 0, false
				s.addTok(c)
			}
		}
	}
}

func (s *frameScanner) inString(c byte) {
	if s.esc {
		s.esc = false
		if s.capStr {
			s.addTok(c)
		}
		return
	}
	switch c {
	case '\\':
		s.esc = true
		if s.inKey {
			s.keyOver = true
		}
		if s.capStr {
			s.addTok(c)
		}
	case '"':
		s.inStr = false
		switch {
		case s.inKey:
			s.inKey = false
			s.pending = ""
			if !s.keyOver {
				switch string(s.key[:s.keyLen]) {
				case "id":
					s.pending = "id"
				case "method":
					s.pending = "method"
				}
			}
		case s.capStr:
			s.addTok('"')
			s.capStr = false
			s.endValue()
		}
	default:
		if s.inKey {
			if s.keyLen < len(s.key) {
				s.key[s.keyLen] = c
				s.keyLen++
			} else {
				s.keyOver = true
			}
		}
		if s.capStr {
			s.addTok(c)
		}
	}
}

func (s *frameScanner) addTok(c byte) {
	if s.tokLen < len(s.tok) {
		s.tok[s.tokLen] = c
		s.tokLen++
	} else {
		s.tokOver = true
	}
}

// endValue closes the scalar or string being captured for id or method.
func (s *frameScanner) endValue() {
	s.capScalar = false
	switch s.want {
	case "id":
		if !s.tokOver && string(s.tok[:s.tokLen]) != "null" {
			s.id = append(json.RawMessage(nil), s.tok[:s.tokLen]...)
		}
	case "method":
		s.hasMethod = true
	}
	s.want = ""
}

// outcome says what the message was, once all of it has been fed.
func (s *frameScanner) outcome() (id json.RawMessage, kind frameKind) {
	if s.bad || !s.started || !s.done || s.depth != 0 {
		return nil, frameUnknown
	}
	if s.hasMethod {
		return nil, frameFromServer
	}
	if len(s.id) == 0 || !json.Valid(s.id) {
		return nil, frameUnknown
	}
	// An id is a number or a string; nothing else could have been captured, but
	// the token is copied into a message of ours, so it is checked, not assumed.
	if c := s.id[0]; c != '"' && c != '-' && (c < '0' || c > '9') {
		return nil, frameUnknown
	}
	return s.id, frameResponse
}
