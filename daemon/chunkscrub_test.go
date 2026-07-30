package main

import (
	"fmt"
	"strings"
	"testing"
)

// chunkscrub.go had all 8 functions at 0.0% coverage. It is the WARN-MODE
// secret detector: the component whose entire job is to tell you your index is
// about to leak credentials. Its false-negative rate was therefore UNMEASURED,
// and the privacy claim it supports is quantitative in nature.
//
// So this file reports NUMBERS, not just pass/fail. Three corpora, each with a
// different contract:
//
//   - MUST-DETECT: real credential shapes. A miss here is a false negative, the
//     failure that matters. Gated by chunkScrubMaxFalseNegativeRate.
//   - MUST-NOT-FIRE: ordinary source code. A hit here is noise that would bury
//     the real signal. Gated by chunkScrubMaxFalsePositiveRate.
//   - KNOWN-BENIGN-HIGH-ENTROPY: git SHAs, UUIDs, base64 assets, lockfile
//     integrity hashes. These are REPORTED and deliberately NOT gated, because
//     over-reporting is warn-mode's stated design intent (see
//     CHUNK_SCRUB_DESIGN §2b) and this rate is precisely the data it exists to
//     gather. Asserting it were zero would be asserting the design away.
//
// Nothing here alters what is sent to the model. detectWarnModeSecrets is
// log-only by design and this file does not change that.

// MEASURED 2026-07-30 against the corpora below. Named constants in the same
// style as evalTop3RecallThreshold, so a regression moves a number that has a
// date attached rather than silently changing a bare literal.
//
// The false-negative rate measured 0.000 (0 of 12) -- the detector catches every
// real credential shape in the corpus.
//
// The false-positive rate measured 0.188 (3 of 16), and that is the number, not
// an aspiration. The threshold below is set just above the measurement so it
// gates future drift rather than pretending the current noise away. The three
// that fire, all on the ENTROPY tier and all on completely ordinary code:
//
//   - a 42-char camelCase identifier      (4.10 bits/char)
//   - a 25-char snake_case constant name  (4.00 bits/char, exactly at the bound)
//   - a 6-char non-secret token value     (keyword tier, at isNonSecretValue's floor)
//
// See TestChunkScrub_OrdinaryIdentifiersTripTheEntropyTier, which pins that
// finding on its own. It is noise in a LOG-ONLY path, so it costs signal quality
// and nothing else today -- but it is exactly the cost that must be priced
// before anyone promotes these detectors to redaction, which the file header of
// chunkscrub.go explicitly reserves as a founder decision.
const (
	chunkScrubMaxFalseNegativeRate = 0.10
	chunkScrubMaxFalsePositiveRate = 0.20
)

// mustDetect are credential shapes the detector is FOR. Each is embedded in
// realistic surrounding code, because that is how it would appear in a chunk.
var mustDetect = []struct {
	name string
	text string
}{
	{"openai project key", `apiKey := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz012345678901234567890"`},
	{"github personal token", `token = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"`},
	{"aws access key id", "AWS_ACCESS_KEY_ID=AKIA1234567890ABCDEF"},
	{"aws secret access key", `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`},
	{"password assignment", `password: "hunter2secretvalue"`},
	{"api_key assignment", `api_key = "9f8e7d6c5b4a39281706fedcba9876543210"`},
	{"client_secret yaml", `client_secret: 8Q~zXcVbNmAsDfGhJkLpOiUyTrEwQ12345`},
	{"bearer in a header literal", `req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9")`},
	{"pem private key body", "-----BEGIN RSA PRIVATE KEY-----\n" +
		"MIIEowIBAAKCAQEAtZ3f8Vx2Qk9LmNpQrStUvWxYz0123456789abcdefGHIJKL\n" +
		"-----END RSA PRIVATE KEY-----"},
	{"dotenv secret", "DATABASE_PASSWORD=pR0dSuperSecret99xyz"},
	{"slack-style token", `SLACK_TOKEN=xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx`},
	{"private_key json field", `"private_key": "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ"`},
}

// mustNotFire is ordinary source code. A detection here is pure noise.
var mustNotFire = []struct {
	name string
	text string
}{
	{"plain go function", "func main() {\n\tfmt.Println(\"hello world\")\n}"},
	{"long identifiers", "someVeryLongVariableNameForTestingPurposes := computeTheThing(a, b)"},
	{"import path", `import "github.com/yalue/onnxruntime_go"`},
	{"url", "https://openrouter.ai/api/v1/chat/completions"},
	{"file path", "/home/user/Desktop/project/daemon/chunkscrub.go"},
	{"prose comment", "// This function returns the per-character Shannon entropy of its argument."},
	{"struct definition", "type Chunk struct {\n\tID string\n\tFilePath string\n\tStartLine int\n}"},
	{"sql", "SELECT file_path, start_line, end_line FROM code_chunks_fts WHERE rowid = ?"},
	// Indirections: a credential-named key whose value is a LOOKUP, not a secret.
	// isNonSecretValue exists for exactly these, and they are common in real code.
	{"env lookup python", `api_key = os.environ["OPENROUTER_API_KEY"]`},
	{"env lookup node", "const token = process.env.GITHUB_TOKEN;"},
	{"env lookup go", `apiKey := os.Getenv("OPENROUTER_API_KEY")`},
	{"shell template", "password: ${DB_PASSWORD}"},
	{"already redacted upstream", `password = "[REDACTED:aws_key]"`},
	{"short value", `password = "abc"`},
	{"snake_case identifiers", "max_bytes_per_chunk_guard = 512"},
	{"base64 word that is too short", `token = "abc123"`},
}

// knownBenignHighEntropy fires by design. Reported, never gated.
var knownBenignHighEntropy = []struct {
	name string
	text string
}{
	{"git sha", "commit 3f2a1b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a"},
	{"uuid", `id := "550e8400-e29b-41d4-a716-446655440000"`},
	{"sha256 hex digest", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	{"base64 png data uri", "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"},
	{"npm lockfile integrity", `"integrity": "sha512-VBIeQ2bLrRi/mVmU4iDVj2gDGRZUnpJdTIWWPl8kU9EVBXCzQyE8YFCFSKPFHRvVXbBGGrRJdvGZ4pFtnqmZQA=="`},
	{"minified js fragment", "var a=function(b,c){return b.charCodeAt(c)^0x5f};module.exports=a;"},
}

func TestChunkScrub_FalseNegativeRate(t *testing.T) {
	var missed []string
	for _, tc := range mustDetect {
		if len(detectWarnModeSecrets(tc.text)) == 0 {
			missed = append(missed, tc.name)
		}
	}

	rate := float64(len(missed)) / float64(len(mustDetect))
	t.Logf("FALSE-NEGATIVE RATE: %d/%d = %.3f (threshold %.3f)",
		len(missed), len(mustDetect), rate, chunkScrubMaxFalseNegativeRate)
	for _, name := range missed {
		t.Logf("  MISSED: %s", name)
	}

	if rate > chunkScrubMaxFalseNegativeRate {
		t.Errorf("the warn-mode detector missed %d of %d real credential shapes (%.3f > %.3f). "+
			"A false negative here means the index leaked a credential and nothing said so",
			len(missed), len(mustDetect), rate, chunkScrubMaxFalseNegativeRate)
	}
}

func TestChunkScrub_FalsePositiveRate(t *testing.T) {
	var fired []string
	for _, tc := range mustNotFire {
		if d := detectWarnModeSecrets(tc.text); len(d) > 0 {
			fired = append(fired, fmt.Sprintf("%s (%s: %s)", tc.name, d[0].Detector, d[0].Note))
		}
	}

	rate := float64(len(fired)) / float64(len(mustNotFire))
	t.Logf("FALSE-POSITIVE RATE on ordinary code: %d/%d = %.3f (threshold %.3f)",
		len(fired), len(mustNotFire), rate, chunkScrubMaxFalsePositiveRate)
	for _, name := range fired {
		t.Logf("  FIRED: %s", name)
	}

	if rate > chunkScrubMaxFalsePositiveRate {
		t.Errorf("the detector fired on %d of %d ordinary-code samples (%.3f > %.3f). "+
			"Noise at this rate buries the real signal the warn-mode log exists to produce",
			len(fired), len(mustNotFire), rate, chunkScrubMaxFalsePositiveRate)
	}
}

// Reported, NOT gated. Warn-mode is designed to over-report on this class so the
// rate can be measured and a redaction threshold chosen from evidence. The
// number is the deliverable; there is no assertion to pass or fail.
func TestChunkScrub_KnownBenignHighEntropyRate(t *testing.T) {
	var fired []string
	for _, tc := range knownBenignHighEntropy {
		if d := detectWarnModeSecrets(tc.text); len(d) > 0 {
			fired = append(fired, fmt.Sprintf("%s -> %s shape=%s", tc.name, d[0].Note, d[0].Shape))
		}
	}
	t.Logf("KNOWN-BENIGN HIGH-ENTROPY FIRE RATE (by design, not gated): %d/%d = %.3f",
		len(fired), len(knownBenignHighEntropy), float64(len(fired))/float64(len(knownBenignHighEntropy)))
	for _, f := range fired {
		t.Logf("  fired: %s", f)
	}
	t.Log("This rate is the DATA warn-mode exists to gather (CHUNK_SCRUB_DESIGN §2b): it is what " +
		"a later founder decision on entropy redaction must be priced against. Note that pure-hex " +
		"shapes (git SHAs, sha256 digests, UUIDs) do NOT fire, because 16 symbols cap Shannon " +
		"entropy at exactly 4.0 bits/char and the threshold is 4.0 -- the dominant expected " +
		"false-positive class is excluded by arithmetic rather than by a special case.")
}

// A secret split across a chunk boundary is a STRUCTURAL blind spot: the
// detector sees one chunk at a time, so neither half looks like a credential.
// Measured and documented rather than asserted away -- it is a real limitation
// of chunk-level detection and cannot be fixed inside this function.
func TestChunkScrub_SecretSplitAcrossAChunkBoundary(t *testing.T) {
	const secret = "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz012345678901234567890"

	whole := detectWarnModeSecrets(`apiKey := "` + secret + `"`)
	if len(whole) == 0 {
		t.Fatal("the intact secret is not detected at all; this test cannot measure the split case")
	}

	half := len(secret) / 2
	firstHalf := detectWarnModeSecrets(`apiKey := "` + secret[:half])
	secondHalf := detectWarnModeSecrets(secret[half:] + `"`)

	t.Logf("SPLIT-SECRET DETECTION: intact=%d hits, first half=%d hits, second half=%d hits",
		len(whole), len(firstHalf), len(secondHalf))

	switch {
	case len(firstHalf) == 0 && len(secondHalf) == 0:
		t.Log("MEASURED: a credential split at this length is invisible to BOTH halves -- a true " +
			"false negative that no threshold tuning can recover, since the detector sees one " +
			"chunk at a time.")
	case len(firstHalf) > 0 || len(secondHalf) > 0:
		t.Logf("MEASURED: the split is still caught by at least one half (first=%d second=%d). "+
			"A 55-char key cut in two leaves ~27 chars, still over the 20-char length floor and "+
			"still above the entropy bound, so detection survives THIS split. It would not "+
			"survive a split that leaves either side under 20 chars -- the length floor, not the "+
			"entropy threshold, is the binding constraint.", len(firstHalf), len(secondHalf))
	}

	// The genuinely undetectable case: both sides under the 20-char floor.
	short := "sk-AbCdEfGh1jKlMn0pQr"
	q := len(short) / 2
	if len(detectWarnModeSecrets(short[:q])) == 0 && len(detectWarnModeSecrets(short[q:])) == 0 {
		t.Logf("CONFIRMED STRUCTURAL LIMITATION: %q split into two sub-20-char halves is "+
			"invisible to both. entropyTokenPattern's {20,} floor is what makes this "+
			"unrecoverable by tuning. Chunking overlaps by 10 LINES (chunker.go), so in practice "+
			"this only bites a secret split mid-line. Recorded, not gated.", short)
	}
}

// A named finding, not a gate. Ordinary identifier naming -- long camelCase and
// snake_case -- reaches 4.0+ bits/char and trips the entropy tier. This is the
// dominant real false-positive source on source code, ahead of the base64 assets
// the design anticipated, and it is measured here so the number exists before
// anyone argues about promoting warn-mode to redaction.
func TestChunkScrub_OrdinaryIdentifiersTripTheEntropyTier(t *testing.T) {
	samples := []string{
		"someVeryLongVariableNameForTestingPurposes := computeTheThing(a, b)",
		"max_bytes_per_chunk_guard = 512",
		"const defaultReservationTokens = 4096",
		"func assertExpectationsAreCurrent(t *testing.T, chunks []Chunk) {",
		"DegradedProviderRouting = \"provider_routing\"",
	}
	fired := 0
	for _, s := range samples {
		if d := detectHighEntropy(s); len(d) > 0 {
			fired++
			t.Logf("  identifier FP: %-68q %s", s, d[0].Note)
		}
	}
	t.Logf("ORDINARY-IDENTIFIER FALSE-POSITIVE RATE (reported, not gated): %d/%d = %.3f",
		fired, len(samples), float64(fired)/float64(len(samples)))
	t.Log("Root cause: entropyTokenPattern's character class includes '_' and '-', so a long " +
		"snake_case or kebab-case name is ONE token, and mixed-case identifiers genuinely carry " +
		">4 bits/char. Harmless while warn-mode is log-only. A prerequisite to fix -- not a " +
		"detail -- if these detectors are ever promoted to redaction, since redacting a variable " +
		"name would corrupt the code sent to the model.")
}

func TestShannonEntropy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
	}{
		{"empty is zero", "", 0},
		{"single repeated byte carries no information", "aaaaaaaa", 0},
		{"two equally frequent bytes is exactly one bit", "abab", 1},
		{"four equally frequent bytes is exactly two bits", "abcdabcd", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shannonEntropy(tt.in); got != tt.want {
				t.Errorf("shannonEntropy(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}

	// The documented range is 0..8 bits/char over a byte distribution.
	for _, s := range []string{"a", "hello world", strings.Repeat("x", 1000), "AKIA1234567890ABCDEF"} {
		if h := shannonEntropy(s); h < 0 || h > 8 {
			t.Errorf("shannonEntropy(%q) = %v, outside the documented 0..8 range", s, h)
		}
	}

	// The property the threshold silently depends on: a hex alphabet is 16
	// symbols, so its entropy cannot exceed 4.0 -- which is exactly why the 4.0
	// threshold excludes git SHAs. If someone lowers the threshold below 4.0,
	// every SHA in the repo starts firing.
	if h := shannonEntropy("0123456789abcdef0123456789abcdef"); h > 4.0 {
		t.Errorf("a uniform hex string measured %v bits/char, above 4.0 -- the assumption that "+
			"hex cannot cross the threshold is wrong", h)
	}
	if entropyWarnThresholdBitsPerChar < 4.0 {
		t.Errorf("entropyWarnThresholdBitsPerChar = %v: below 4.0 every git SHA and sha256 digest "+
			"in the repo becomes a detection, which is the dominant false-positive class",
			entropyWarnThresholdBitsPerChar)
	}
}

func TestClassifyTokenShape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "unknown"},
		{"550e8400-e29b-41d4-a716-446655440000", "uuid-like"},
		{"3f2a1b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a", "hex"},
		{"deadBEEF", "hex"},
		// Order matters: hex's alphabet is a subset of base64's, so hex must be
		// tested first or every SHA would be labelled base64.
		{"iVBORw0KGgoAAAANSUhEUgAA", "base64"},
		{"abc_-=+/", "base64"},
		{"has spaces in it", "mixed"},
		{"tok!with@punct", "mixed"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := classifyTokenShape(tt.in); got != tt.want {
				t.Errorf("classifyTokenShape(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// GATE 3, non-negotiable: classifyTokenShape must return ONLY a fixed label and
// never any substring of its input, or raw secret material rides out on the
// shape field into the log this file exists to keep secret-free.
func TestClassifyTokenShape_NeverLeaksTokenMaterial(t *testing.T) {
	allowed := map[string]bool{"unknown": true, "uuid-like": true, "hex": true, "base64": true, "mixed": true}

	secrets := []string{
		"sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz01234567890",
		"ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"AKIA1234567890ABCDEF",
		"hunter2secretvalue",
		"550e8400-e29b-41d4-a716-446655440000",
		"password!@#$%^&*()",
	}
	for _, s := range secrets {
		shape := classifyTokenShape(s)
		if !allowed[shape] {
			t.Errorf("classifyTokenShape(%q) returned %q, which is not one of the fixed labels", s, shape)
		}
		// No run of 4+ characters from the input may appear in the label.
		for i := 0; i+4 <= len(s); i++ {
			if frag := s[i : i+4]; strings.Contains(shape, frag) {
				t.Errorf("the shape label %q contains the input fragment %q -- secret material "+
					"escaping via the shape field", shape, frag)
			}
		}
	}
}

// The same non-negotiable, applied to the whole detection: no field of a
// warnDetection may carry the suspected value.
func TestWarnDetection_NeverCarriesTheRawSecret(t *testing.T) {
	const secret = "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz012345678901234567890"
	const password = "hunter2secretvalue"

	for _, tc := range []struct{ name, text, value string }{
		{"entropy detector", `apiKey := "` + secret + `"`, secret},
		{"keyword detector", `password: "` + password + `"`, password},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detections := detectWarnModeSecrets(tc.text)
			if len(detections) == 0 {
				t.Fatalf("no detection produced for %s; nothing to check", tc.name)
			}
			for _, d := range detections {
				for field, got := range map[string]string{
					"Detector": d.Detector, "Note": d.Note, "Shape": d.Shape, "Indicator": d.Indicator,
				} {
					if strings.Contains(got, tc.value) {
						t.Errorf("%s carries the raw value: %q", field, got)
					}
					// Also no substantial fragment of it.
					for i := 0; i+8 <= len(tc.value); i++ {
						if frag := tc.value[i : i+8]; strings.Contains(got, frag) {
							t.Errorf("%s = %q contains the 8-char fragment %q of the suspected value",
								field, got, frag)
						}
					}
				}
			}
		})
	}
}

func TestValueIndicator(t *testing.T) {
	const v = "hunter2secretvalue"

	got := valueIndicator(v)
	if !strings.HasPrefix(got, "sha256:") {
		t.Errorf("valueIndicator(%q) = %q, want a \"sha256:\" prefix so it is unmistakably a hash", v, got)
	}
	if strings.Contains(got, v) {
		t.Errorf("valueIndicator returned the value itself: %q", got)
	}
	// Stable: the same value must produce the same indicator, which is what makes
	// "the same secret across log lines" recognizable.
	if again := valueIndicator(v); again != got {
		t.Errorf("valueIndicator is not deterministic: %q then %q", got, again)
	}
	// Distinct values must not collide at this truncation.
	if valueIndicator("aaaaaaaaaaaa") == valueIndicator("bbbbbbbbbbbb") {
		t.Error("two different values produced the same indicator")
	}
	// Truncated: 8 hex chars after the prefix, not a full digest (a full digest of
	// a low-entropy value is closer to being brute-forceable back to it).
	if hex := strings.TrimPrefix(got, "sha256:"); len(hex) != 8 {
		t.Errorf("indicator hex length = %d, want 8", len(hex))
	}
}

func TestIsNonSecretValue(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"abc", true},     // too short
		{"abcde", true},   // still under the 6-char floor
		{"abcdef", false}, // at the floor
		{"${DB_PASSWORD}", true},
		{"$(vault read)", true},
		{"$DB_PASSWORD", true},
		{`os.environ["K"]`, true},
		{"process.env.TOKEN", true},
		{`os.Getenv("K")`, true},
		{"[REDACTED:aws_key]", true},
		{"hunter2secretvalue", false},
		{"sk-proj-Ab3dEfGh1jKlMn0p", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isNonSecretValue(tt.in); got != tt.want {
				t.Errorf("isNonSecretValue(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// detectWarnModeSecrets must run BOTH detectors and concatenate, not short-circuit
// on the first that fires -- the fire-rate measurement needs per-detector counts.
func TestDetectWarnModeSecrets_RunsBothDetectors(t *testing.T) {
	// A credential-named assignment whose value is also high-entropy: both
	// detectors should have something to say about it.
	text := `api_key = "Ab3dEfGh1jKlMn0pQrStUvWxYz0123456789"`
	detections := detectWarnModeSecrets(text)

	seen := map[string]int{}
	for _, d := range detections {
		seen[d.Detector]++
	}
	t.Logf("detectors that fired: %v", seen)
	if seen["entropy"] == 0 {
		t.Error("the entropy detector did not fire on a 36-char high-entropy token")
	}
	if seen["keyword"] == 0 {
		t.Error("the keyword detector did not fire on an api_key assignment")
	}
}

func TestDetectWarnModeSecrets_EmptyAndBenignInputs(t *testing.T) {
	for _, in := range []string{"", "\n\n", "   ", "// nothing to see"} {
		if d := detectWarnModeSecrets(in); len(d) != 0 {
			t.Errorf("detectWarnModeSecrets(%q) produced %d detection(s), want none", in, len(d))
		}
	}
}
