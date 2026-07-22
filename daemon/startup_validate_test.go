package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAPIBaseAcceptsUsableEndpoints(t *testing.T) {
	for _, base := range []string{
		"https://openrouter.ai/api/v1",
		"http://127.0.0.1:8099/v1",
		"https://example.com",
		"http://localhost:1234/v1/",
	} {
		if err := validateAPIBase(base); err != nil {
			t.Errorf("validateAPIBase(%q) = %v, want nil", base, err)
		}
	}
}

// The malformed base observed in the Tier 4 reconfirmation: it started the
// daemon, burned three retries with backoff, and was reported to the client as
// a provider outage that was "usually temporary".
func TestValidateAPIBaseRejectsMalformed(t *testing.T) {
	tests := []struct {
		name, base, wantSubstring string
	}{
		{"no scheme", ":::not a url", "scheme"},
		{"bare host", "openrouter.ai/api/v1", "scheme"},
		{"blank", "   ", "blank"},
		{"untrimmed", " https://example.com ", "whitespace"},
		{"wrong scheme", "ftp://example.com", "unsupported scheme"},
		{"file scheme", "file:///etc/passwd", "unsupported scheme"},
		{"scheme but no host", "https://", "no host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAPIBase(tc.base)
			if err == nil {
				t.Fatalf("validateAPIBase(%q) = nil, want an error", tc.base)
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Errorf("error %q does not mention %q", err, tc.wantSubstring)
			}
		})
	}
}

// Fails when neutered: an implementation that returned the raw string without
// stat-ing it would pass the happy path but not these.
func TestValidateWorkspaceRejectsNonWorkspaces(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "calc.go")
	if err := os.WriteFile(file, []byte("package calc\n"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("missing", func(t *testing.T) {
		_, err := validateWorkspace(filepath.Join(dir, "no", "such", "dir"))
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("got %v, want a does-not-exist error", err)
		}
	})

	t.Run("regular file", func(t *testing.T) {
		_, err := validateWorkspace(file)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("got %v, want a not-a-directory error", err)
		}
	})
}

// A real directory with no index is explicitly NOT a startup failure: the
// daemon serves ungrounded prompts in that state and setupRetrieval already
// reports it honestly. Guards against over-tightening this check.
func TestValidateWorkspaceAcceptsUnindexedDirectory(t *testing.T) {
	dir := t.TempDir()
	abs, err := validateWorkspace(dir)
	if err != nil {
		t.Fatalf("validateWorkspace(%q) = %v, want nil", dir, err)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("returned %q, want an absolute path", abs)
	}
}
