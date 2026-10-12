package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FAKE CREDENTIALS ONLY. These never were and never will be real keys. The
// 48-char bare one stands in for a DeepSeek `sk-` key (the heuristic catches the
// raw form, but this file is about the ENCODINGS it does not).
const (
	fakeCred     = "sk-FAKE0123456789abcdefABCDEFghijklmnopqrstuvQZ" // 47 chars, bare sk-
	fakeOpaque   = "ENVMARKER7f3a91c2b4d6e8f0a1c3e5d79bktpxzq"       // no known prefix
	fakeTooShort = "sk-abcdef1234"                                   // < credMinLen, must NOT register
)

// b64, b64url, hexLower, hexUpper, rev, reverse the raw cred the way the forms do.
func revStr(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// encodedForms returns, by label, every way the scrubber should recognise raw.
func encodedForms(raw string) map[string]string {
	return map[string]string{
		"raw":          raw,
		"b64std":       base64.StdEncoding.EncodeToString([]byte(raw)),
		"b64std_nopad": base64.RawStdEncoding.EncodeToString([]byte(raw)),
		"b64url":       base64.URLEncoding.EncodeToString([]byte(raw)),
		"b64url_nopad": base64.RawURLEncoding.EncodeToString([]byte(raw)),
		"hex_lower":    hex.EncodeToString([]byte(raw)),
		"hex_upper":    strings.ToUpper(hex.EncodeToString([]byte(raw))),
		"urlenc_query": url.QueryEscape(raw),
		"urlenc_path":  url.PathEscape(raw),
		"reversed":     revStr(raw),
	}
}

func TestCredScrub_RedactsEveryEncodedForm(t *testing.T) {
	cs := buildCredScrubber([]string{fakeCred})
	if cs == nil {
		t.Fatal("no scrubber built for a real-length credential")
	}
	for label, form := range encodedForms(fakeCred) {
		t.Run(label, func(t *testing.T) {
			text := "prefix text " + form + " suffix text"
			out, n := cs.redact(text)
			if n == 0 || strings.Contains(out, form) {
				t.Errorf("form %q (%q) survived redaction: n=%d out=%q", label, form, n, out)
			}
			if !strings.Contains(out, credPlaceholder) {
				t.Errorf("no placeholder written for form %q: %q", label, out)
			}
			// The surrounding text is preserved.
			if !strings.HasPrefix(out, "prefix text ") || !strings.HasSuffix(out, " suffix text") {
				t.Errorf("redaction corrupted the surrounding text: %q", out)
			}
		})
	}
}

// A key split across lines (or fields) into pieces that are each shorter than
// the whole but at least credFragmentWindow long is caught by the fragment rule.
func TestCredScrub_CatchesAKeySplitAcrossLines(t *testing.T) {
	cs := buildCredScrubber([]string{fakeCred})
	half := len(fakeCred) / 2
	first, second := fakeCred[:half], fakeCred[half:] // each ~23 chars, > window
	text := "line one has " + first + "\nline two has " + second + "\n"
	out, n := cs.redact(text)
	if n < 2 {
		t.Errorf("a split key was not caught on both halves: n=%d out=%q", n, out)
	}
	if strings.Contains(out, first) || strings.Contains(out, second) {
		t.Errorf("a half of the split key survived: %q", out)
	}
	// A piece SHORTER than the window is not a fragment match (no false alarm on
	// a short common run).
	tiny := fakeCred[:credFragmentWindow-1]
	if _, n := cs.redact("just " + tiny + " here"); n != 0 {
		t.Errorf("a sub-window fragment was redacted (%q), which would be a false positive", tiny)
	}
}

func TestCredScrub_ScanReportsFormsNotValues(t *testing.T) {
	cs := buildCredScrubber([]string{fakeCred})
	hit, labels := cs.scan("see " + base64.StdEncoding.EncodeToString([]byte(fakeCred)) + " now")
	if !hit {
		t.Fatal("scan missed a base64 form")
	}
	for _, l := range labels {
		if strings.Contains(fakeCred, l) || len(l) > 20 {
			t.Errorf("a label leaked key material: %q", l)
		}
	}
	if hit, _ := cs.scan("nothing secret here, just ordinary words"); hit {
		t.Error("scan reported a hit on clean text")
	}
}

// The matcher tracks the CURRENT credential: a swap (as /connect does via
// setProvider -> rebuild) stops matching the old value and starts on the new.
func TestCredScrub_RebuildTracksTheCurrentCredential(t *testing.T) {
	keyA, keyB := fakeCred, fakeOpaque
	csA := buildCredScrubber([]string{keyA})
	if _, n := csA.redact("holding " + keyA); n == 0 {
		t.Fatal("key A not matched by its own scrubber")
	}
	csB := buildCredScrubber([]string{keyB})
	if _, n := csB.redact("holding " + keyA); n != 0 {
		t.Error("key A is still matched after a rebuild for key B -- the registry did not update")
	}
	if _, n := csB.redact("holding " + keyB); n == 0 {
		t.Error("key B not matched after the rebuild")
	}
}

func TestCredScrub_NilAndShortAreNoOps(t *testing.T) {
	// No credentials -> nil scrubber -> every op a no-op.
	if cs := buildCredScrubber(nil); cs != nil {
		t.Error("a scrubber was built from no values")
	}
	if cs := buildCredScrubber([]string{"", "   "}); cs != nil {
		t.Error("blank values built a scrubber")
	}
	if cs := buildCredScrubber([]string{fakeTooShort}); cs != nil {
		t.Errorf("a too-short value (%q, < credMinLen) was registered", fakeTooShort)
	}
	var nilCS *credScrubber
	if out, n := nilCS.redact("anything " + fakeCred); n != 0 || out != "anything "+fakeCred {
		t.Error("nil scrubber mutated text")
	}
	if hit, _ := nilCS.scan("anything " + fakeCred); hit {
		t.Error("nil scrubber reported a hit")
	}
}

func TestCredScrub_MultipleCredentialsAndOverlapMerge(t *testing.T) {
	cs := buildCredScrubber([]string{fakeCred, fakeOpaque})
	text := fmt.Sprintf("a=%s b=%s", fakeCred, fakeOpaque)
	out, n := cs.redact(text)
	if strings.Contains(out, fakeCred) || strings.Contains(out, fakeOpaque) {
		t.Errorf("a registered credential survived: %q", out)
	}
	// The raw form and its many fragment windows all match; merging means one
	// placeholder per occurrence, not dozens.
	if n != 2 {
		t.Errorf("overlapping matches were not merged into one span each: n=%d", n)
	}
	if c := strings.Count(out, credPlaceholder); c != 2 {
		t.Errorf("expected 2 placeholders, got %d: %q", c, out)
	}
}

// THE FRAGMENT WINDOW'S FALSE-POSITIVE RATE, MEASURED on this repo's own corpus.
// For several random 48-char base62 keys, generate their N-byte windows and scan
// every .go, .md and .json file under the module (the test outputs and fixtures
// the brief asks about included) for an accidental match. Reports the count per
// window size so the choice of credFragmentWindow is evidence, not assertion.
func TestCredFragment_FalsePositiveRateAtWindowSizes(t *testing.T) {
	corpus := readModuleCorpus(t)
	t.Logf("corpus: %d files, %d bytes", corpus.files, len(corpus.text))

	const keys = 8
	randomKeys := make([]string, keys)
	for i := range randomKeys {
		randomKeys[i] = randomBase62(t, 48)
	}
	for _, n := range []int{8, 12, 16} {
		fp := 0
		for _, k := range randomKeys {
			windows := map[string]bool{}
			for i := 0; i+n <= len(k); i++ {
				windows[k[i:i+n]] = true
			}
			for w := range windows {
				fp += strings.Count(corpus.text, w)
			}
		}
		t.Logf("window=%d: %d false-positive match(es) across %d random keys", n, fp, keys)
		if n == credFragmentWindow && fp != 0 {
			t.Errorf("credFragmentWindow=%d has %d false positive(s) on the repo corpus; raise it", n, fp)
		}
	}
}

type corpusText struct {
	text  string
	files int
}

func readModuleCorpus(t *testing.T) corpusText {
	t.Helper()
	var b strings.Builder
	files := 0
	for _, root := range []string{".", "../helper", "../editapply", "../protocol"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".go", ".md", ".json", ".txt":
			default:
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			b.Write(data)
			b.WriteByte('\n')
			files++
			return nil
		})
	}
	return corpusText{text: b.String(), files: files}
}

func randomBase62(t *testing.T, n int) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteByte(alphabet[idx.Int64()])
	}
	return b.String()
}

// Throughput over a 512 KiB result -- the per-turn tool-output ceiling -- with a
// realistic registry. Reports ns/byte.
func BenchmarkCredScrubRedact512KiB(b *testing.B) {
	cs := buildCredScrubber([]string{fakeCred, fakeOpaque})
	const size = 512 * 1024
	var sb strings.Builder
	for sb.Len() < size {
		sb.WriteString("the quick brown fox jumps over the lazy dog 0123456789 ")
	}
	text := sb.String()[:size]
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cs.redact(text)
	}
}
