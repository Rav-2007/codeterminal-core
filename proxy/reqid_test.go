package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidRequestID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"minted shape", "0123456789abcdef", true},
		{"minimum length", "00000000", true},
		{"maximum length", strings.Repeat("a", maxInboundRequestIDLen), true},
		{"too short", "0123456", false},
		{"too long", strings.Repeat("a", maxInboundRequestIDLen+1), false},
		{"empty", "", false},
		{"uppercase hex", "0123456789ABCDEF", false},
		{"not hex", "hello-world-1234", false},
		// The three that matter. Each is a channel a looser charset would open,
		// and each must be refused so the id can be echoed into a JSON error body
		// and a log line without further escaping.
		{"newline (log forging)", "0123456\n789abcdef", false},
		{"logfmt key (log forging)", "abc=def012345678", false},
		{"a daemon classifier needle", "quota_exceeded00", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validRequestID(tc.id); got != tc.want {
				t.Errorf("validRequestID(%q) = %t, want %t", tc.id, got, tc.want)
			}
		})
	}
}

// TestNewRequestID pins the property validRequestID's charset argument rests on:
// a minted id is itself always acceptable, so adopting an inbound id can never
// admit a shape we would not have produced ourselves.
func TestNewRequestID(t *testing.T) {
	first := newRequestID()
	if len(first) != requestIDBytes*2 {
		t.Errorf("minted id %q has length %d, want %d hex chars", first, len(first), requestIDBytes*2)
	}
	if !validRequestID(first) {
		t.Errorf("minted id %q is not accepted by validRequestID; the two must agree", first)
	}
	if second := newRequestID(); second == first {
		t.Errorf("two minted ids are identical (%q); correlation would be worthless", first)
	}
}

func TestWithRequestID(t *testing.T) {
	// seen captures what the wrapped handler observed on its context, so the test
	// asserts the id the HANDLER will log matches the one the CLIENT is told.
	newProbe := func(seen *string) http.Handler {
		return withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*seen = requestIDFrom(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	}

	t.Run("mints an id when the caller sends none", func(t *testing.T) {
		var seen string
		rec := httptest.NewRecorder()
		newProbe(&seen).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		header := rec.Header().Get(requestIDHeader)
		if !validRequestID(header) {
			t.Errorf("response %s = %q, want a minted id", requestIDHeader, header)
		}
		if seen != header {
			t.Errorf("handler saw req_id %q but the client was told %q; a quoted id would "+
				"resolve to nothing", seen, header)
		}
	})

	t.Run("adopts a well-formed inbound id", func(t *testing.T) {
		var seen string
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(requestIDHeader, "cafebabe12345678")
		rec := httptest.NewRecorder()
		newProbe(&seen).ServeHTTP(rec, req)

		if seen != "cafebabe12345678" {
			t.Errorf("inbound id was not adopted: handler saw %q", seen)
		}
		if got := rec.Header().Get(requestIDHeader); got != "cafebabe12345678" {
			t.Errorf("inbound id was not echoed: %s = %q", requestIDHeader, got)
		}
	})

	// The security property, not a formatting preference: a rejected id must be
	// REPLACED, never sanitized-and-kept, so nothing a caller chose reaches the
	// log or the error body.
	t.Run("replaces a malformed inbound id instead of echoing it", func(t *testing.T) {
		for _, hostile := range []string{
			"quota_exceeded",
			"id\nmsg=\"auth ok\"",
			"BADCAFE12345678",
			"x",
		} {
			var seen string
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(requestIDHeader, hostile)
			rec := httptest.NewRecorder()
			newProbe(&seen).ServeHTTP(rec, req)

			if seen == hostile {
				t.Errorf("hostile inbound id %q was adopted", hostile)
			}
			if !validRequestID(seen) {
				t.Errorf("replacement for %q is itself malformed: %q", hostile, seen)
			}
			if got := rec.Header().Get(requestIDHeader); got != seen {
				t.Errorf("header %q disagrees with the handler's id %q", got, seen)
			}
		}
	})

	// Ordering assertion: withRequestID sits OUTSIDE recoverPanics precisely so
	// the response a caller can least explain still carries an id. If the two are
	// ever swapped, the header is written after the panic has aborted the handler
	// and this fails.
	t.Run("id survives the panic path", func(t *testing.T) {
		var logs syncBuffer
		h := withRequestID(recoverPanics(log.New(&logs, "", 0), newMetrics(),
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				panic("synthetic fault for the request-id ordering test")
			})))

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if got := rec.Header().Get(requestIDHeader); !validRequestID(got) {
			t.Errorf("the 500 carried no usable %s (%q), so the caller cannot quote "+
				"the request whose stack we logged", requestIDHeader, got)
		}
	})

	t.Run("requestIDFrom is empty outside a request", func(t *testing.T) {
		// The nil context IS the case under test: requestIDFrom is called from
		// logging paths that can run before a context exists, so it must answer
		// rather than panic. Passing nil is the assertion, not an oversight.
		//lint:ignore SA1012 deliberate nil-context probe; see above
		if got := requestIDFrom(nil); got != "" {
			t.Errorf("requestIDFrom(nil) = %q, want empty", got)
		}
		if got := requestIDFrom(t.Context()); got != "" {
			t.Errorf("requestIDFrom(bare ctx) = %q, want empty", got)
		}
	})
}

// TestNewHandlerWiring asserts the ordering on the handler MAIN actually serves,
// not on a composition this test chose for itself.
//
// The distinction is the whole point. TestWithRequestID's panic subtest proves
// "withRequestID outside recoverPanics carries an id"; it would keep passing if
// main.go were wired the other way round. That is the P1.2b failure mode -- safety
// wiring living where no test can reach it -- so newHandler exists to be reached.
func TestNewHandlerWiring(t *testing.T) {
	var logs syncBuffer
	p := newProxy("k", "http://unused.invalid", "", "", log.New(&logs, "", 0), nil)
	h := newHandler(p, log.New(&logs, "", 0), "test-commit", "")

	t.Run("the catch-all 404 carries an id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-route", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if got := rec.Header().Get(requestIDHeader); !validRequestID(got) {
			t.Errorf("404 carried no usable %s: %q", requestIDHeader, got)
		}
	})

	t.Run("/health passes through the middlewares", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get(requestIDHeader); !validRequestID(got) {
			t.Errorf("/health carried no usable %s: %q", requestIDHeader, got)
		}
	})

	// The ordering assertion with teeth: wrapMiddleware is the exact wrapping main
	// serves, so swapping the two middlewares there fails HERE. No real route
	// panics, which is why the inner handler is injected rather than routed to.
	t.Run("main's own wrapping carries an id on the panic path", func(t *testing.T) {
		var panicLogs syncBuffer
		wrapped := wrapMiddleware(log.New(&panicLogs, "", 0), newMetrics(),
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				panic("synthetic fault through main's real middleware stack")
			}))

		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, chatCompletionsPath, nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		id := rec.Header().Get(requestIDHeader)
		if !validRequestID(id) {
			t.Fatalf("the 500 main would serve carries no usable %s (%q)", requestIDHeader, id)
		}
		if !strings.Contains(panicLogs.String(), "PANIC recovered") {
			t.Errorf("the fault was not logged: %q", panicLogs.String())
		}
		// The assertion that actually detects a swap. The HEADER survives either
		// order (withRequestID sets it before the handler panics, and a header set
		// before WriteHeader is still sent) -- so asserting on the header alone was
		// inert, and this test said so only after the neuter check proved it. What
		// does NOT survive is recoverPanics being able to READ the id: inside
		// withRequestID it holds the outer request, whose context never had one, and
		// the stack becomes uncorrelatable.
		if !strings.Contains(panicLogs.String(), "req_id="+id) {
			t.Errorf("the panic log does not carry the id the caller was given (%s); "+
				"the stack cannot be tied to the 500. Log: %q", id, panicLogs.String())
		}
	})

	// A GET to the completions route reaches handleChatCompletions and is refused
	// there, which is the shortest path through the real route table to a real
	// refusal body.
	t.Run("a refusal from the real route table carries a matching id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, chatCompletionsPath, nil))

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		var body refusalBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("405 body is not JSON (%v): %q", err, rec.Body.String())
		}
		if body.RequestID == "" || body.RequestID != rec.Header().Get(requestIDHeader) {
			t.Errorf("body request_id %q does not match header %q", body.RequestID,
				rec.Header().Get(requestIDHeader))
		}
	})
}

// TestRefusalBodyCarriesRequestID drives the real handler's 401 branch through the
// real middleware, because that is the shape of the open production incident: a
// caller gets a 401 and needs one string that ties their failure to our log.
func TestRefusalBodyCarriesRequestID(t *testing.T) {
	// Supabase answering 200 [] is the exact production symptom -- a key that
	// looks valid to the user, matching no row here.
	supabase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer supabase.Close()

	var logs syncBuffer
	p := newProxy("k", "http://unused.invalid", supabase.URL, "sr", log.New(&logs, "", 0), nil)
	h := withRequestID(http.HandlerFunc(p.handleChatCompletions))

	req := httptest.NewRequest(http.MethodPost, chatCompletionsPath, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer mochi-key-that-matches-nothing")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	var body refusalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body is not JSON (%v): %q", err, rec.Body.String())
	}
	if body.Error != "unauthorized" {
		t.Errorf("body error = %q, want unauthorized", body.Error)
	}
	if body.RequestID == "" || body.RequestID != rec.Header().Get(requestIDHeader) {
		t.Errorf("body request_id %q does not match header %q", body.RequestID,
			rec.Header().Get(requestIDHeader))
	}
}

// TestRefusalBodyNeverEchoesAHostileID is the injection half of the charset
// decision. The daemon classifies proxy errors by substring-matching the body
// (daemon/modelerror.go), so an id a caller chose must not be able to spell one of
// those needles into the body it lands in.
func TestRefusalBodyNeverEchoesAHostileID(t *testing.T) {
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRefusal(w, http.StatusForbidden, "zdr_required", requestIDFrom(r.Context()))
	}))

	req := httptest.NewRequest(http.MethodPost, chatCompletionsPath, nil)
	req.Header.Set(requestIDHeader, "insufficient_quota")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "insufficient_quota") {
		t.Errorf("a caller-chosen id reached the error body, where the daemon's "+
			"classifier reads it: %q", rec.Body.String())
	}
}

// TestTooManyRequestsCarriesRequestID keeps the 429s in the same regime as the
// other refusals: they were the responses most likely to be reported ("it just
// stopped working") and had nothing to quote.
func TestTooManyRequestsCarriesRequestID(t *testing.T) {
	rec := httptest.NewRecorder()
	tooManyRequests(rec, "key_rate", "0123456789abcdef")

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != retryAfterValue {
		t.Errorf("Retry-After = %q, want %q", got, retryAfterValue)
	}

	var body refusalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON (%v): %q", err, rec.Body.String())
	}
	if body.Error != "rate_limited" || body.Scope != "key_rate" {
		t.Errorf("429 body lost its shape: %+v", body)
	}
	if body.RequestID != "0123456789abcdef" {
		t.Errorf("429 body request_id = %q, want the id it was given", body.RequestID)
	}
}
