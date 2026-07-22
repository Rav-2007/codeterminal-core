package main

import (
	"io"
	"log"
	"strings"
	"testing"

	"codeterminal/protocol"
)

func quietServer() *Server {
	return &Server{
		logger: log.New(io.Discard, "", 0),
		cfg:    &Config{Tiers: map[string]ModelTier{}},
	}
}

// A healthy daemon must report nothing, so TokenResponse.Degraded's omitempty
// drops the field entirely and a healthy response stays byte-identical to what
// it was before this existed.
func TestDegradationsSilentWhenHealthy(t *testing.T) {
	s := quietServer()
	s.embedder = &fakeEmbedder{}
	s.store = fixedStore{}
	s.lexicalStore = &fakeLexicalStore{}
	s.memory = &MemoryStore{}

	if got := s.degradations(); len(got) != 0 {
		t.Errorf("degradations() = %v on a healthy daemon, want none", got)
	}
}

// The reconfirmed C2-a case: semantic tier healthy, lexical tier down. This
// used to be reported only to stderr while the wire said {"grounded":true}.
func TestDegradationsReportsLexicalTierDown(t *testing.T) {
	s := quietServer()
	s.embedder = &fakeEmbedder{}
	s.store = fixedStore{}
	s.lexicalStore = nil
	s.memory = &MemoryStore{}

	got := s.degradations()
	if len(got) != 1 || got[0].Component != protocol.DegradedLexicalRetrieval {
		t.Fatalf("degradations() = %v, want exactly one lexical_retrieval entry", got)
	}
	if got[0].Detail == "" {
		t.Error("lexical degradation carries no detail; a client would have nothing to show")
	}
}

// When retrieval is off entirely, GroundingInfo.Reason already carries a
// specific cause. Adding "the lexical half is also down" would be noise about
// a subsystem the user has already been told is not running.
func TestDegradationsSilentAboutLexicalWhenRetrievalFullyOff(t *testing.T) {
	s := quietServer()
	s.embedder = nil
	s.store = nil
	s.lexicalStore = nil
	s.memory = &MemoryStore{}

	for _, d := range s.degradations() {
		if d.Component == protocol.DegradedLexicalRetrieval {
			t.Errorf("reported %q while retrieval is off entirely; GroundingInfo.Reason already covers that", d.Component)
		}
	}
}

func TestDegradationsReportsMemoryUnavailable(t *testing.T) {
	s := quietServer()
	s.embedder = &fakeEmbedder{}
	s.store = fixedStore{}
	s.lexicalStore = &fakeLexicalStore{}
	s.memory = nil

	var found bool
	for _, d := range s.degradations() {
		if d.Component == protocol.DegradedMemory {
			found = true
		}
	}
	if !found {
		t.Errorf("degradations() = %v, want a memory entry when the store is unavailable", s.degradations())
	}
}

// Each ZDR weaken-bool is derived from configuration, which is the only
// honest source: OpenRouter reports which provider served a request but not
// whether a fallback was used, so a per-request claim would be fabricated.
func TestRoutingDegradationsFollowConfig(t *testing.T) {
	tests := []struct {
		name       string
		zdr        ZDRConfig
		wantDetail string
	}{
		{"fallbacks allowed", ZDRConfig{AllowFallbacks: true}, "fallbacks"},
		{"non-zdr allowed", ZDRConfig{AllowNonZDR: true}, "zero-data-retention routing is not enforced"},
		{"collection allowed", ZDRConfig{AllowDataCollection: true}, "store or train"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := quietServer()
			s.cfg = &Config{ZDR: tc.zdr}

			got := s.routingDegradations()
			if len(got) != 1 {
				t.Fatalf("routingDegradations() = %v, want exactly one entry", got)
			}
			if got[0].Component != protocol.DegradedProviderRouting {
				t.Errorf("component = %q, want %q", got[0].Component, protocol.DegradedProviderRouting)
			}
			if !strings.Contains(got[0].Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got[0].Detail, tc.wantDetail)
			}
		})
	}
}

// The secure default (every weaken-bool false, i.e. an absent "zdr" section)
// must report nothing — otherwise a correctly-configured daemon would nag.
func TestRoutingDegradationsSilentOnSecureDefault(t *testing.T) {
	s := quietServer()
	s.cfg = &Config{}
	if got := s.routingDegradations(); len(got) != 0 {
		t.Errorf("routingDegradations() = %v on the secure default, want none", got)
	}
}

// Server literals built directly by tests have no Config; this must not panic,
// matching Server.noScrub's tolerance of the same case.
func TestRoutingDegradationsToleratesNilConfig(t *testing.T) {
	s := &Server{logger: log.New(io.Discard, "", 0)}
	if got := s.routingDegradations(); got != nil {
		t.Errorf("routingDegradations() = %v with a nil config, want nil", got)
	}
}

// Client-facing strings follow the Fix 8 / Gate 7 discipline: they name what
// is reduced and what it costs, never where anything lives.
func TestDegradationDetailsCarryNoPathsOrHosts(t *testing.T) {
	for _, detail := range []string{detailLexicalDown, detailMemoryDown, detailFallbacks, detailNonZDR, detailCollection} {
		if strings.Contains(detail, "/") || strings.Contains(detail, "\\") {
			t.Errorf("detail %q contains a path separator; client-facing text must carry no paths", detail)
		}
		if strings.Contains(detail, "http") {
			t.Errorf("detail %q mentions a URL; client-facing text must carry no hosts", detail)
		}
	}
}
