package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// Per-request identity.
//
// The proxy had none, and that is the specific reason a production incident is
// still open: a user reported a valid key being 401'd, and nothing connected
// their failed request to any of the log lines authorize() writes. Every
// fail-closed branch was already logged -- there was just no way to tell WHICH
// request, out of everything in the log, was theirs.
//
// So every response now carries an X-Request-Id, every log line for that request
// carries the same id as req_id, and every JSON error body repeats it. A user can
// quote one string and it resolves to one request's trail.

// requestIDHeader is both the inbound (optional) and outbound (always) header.
const requestIDHeader = "X-Request-Id"

// requestIDBytes is the entropy of a minted id: 8 bytes -> 16 hex chars. Enough
// that two concurrent requests colliding is not a practical concern; short enough
// that a user can read one out over a support channel.
const requestIDBytes = 8

// minInboundRequestIDLen is the shortest client-supplied id accepted. A 1-char
// id would "work" but is useless for correlation and trivially collides with
// every other caller's.
const minInboundRequestIDLen = 8

// maxInboundRequestIDLen bounds what a caller can make us log and echo. Ids are
// written to the log and into error bodies, so an unbounded one is a caller
// deciding how much of our log a single request occupies.
const maxInboundRequestIDLen = 64

// requestIDContextKey is the context key for the current request's id. An
// unexported struct type, so no other package can collide with it and nothing
// outside this file can plant a value.
type requestIDContextKey struct{}

// newRequestID mints a lowercase-hex id.
//
// crypto/rand.Read is documented never to return an error (it panics internally
// if the system entropy source fails), so there is no error branch to write here
// -- and an id is not the place to invent a fallback that weakens the property
// validRequestID depends on.
func newRequestID() string {
	b := make([]byte, requestIDBytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// validRequestID reports whether an inbound X-Request-Id may be adopted.
//
// The charset is deliberately narrow -- lowercase hex only, exactly the shape
// newRequestID mints -- and the reason is a real injection channel rather than
// tidiness:
//
//  1. The id is echoed into JSON error bodies, and the DAEMON classifies proxy
//     errors by substring-matching those bodies (daemon/modelerror.go's
//     quotaPhrases and contextLengthPhrases). Every needle there contains a
//     non-hex letter ("billing", "quota_exceeded", "context length", ...), so a
//     hex id provably cannot spell one. A general "safe characters" charset would
//     hand a caller the ability to steer how its own error is classified.
//  2. The id is written to the log. Hex excludes newlines and '=' by
//     construction, so it cannot forge a log line or a logfmt key.
//
// A rejected header is not an error: the request proceeds with a minted id. The
// caller loses only its own correlation, which is its own choice to make.
func validRequestID(s string) bool {
	if len(s) < minInboundRequestIDLen || len(s) > maxInboundRequestIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// requestIDFrom returns the id withRequestID put on ctx, or "" if there is none.
//
// The empty return is not an error path to guard against: it means the caller is
// outside a request (a background sweep, a test calling a handler directly), and
// a log line with no req_id is the honest record of that.
func requestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// withRequestID adopts or mints an id for every request, before anything else runs.
//
// It is installed OUTERMOST, outside recoverPanics -- so the panic 500, the
// pre-auth 429s and the catch-all 404 all carry an id too. Those are exactly the
// responses a caller cannot otherwise explain, and an id that only appeared on
// the paths that already work would be an id you cannot ask about.
//
// The response header is set BEFORE next runs, which is what makes it survive
// the streaming and panic paths: once a handler has written a status (or a panic
// has aborted mid-stream) the header map is no longer mutable, so setting it
// afterwards would silently do nothing on the responses that need it most.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}
