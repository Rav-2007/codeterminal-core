package mcp

import (
	"fmt"
	"io"
	"mime"
)

// THE SIZE BOUND ON A REMOTE SERVER'S RESPONSE, and why it is not the stdio one.
//
// A subprocess speaks newline-delimited JSON, so "bytes since the last newline"
// is exactly the size of the message being read (messageLimitReader), and that
// is the bound used for it. An HTTP response is different in kind. The body of
// an application/json answer is ONE message in which a newline is just
// whitespace between tokens, and a server can put as many of them as it likes:
// a line-counting bound saw 5 MiB, a newline every thousand bytes, as a few
// thousand short lines and let it through against a 1 MiB limit (flaw F4).
//
// So an HTTP response is bounded by what it IS:
//   - application/json (and anything else): the whole body is one message, so
//     the whole body is bounded -- totalLimitReader.
//   - text/event-stream: a stream of events, each one message. The stream as a
//     whole must be free to run (a long session of small events is ordinary) so
//     each EVENT is bounded -- eventLimitReader -- where an event ends at an
//     empty line, as the SDK's own parser ends it (mcp/event.go: lines split at
//     "\n", an empty line closes the event). A message spread over many
//     data: lines is still one event and is counted whole.

// boundedBody wraps a response body in the bound that fits its content type.
func boundedBody(body io.Reader, contentType string, limit int, server string) io.Reader {
	if media, _, err := mime.ParseMediaType(contentType); err == nil && media == "text/event-stream" {
		return &eventLimitReader{r: body, max: limit, server: server, lineBlank: true}
	}
	return &totalLimitReader{r: body, max: limit, server: server}
}

// totalLimitReader fails the stream when the body as a whole exceeds max bytes.
// Once tripped it stays tripped.
type totalLimitReader struct {
	r       io.Reader
	max     int
	server  string
	read    int
	tripped bool
}

func (l *totalLimitReader) Read(p []byte) (int, error) {
	if l.tripped {
		return 0, l.err()
	}
	n, err := l.r.Read(p)
	l.read += n
	if l.read > l.max {
		l.tripped = true
		return 0, l.err()
	}
	return n, err
}

func (l *totalLimitReader) err() error {
	return fmt.Errorf("%w: server %s sent a response larger than the %d byte limit; "+
		"reading it in full is how an unconfined server exhausts this daemon's memory",
		ErrServerUnavailable, l.server, l.max)
}

// eventLimitReader fails the stream when ONE event exceeds max bytes.
//
// It counts every byte since the last event boundary, newlines and field names
// included -- that is what the SDK buffers -- so a message split over any number
// of data: lines is bounded by its total, and a very long single line is bounded
// too. The boundary rule is the SDK's: a line is blank if it holds nothing but
// carriage returns, and a newline that ends a blank line ends the event.
type eventLimitReader struct {
	r         io.Reader
	max       int
	server    string
	sinceEvt  int
	lineBlank bool // every byte of the current line so far is '\r'
	tripped   bool
}

func (l *eventLimitReader) Read(p []byte) (int, error) {
	if l.tripped {
		return 0, l.err()
	}
	n, err := l.r.Read(p)
	for _, b := range p[:n] {
		l.sinceEvt++
		switch b {
		case '\n':
			if l.lineBlank {
				l.sinceEvt = 0
			}
			l.lineBlank = true
		case '\r':
		default:
			l.lineBlank = false
		}
		if l.sinceEvt > l.max {
			l.tripped = true
			return 0, l.err()
		}
	}
	return n, err
}

func (l *eventLimitReader) err() error {
	return fmt.Errorf("%w: server %s sent a single event larger than the %d byte limit; "+
		"reading it in full is how an unconfined server exhausts this daemon's memory",
		ErrServerUnavailable, l.server, l.max)
}
