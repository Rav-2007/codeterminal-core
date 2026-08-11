package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetHeadroom_Unconfigured(t *testing.T) {
	p := &proxy{}
	if hr, ok := p.getHeadroom(context.Background(), "test-key"); ok || hr != 0 {
		t.Errorf("unconfigured proxy returned hr=%d, ok=%v; want 0, false", hr, ok)
	}
}

func TestGetHeadroom_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/usage" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]int64{
			{"tokens_used": 800, "token_limit": 1000},
		})
	}))
	defer ts.Close()

	p := newProxy("sk-test", "http://unused", ts.URL, "sb_secret", log.New(io.Discard, "", 0), nil)
	hr, ok := p.getHeadroom(context.Background(), "key-1")
	if !ok {
		t.Fatalf("getHeadroom failed unexpectedly")
	}
	if hr != 200 {
		t.Errorf("got headroom %d, want 200", hr)
	}
}

func TestGetHeadroom_NegativeHeadroomClamped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]int64{
			{"tokens_used": 1200, "token_limit": 1000},
		})
	}))
	defer ts.Close()

	p := newProxy("sk-test", "http://unused", ts.URL, "sb_secret", log.New(io.Discard, "", 0), nil)
	hr, ok := p.getHeadroom(context.Background(), "key-1")
	if !ok || hr != 0 {
		t.Errorf("got hr=%d, ok=%v; want 0, true", hr, ok)
	}
}

func TestGetHeadroom_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	p := newProxy("sk-test", "http://unused", ts.URL, "sb_secret", log.New(io.Discard, "", 0), nil)
	if hr, ok := p.getHeadroom(context.Background(), "key-1"); ok || hr != 0 {
		t.Errorf("got hr=%d, ok=%v; want 0, false", hr, ok)
	}
}

func TestQuotaTailRetry_SucceedsOnSecondAttempt(t *testing.T) {
	var reserveAttempts atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/v1/api_keys":
			json.NewEncoder(w).Encode([]map[string]string{{"id": "test-key-id"}})
		case "/rest/v1/rpc/reserve_usage":
			att := reserveAttempts.Add(1)
			if att == 1 {
				// First reservation with 4096 tokens fails (exceeds limit)
				w.Write([]byte(`[]`))
			} else {
				// Second reservation with reduced headroom (500) succeeds
				json.NewEncoder(w).Encode([]map[string]any{
					{"tokens_used": 4000, "token_limit": 4000, "pending_id": 99},
				})
			}
		case "/rest/v1/usage":
			// Headroom query returns 500 remaining tokens
			json.NewEncoder(w).Encode([]map[string]int64{
				{"tokens_used": 3500, "token_limit": 4000},
			})
		case "/rest/v1/rpc/apply_correction":
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	up := authTestUpstream()
	defer up.Close()

	p := newProxy("sk-test", up.URL, ts.URL, "sb_secret", log.New(io.Discard, "", 0), nil)

	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(authTestBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 OK on quota tail retry, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := reserveAttempts.Load(); got != 2 {
		t.Errorf("expected 2 reserve_usage attempts, got %d", got)
	}
}
