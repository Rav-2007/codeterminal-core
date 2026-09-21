package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ReconcileWithProxy runs at daemon startup against whatever answers
// MOCHIII_API_BASE, and until this test it decoded that answer straight
// off the wire with no size bound. Every other network read in this process is
// capped -- provider.go reads error bodies under io.LimitReader, webfetch.go
// caps at maxBytes, the proxy has decodeCappedJSON -- and this one was missed
// because the 5s context LOOKS like a bound. It bounds duration, not volume: a
// hostile or compromised proxy on a fast link can push hundreds of megabytes
// into daemon heap well inside five seconds.
//
// There were also no tests for this function at all, in either direction,
// which is why nobody noticed.

func reconcileConfig() *Config {
	return &Config{
		DefaultTier: "fast",
		Tiers: map[string]ModelTier{
			"fast":  {Slug: "vendor/cheap", Active: true},
			"heavy": {Slug: "vendor/expensive", Active: true},
		},
	}
}

// TestReconcileRejectsAnOversizedBody is the cap itself. The oversized field is
// INSIDE the JSON value on purpose: json.Decode stops at the top-level closing
// brace, so trailing padding would never be read and would prove nothing.
func TestReconcileRejectsAnOversizedBody(t *testing.T) {
	const overCap = maxModelStatusBytes + (1 << 20) // comfortably past 1 MiB

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A single enormous slug. Well-formed JSON -- the decode must fail
		// because of the cap, not because the body is garbage.
		fmt.Fprintf(w, `{"allowed_models":["%s"],"unrestricted":false}`, strings.Repeat("a", overCap))
	}))
	defer srv.Close()

	cfg := reconcileConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := cfg.ReconcileWithProxy(ctx, srv.URL, "test-key")
	if err == nil {
		t.Fatalf("a %d-byte body decoded without error: the response is not capped", overCap)
	}
	// The generous context above is deliberate. If this ever fails on a
	// deadline instead of a short read, the cap is gone and the test would
	// otherwise pass for the wrong reason.
	if ctx.Err() != nil {
		t.Fatalf("failed on the context deadline, not the size cap: %v", ctx.Err())
	}

	// And the config must be untouched by a rejected response -- a partially
	// decoded allow-list silently disabling paid tiers is the failure mode that
	// would make this worse than the leak.
	for name, tier := range cfg.Tiers {
		if !tier.Active {
			t.Errorf("tier %q was disabled by a response that failed to decode", name)
		}
	}
}

// TestReconcileAcceptsARealResponse is the other half: the cap must be far
// above anything real. Without this, "cap it at zero" passes the test above.
func TestReconcileAcceptsARealResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want the bearer key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"allowed_models":["vendor/cheap"],"unrestricted":false}`)
	}))
	defer srv.Close()

	cfg := reconcileConfig()
	if err := cfg.ReconcileWithProxy(context.Background(), srv.URL, "test-key"); err != nil {
		t.Fatalf("ReconcileWithProxy: %v", err)
	}
	if !cfg.Tiers["fast"].Active {
		t.Error("an allowed tier was disabled")
	}
	if cfg.Tiers["heavy"].Active {
		t.Error("a tier absent from allowed_models stayed active -- reconcile did nothing")
	}
}

// A response just under the cap must still decode. This is what stops the cap
// from being tightened later to a value that breaks a legitimate allow-list:
// the boundary is asserted, not just the far side of it.
func TestReconcileAcceptsABodyJustUnderTheCap(t *testing.T) {
	// Leave room for the JSON scaffolding around the padded slug.
	padding := strings.Repeat("a", maxModelStatusBytes-1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"allowed_models":["vendor/cheap","%s"],"unrestricted":false}`, padding)
	}))
	defer srv.Close()

	cfg := reconcileConfig()
	if err := cfg.ReconcileWithProxy(context.Background(), srv.URL, "test-key"); err != nil {
		t.Fatalf("a body under the cap was rejected: %v", err)
	}
	if !cfg.Tiers["fast"].Active {
		t.Error("an allowed tier was disabled")
	}
}
