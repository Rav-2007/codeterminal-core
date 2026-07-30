package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Structured logging.
//
// The proxy logged in prose: "auth: rejected (key prefix=mk_ab, matches=0)".
// Readable, and impossible to ask a question of -- there was no way to count
// refusals by cause, or to pull one request's trail out of a deploy's worth of
// lines. Now every line is logfmt with a stable key set, so "which gate refused
// this request" and "how often does that gate fire" are the same query shape.
//
// log/slog is stdlib. proxy/go.mod has no third-party requires and this does not
// change that, which for the one internet-facing component of the product is
// worth more than any structured-logging library's extra features.
//
// The key set, used consistently everywhere:
//
//	req_id      the request id (reqid.go); "" outside a request, e.g. in the sweep
//	gate        which fail-closed branch refused, as a machine-readable label
//	key_prefix  the first keyLogPrefixLen chars of a Mochiii key -- NEVER the key
//	key_id      api_keys.id, the authenticated caller (not a secret)
//	status      HTTP status this proxy returned
//	latency_ms  wall time the request occupied
//	reserved / actual / pending_id / delta   the money path
//	scope       which limiter bucket refused
//	provider    the upstream provider that served a stream, when it says
//
// Nothing here changes WHAT is logged with respect to secrets: request and
// response bodies are still never logged, and the full Mochiii key, the Supabase
// service-role key and the OpenRouter key still never appear. That was
// comment-enforced discipline before; logging_test.go now asserts it.

// The gate vocabulary: one label per way this proxy can refuse a request.
//
// These strings are the whole point of the phase. They are what `gate=` carries
// in the log AND what metrics.go counts refusals by, deliberately the same
// constants rather than two parallel lists that drift -- a counter that says
// "auth_row_count fired 400 times" and a log line that says the same thing about
// one request are then answering the same question at two zoom levels.
//
// Naming rule: the label names the BRANCH, not the status code. Six different
// causes all answered 401 in the incident this phase exists for, and "401" was
// exactly the information that turned out to be useless.
const (
	// Admission control (ratelimit.go). The bucket that refused is carried
	// separately as scope=, since the cause is the same in each case.
	gateRateLimited = "rate_limited"

	// authorize's fail-closed branches, in the order they can fire.
	gateNoBearer             = "no_bearer"
	gateEmptyKey             = "empty_key"
	gateSupabaseUnconfigured = "supabase_unconfigured"
	gateAuthRequestBuild     = "auth_request_build"
	gateAuthLookupFailed     = "auth_lookup_failed"
	gateAuthUpstreamStatus   = "auth_upstream_status"
	gateAuthDecodeFailed     = "auth_decode_failed"
	// gateAuthRowCount is the branch the production incident actually hit:
	// Supabase answered 200 with an empty array for a key the user believed was
	// valid. It carries matches= so the log distinguishes "no such key" from
	// "somehow more than one".
	gateAuthRowCount = "auth_row_count"

	// Request-shape and policy gates in handleChatCompletions. These slugs match
	// the error codes the refusal bodies carry, so a caller's error and our
	// counter name the same thing.
	gateMethodNotAllowed = "method_not_allowed"
	gateNotFound         = "not_found"
	gateBadRequestBody   = "bad_request_body"
	gateDuplicateJSONKey = "duplicate_json_key"
	gateCostSurface      = "cost_surface"
	gateZDRRequired      = "zdr_required"
	gateStreamRequired   = "stream_required"
	gateMaxTokens        = "max_tokens_too_large"

	// reserveQuota's fail-closed branches. Kept apart from the auth ones because
	// "we could not reach Supabase" and "this key is out of quota" are the same
	// 429 to the caller and completely different operationally.
	gateQuotaUnconfigured = "quota_unconfigured"
	gateQuotaEncode       = "quota_encode"
	gateQuotaRequestBuild = "quota_request_build"
	gateQuotaCallFailed   = "quota_call_failed"
	gateQuotaStatus       = "quota_status"
	gateQuotaDecode       = "quota_decode"
	gateQuotaDenied       = "quota_denied"

	// The operational surface (metrics.go).
	gateAdminAuth = "admin_auth"

	// Upstream and mid-stream outcomes.
	gateUpstreamRequestBuild = "upstream_request_build"
	gateUpstreamCallFailed   = "upstream_call_failed"
	gateBudgetKill           = "budget_kill"
)

// logLevelEnv names the level knob. Deliberately unlike the daemon, which chose a
// rotating log FILE and explicitly no levels (Tier-4 C3): the proxy has no file
// sink, its stderr goes to a platform log viewer, and the per-request access line
// is the one thing an operator may reasonably want turned down.
const logLevelEnv = "PROXY_LOG_LEVEL"

// logWriterAdapter routes slog records through a *log.Logger.
//
// This is the seam, and it is load-bearing for two reasons rather than one:
//
//  1. Serialization. Every write in this process then goes through that one
//     log.Logger's mutex. Handing slog the raw io.Writer instead would give it a
//     SECOND mutex over the same writer, and two independently-locked writers on
//     one destination is a data race -- immediately visible under `go test -race`
//     with the bytes.Buffer the tests inject.
//  2. Format. log.Logger contributes the "codeterminal-proxy: " prefix and the
//     timestamp, so a slog line is indistinguishable in shape from the startup
//     lines main still writes directly, and slog's own time attr is dropped as
//     redundant (see slogFromLogger).
//
// It also means no constructor signature had to change: everything still takes
// the *log.Logger it always took, and tests capture slog output in the same
// buffer they already passed.
type logWriterAdapter struct{ l *log.Logger }

func (a logWriterAdapter) Write(p []byte) (int, error) {
	// slog hands over exactly one record, newline-terminated; log.Logger adds its
	// own newline, so the trailing one is trimmed rather than doubled.
	a.l.Print(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// slogFromLogger derives a structured logger that writes through l.
//
// Cheap and safe to call more than once (see logWriterAdapter): every derived
// logger funnels into the same serialized destination, so there is no need to
// thread one instance through every constructor.
func slogFromLogger(l *log.Logger) *slog.Logger {
	return slog.New(slog.NewTextHandler(logWriterAdapter{l: l}, &slog.HandlerOptions{
		Level: logLevelFromEnv(),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Drop slog's timestamp: l already stamps every line, and two
			// timestamps per line is worse than none.
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

// logLevelFromEnv reads PROXY_LOG_LEVEL. An unset or unrecognised value is info
// -- a typo must not silently switch the proxy to a quieter level than the
// operator believes they configured, and info is the level everything
// safety-relevant is logged at.
func logLevelFromEnv() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(logLevelEnv))) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// statusRecorder observes the status and size of a response on its way out, so
// the access log can report what the caller actually received.
//
// It MUST forward http.Flusher, and that is not a nicety: streamSSE decides
// whether it can stream by type-asserting w.(http.Flusher). A wrapper that does
// not implement Flush makes that assertion fail, canFlush goes false, and every
// SSE response silently turns into one buffered blob delivered at the end -- the
// proxy would still pass its tests and every stream would feel broken. See
// TestAccessLogPreservesFlushing.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		// net/http's implicit 200 on a first write without WriteHeader.
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.written += int64(n)
	return n, err
}

func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, so future code
// that uses the controller instead of a type assertion keeps working through this
// wrapper.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// accessLog emits one line per request: what was asked, what was returned, how
// long it took.
//
// It sits OUTSIDE recoverPanics (see wrapMiddleware). Inside, a panicking
// request's line would be emitted while unwinding -- before recovery has written
// the 500 -- and would report whatever status had been written so far, i.e. it
// would report the fault as a success.
//
// A status of 0 means the handler returned without writing anything at all, which
// net/http turns into a 200 on the wire; it is logged as 0 rather than guessed at,
// because a handler that wrote nothing is itself worth seeing.
func accessLog(logger *log.Logger, m *metricSet, next http.Handler) http.Handler {
	sl := slogFromLogger(logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		defer func() {
			m.countResponse(rec.status)
			sl.Info("request",
				"req_id", requestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"latency_ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(rec, r)
	})
}
