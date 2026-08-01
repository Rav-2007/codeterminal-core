package main

import (
	"reflect"
	"strings"
	"testing"
)

// fakeSecrets holds one syntactically-valid (but not real) example per
// pattern in scrubPatterns, built to satisfy each regex's length/charset
// requirements exactly. None of these are live credentials.
var fakeSecrets = map[string]string{
	"openai_key":           "sk-" + strings.Repeat("a1B2", 6), // 24 chars, >= 20
	"aws_access_key":       "AKIA1234567890ABCDEF",            // AKIA + 16
	"github_token":         "ghp_" + strings.Repeat("x9Y8", 6),
	"slack_token":          "xoxb-1234567890-abcdefghijklmno",
	"google_key":           "AIza" + strings.Repeat("q", 35),
	"supabase_secret":      "sb_secret_" + strings.Repeat("z7", 12),
	"supabase_publishable": "sb_publishable_" + strings.Repeat("z7", 12),
	"mochiii_key":          "mochi_" + strings.Repeat("0a1b2c3d4e", 3), // 30 hex chars
	"private_key_block":    "-----BEGIN PRIVATE KEY-----\nMIIBVwIBADANBgkqhkiG9w0BAQEFAASCAT\n-----END PRIVATE KEY-----",
}

func TestScrub_TruePositives(t *testing.T) {
	for kind, secret := range fakeSecrets {
		t.Run(kind, func(t *testing.T) {
			prompt := "here is my key: " + secret + " please use it"
			cleaned, redactions := scrub(prompt, false)

			if strings.Contains(cleaned, secret) {
				t.Fatalf("cleaned output still contains the raw secret: %q", cleaned)
			}
			want := "[REDACTED:" + kind + "]"
			if !strings.Contains(cleaned, want) {
				t.Fatalf("cleaned = %q, want it to contain %q", cleaned, want)
			}
			if len(redactions) == 0 {
				t.Fatal("expected at least one redaction, got none")
			}
			found := false
			for _, r := range redactions {
				if r.Kind == kind {
					found = true
				}
				// Structural guarantee, not just a convention: Redaction
				// has no field capable of holding matched text.
				if r.Kind == "" {
					t.Fatal("redaction with empty Kind")
				}
			}
			if !found {
				t.Fatalf("redactions = %+v, want a Redaction with Kind=%q", redactions, kind)
			}
		})
	}
}

func TestScrub_FalsePositives(t *testing.T) {
	cases := map[string]string{
		"variable_name_substring": "var skipList []string; skipList = append(skipList, x)",
		"too_short_aws_prefix":    "the value AKIAA appears here, only 5 chars, not a real key",
		"unrelated_base64_blob":   "token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"ssh_public_key_comment":  "# ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKp8x3f9example user@example.com",
		"short_generic_prefix":    "sk-tiny is not long enough to be a real key (only 4 chars after sk-)",
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			cleaned, redactions := scrub(text, false)
			if cleaned != text {
				t.Errorf("cleaned = %q, want unchanged %q", cleaned, text)
			}
			if len(redactions) != 0 {
				t.Errorf("redactions = %+v, want none (false positive)", redactions)
			}
		})
	}
}

func TestScrub_Disabled(t *testing.T) {
	prompt := "here is my key: " + fakeSecrets["openai_key"]
	cleaned, redactions := scrub(prompt, true)

	if cleaned != prompt {
		t.Errorf("cleaned = %q, want unchanged input when disabled", cleaned)
	}
	if redactions != nil {
		t.Errorf("redactions = %+v, want nil when disabled", redactions)
	}
}

func TestScrub_NoMatchesReturnsNilRedactions(t *testing.T) {
	_, redactions := scrub("just an ordinary prompt with no secrets in it", false)
	if redactions != nil {
		t.Errorf("redactions = %+v, want nil when nothing matched", redactions)
	}
}

func TestRedactionKinds(t *testing.T) {
	redactions := []Redaction{{Kind: "openai_key"}, {Kind: "aws_access_key"}}
	kinds := redactionKinds(redactions)
	want := []string{"openai_key", "aws_access_key"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], want[i])
		}
	}
}

// Redaction must not carry offsets, because the ones it used to carry were
// wrong (L6).
//
// scrub applies its patterns in sequence, each rewriting the string for the
// next, so an offset recorded during pattern N indexed a string that no longer
// existed once pattern N+1 had run. This test pins the removal by construction:
// the struct has one field, so there is nowhere for a stale offset to live, and
// a future change that reintroduces one has to delete this test to compile.
//
// The scenario below is the one that produced the wrong numbers — two different
// kinds in one text, where the first replacement changes the length of
// everything after it.
func TestScrub_RedactionCarriesKindOnly(t *testing.T) {
	if n := reflect.TypeOf(Redaction{}).NumField(); n != 1 {
		t.Fatalf("Redaction has %d fields; it must carry Kind and nothing else — offsets computed across scrub's sequential passes are stale by construction", n)
	}

	text := "OPENAI_KEY=sk-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA and AKIAIOSFODNN7EXAMPLE"
	cleaned, redactions := scrub(text, false)

	if len(redactions) < 2 {
		t.Fatalf("expected both kinds to be found, got %v", redactionKinds(redactions))
	}
	if strings.Contains(cleaned, "sk-AAAA") || strings.Contains(cleaned, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("cleaned still carries secret material: %q", cleaned)
	}
	for _, k := range redactionKinds(redactions) {
		if k == "" {
			t.Error("a redaction has an empty Kind")
		}
	}
}
