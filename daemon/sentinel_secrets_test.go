// B.2 — the sentinel sweep.
//
// One distinctive, scrub-matching sentinel is planted per A.4 row, the path is
// driven (including its ERROR arms, because the happy path rarely stringifies a
// secret), and every sink is then searched for the planted bytes. Zero hits
// passes.
//
// WHY THE SENTINELS ARE AKIA-SHAPED. A sentinel that scrub() does not recognise
// would make every assertion here pass for the wrong reason: "no hits" would
// mean "nothing was ever supposed to be redacted". Each sentinel is a literal
// AWS-access-key shape (AKIA + 16 upper-alnum), so scrub MUST catch it at every
// point that claims to redact, and the row number is carried inside the sentinel
// so a hit names the row that leaked.
//
// VACUITY. Every assertion below is paired with a floor: the sink must be
// non-empty, or the sentinel must be shown present somewhere upstream, before
// "zero hits" is allowed to mean anything. Two tests shipped vacuous earlier in
// this pass and both were caught by neutering rather than by care.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/daemon/mcp"
)

// sentinelFor returns row n's sentinel: AKIA + exactly 16 chars from [0-9A-Z],
// which is what scrubPatterns' aws_access_key regexp requires.
func sentinelFor(n int) string {
	s := fmt.Sprintf("AKIASENTINELROW%02d000", n)
	if len(s) != 20 {
		panic("sentinel must be AKIA + 16 chars; got " + s)
	}
	return s
}

// a4Rows names the eleven places A.4 says a secret can live, so a report can
// say which rows were actually exercised rather than implying all of them were.
var a4Rows = map[int]string{
	1:  "daemon env keys",
	2:  "proxy env keys",
	3:  "the TCP bearer token",
	4:  "the user's prompt",
	5:  "retrieved chunk text",
	6:  "MCP server env",
	7:  "persisted prompts and answers",
	8:  "chunk text in chromem",
	9:  "file content in backups",
	10: "detector fires in warnmode.jsonl",
	11: "tool arguments in toolcalls.jsonl",
}

// bufferLogger returns a logger and the buffer holding everything written to
// it, so a test can grep the daemon's own log output rather than trusting it.
// (watcher_test.go's capturingLogger is the mutex-guarded variant for the
// watcher goroutine; nothing here is concurrent, so a plain buffer is honest.)
func bufferLogger() (*log.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return log.New(&buf, "", 0), &buf
}

// sentinelChunk is a retrieved chunk whose content carries row 5's sentinel
// surrounded by ordinary code, so scrub's span-level behaviour (redact the
// match, keep the surroundings) is visible in the result.
func sentinelChunk() Chunk {
	body := "func main() {\n\tkey := \"" + sentinelFor(5) + "\"\n\t_ = key\n}\n"
	return Chunk{
		ID:        "creds.go:1-4",
		FilePath:  "creds.go",
		StartLine: 1,
		EndLine:   4,
		Content:   body,
		Class:     FileClassCode,
	}
}

// TestSentinel_RetrievedChunkIsScrubbedOnTheWireButNotInTheDebugLog is the
// measurement behind the B.2 finding: renderChunk redacts, and logRetrieval's
// --debug-context arm does not, because it formats c.Content directly.
//
// Both halves are asserted in one test on purpose. Separated, the passing half
// reads as "chunk scrubbing works" and the failing half reads as a log-format
// nit; together they are one fact — the same field reaches two consumers and
// only one of them goes through the choke point.
func TestSentinel_RetrievedChunkIsScrubbedOnTheWireButNotInTheDebugLog(t *testing.T) {
	want := sentinelFor(5)
	chunk := sentinelChunk()

	if !strings.Contains(chunk.Content, want) {
		t.Fatalf("vacuity floor: the sentinel is not in the chunk content at all")
	}

	// The wire half: what buildAugmentedUserMessage produces is what goes into
	// the outbound user message verbatim (see buildChatMessages).
	onTheWire := buildAugmentedUserMessage("explain this", []Chunk{chunk}, false)
	if onTheWire == "" {
		t.Fatal("vacuity floor: rendered message is empty")
	}
	if strings.Contains(onTheWire, want) {
		t.Errorf("row 5: the sentinel reached the outbound user message unscrubbed")
	}
	if !strings.Contains(onTheWire, "[REDACTED:aws_access_key]") {
		t.Errorf("row 5: no redaction marker in the outbound message — scrub did not fire, so the clean result above proves nothing")
	}

	// The log half, at a level that is OFF BY DEFAULT and therefore exactly
	// where B2.2c says to look.
	logger, logBuf := bufferLogger()
	s := &Server{logger: logger, debugContext: true}
	s.logRetrieval(retrievalOutcome{Chunks: []Chunk{chunk}})

	if logBuf.Len() == 0 {
		t.Fatal("vacuity floor: logRetrieval wrote nothing, so grepping it proves nothing")
	}
	if strings.Contains(logBuf.String(), want) {
		t.Errorf("row 5: --debug-context wrote the RAW chunk content, sentinel and all, to the daemon log.\n"+
			"logRetrieval must scrub c.Content like the wire path does; if this is red, that scrub was removed.\n"+
			"log was:\n%s", logBuf.String())
	}
}

// TestSentinel_DebugContextIsTheOnlyDifference isolates the flag as the cause,
// so the finding cannot be misread as "the daemon logs chunks".
func TestSentinel_DebugContextIsTheOnlyDifference(t *testing.T) {
	want := sentinelFor(5)
	chunk := sentinelChunk()

	quietLogger, quietBuf := bufferLogger()
	(&Server{logger: quietLogger}).logRetrieval(retrievalOutcome{Chunks: []Chunk{chunk}})

	loudLogger, loudBuf := bufferLogger()
	(&Server{logger: loudLogger, debugContext: true}).logRetrieval(retrievalOutcome{Chunks: []Chunk{chunk}})

	if quietBuf.Len() == 0 || loudBuf.Len() == 0 {
		t.Fatal("vacuity floor: one of the two logRetrieval calls wrote nothing")
	}
	if strings.Contains(quietBuf.String(), want) {
		t.Errorf("default logging leaked the sentinel; the finding is wider than --debug-context")
	}
	if loudBuf.Len() <= quietBuf.Len() {
		t.Fatalf("vacuity floor: --debug-context did not produce more output (%d vs %d bytes), so it was not exercised",
			loudBuf.Len(), quietBuf.Len())
	}
}

// TestSentinel_WarnSinkRecordsNoSecretMaterial is B2.3a: warnsink.go:18 claims
// the record carries "only fixed labels, source offsets, a secret-free note,
// and the one-way indicator hash... never the value". Planted, driven, grepped.
//
// STRUCTURAL, not pattern-matching: the claim holds because warnEvent has no
// field that carries the value, not because something filtered it. That is the
// right discipline, and the test is written to catch a HOLE in the structure
// (a field that does carry it) rather than to re-test a filter.
func TestSentinel_WarnSinkRecordsNoSecretMaterial(t *testing.T) {
	secret := sentinelFor(10)
	dir := t.TempDir()
	path := filepath.Join(dir, "warnmode.jsonl")

	dets := detectKeywordSecrets("api_key = \"" + secret + "\"\n")
	if len(dets) == 0 {
		t.Fatalf("vacuity floor: the keyword detector did not fire on the planted sentinel, so writing its (empty) detections proves nothing")
	}

	sink := newWarnSink(path)
	for _, d := range dets {
		sink.write(warnEvent{
			Detector: d.Detector, File: "config.go", StartLine: 1, EndLine: 1,
			Class: FileClassConfig, Shape: d.Shape, Note: d.Note, Indicator: d.Indicator,
		})
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the warn sink: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("vacuity floor: the warn sink file is empty")
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Errorf("row 10: warnmode.jsonl contains the raw value.\nfile:\n%s", raw)
	}
	// The claim is about the VALUE, and the note deliberately carries the
	// keyword. Assert the intended shape survived, or "no secret" could equally
	// mean "nothing was written".
	if !bytes.Contains(raw, []byte(`"indicator":"sha256:`)) {
		t.Errorf("row 10: no indicator in the record — the structure under test is not present")
	}
}

// TestSentinel_ToolAuditRecordsDigestNotArguments is B2.3b, including the arms
// B2.3b names: malformed arguments, oversized arguments, and an unparseable
// qualified name, which is the one place auditFor records model output whole.
func TestSentinel_ToolAuditRecordsDigestNotArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toolcalls.jsonl")
	sink := newToolAuditSink(path)

	secret := sentinelFor(11)
	cases := []struct {
		name      string
		qualified string
		arguments string
	}{
		{"well formed", "fs/read", `{"path":"` + secret + `"}`},
		{"malformed json", "fs/read", `{"path":"` + secret + `"`},
		{"oversized", "fs/read", strings.Repeat("x", 1<<20) + secret},
		{"empty arguments", "fs/read", ""},
	}
	for _, tc := range cases {
		ev := auditFor(1, "auto", tc.qualified, mcp.Tool{}, mcp.Policy("ask"), tc.arguments)
		ev.Outcome = auditOutcomeOK
		sink.record(ev)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the audit sink: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("vacuity floor: the audit file is empty")
	}
	if got := bytes.Count(raw, []byte("\n")); got != len(cases) {
		t.Fatalf("vacuity floor: wrote %d lines, want %d — not every arm was recorded", got, len(cases))
	}
	if !bytes.Contains(raw, []byte(`"arguments_sha256":"`)) {
		t.Error("row 11: no digest field — the structure under test is not present")
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Errorf("row 11: toolcalls.jsonl contains the sentinel.\nfile:\n%s", raw)
	}
}

// TestSentinel_ToolAuditBoundsAnUnparseableToolName is the corrected half of
// what I first filed as one finding.
//
// I CONFLATED TWO THINGS AND THE SECOND WAS NOT A DEFECT. The sentinel did
// appear in toolcalls.jsonl -- via the SERVER field, because auditFor records a
// tool name the model asked for and could not be parsed. That recording is
// deliberate and documented ("an unparseable tool name is a finding, not a
// formatting problem"), so asserting its absence was asserting against the
// design. What was a defect is that it was UNBOUNDED: measured, a 1,048,596-byte
// name produced a 1,048,851-byte line.
//
// So this test asserts the property that was actually broken -- the record is
// bounded -- and deliberately does NOT assert the name is absent.
func TestSentinel_ToolAuditBoundsAnUnparseableToolName(t *testing.T) {
	secret := sentinelFor(11)
	huge := strings.Repeat("N", 1<<20) + secret

	ev := auditFor(1, "auto", huge, mcp.Tool{}, mcp.Policy("ask"), "{}")
	// maxReportedToolName bounds the CONTENT, not the field: truncateForClient
	// returns s[:max] + "…", and the ellipsis is 3 UTF-8 bytes. Asserting the
	// field at exactly 128 would be asserting against the helper's semantics.
	const ellipsis = "\u2026"
	if want := maxReportedToolName + len(ellipsis); len(ev.Server) > want {
		t.Errorf("Server field is %d bytes, over the %d-byte bound (%d content + %d ellipsis)",
			len(ev.Server), want, maxReportedToolName, len(ellipsis))
	}
	if len(ev.Server) == 0 {
		t.Fatal("vacuity floor: the name was dropped entirely, which loses the diagnostic the field exists for")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "toolcalls.jsonl")
	sink := newToolAuditSink(path)
	ev.Outcome = auditOutcomeOK
	sink.record(ev)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the audit sink: %v", err)
	}
	if len(raw) > 4096 {
		t.Errorf("one audit line is %d bytes from a %d-byte tool name; the bound is not holding", len(raw), len(huge))
	}
	// BY DESIGN, asserted so a future reader does not "fix" it: a short
	// unparseable name is still recorded, because that is the finding.
	shortEv := auditFor(1, "auto", "no-slash-here", mcp.Tool{}, mcp.Policy("ask"), "{}")
	if shortEv.Server != "no-slash-here" {
		t.Errorf("Server = %q, want the name recorded whole when it fits", shortEv.Server)
	}
}

// TestSentinel_ToolAuditDigestIsOverTheExactArguments keeps the digest
// load-bearing: it must bind to the call that ran, or the absence of arguments
// is a loss of audit value rather than a privacy win.
func TestSentinel_ToolAuditDigestIsOverTheExactArguments(t *testing.T) {
	args := `{"path":"` + sentinelFor(11) + `"}`
	sum := sha256.Sum256([]byte(args))
	want := hex.EncodeToString(sum[:])

	ev := auditFor(1, "auto", "fs/read", mcp.Tool{}, mcp.Policy("ask"), args)
	if ev.ArgumentsSHA256 != want {
		t.Errorf("ArgumentsSHA256 = %q, want %q", ev.ArgumentsSHA256, want)
	}
	if ev.ArgumentsBytes != len(args) {
		t.Errorf("ArgumentsBytes = %d, want %d", ev.ArgumentsBytes, len(args))
	}
}

// TestSentinel_WarnNoteAndIndicatorConfirmAGuessedValue is B2.4: not a
// plaintext leak, and deliberately not written as one. The question is whether
// the record lets a reader who already has a CANDIDATE confirm it — which is
// the definition of an oracle, and a strictly weaker property than disclosure.
func TestSentinel_WarnNoteAndIndicatorConfirmAGuessedValue(t *testing.T) {
	actual := sentinelFor(10)
	dets := detectKeywordSecrets("api_key = \"" + actual + "\"\n")
	if len(dets) != 1 {
		t.Fatalf("vacuity floor: want exactly 1 detection to reason about, got %d", len(dets))
	}
	note, indicator := dets[0].Note, dets[0].Indicator

	// Everything an attacker holding only the log line has.
	confirms := func(candidate string) bool {
		if fmt.Sprintf("keyword=api_key value_len=%d", len(candidate)) != note {
			return false
		}
		return valueIndicator(candidate) == indicator
	}

	if !confirms(actual) {
		t.Fatalf("vacuity floor: the true value does not satisfy the record, so this measures nothing")
	}
	wrong := 0
	for i := 1; i <= 40; i++ {
		if c := sentinelFor(i); c != actual && confirms(c) {
			wrong++
		}
	}
	if wrong != 0 {
		t.Fatalf("vacuity floor: %d wrong candidates also matched; the oracle claim needs a sharper measurement", wrong)
	}
	t.Logf("ORACLE CONFIRMED: note=%q indicator=%q accepted the true value and rejected 39 same-shape candidates. "+
		"The record does not DISCLOSE the value; it VERIFIES a guess. 'secret-free' is true of the bytes and narrower than it reads.",
		note, indicator)
}

// TestSentinel_IndicatorSearchSpaceIsThirtyTwoBits states the strength of the
// oracle as a number rather than an adjective, so the severity argument in the
// register rests on something measured.
func TestSentinel_IndicatorSearchSpaceIsThirtyTwoBits(t *testing.T) {
	ind := valueIndicator("anything")
	hexDigits := strings.TrimPrefix(ind, "sha256:")
	if len(hexDigits) != 8 {
		t.Fatalf("indicator carries %d hex digits, want 8; the 32-bit figure in the register is wrong", len(hexDigits))
	}
	if _, err := hex.DecodeString(hexDigits); err != nil {
		t.Fatalf("indicator is not hex: %v", err)
	}
}

// TestSentinel_A4RowsAreAccountedFor is the honesty gate on the sweep: every
// A.4 row must be claimed by a named test, and the claim must say WHERE.
//
// IT WAS BUILT WRONG THE FIRST TIME and is worth recording. Its original form
// asserted that at least one row was still undriven -- a fine gate while
// coverage was partial, and one that FAILS THE MOMENT THE WORK IS FINISHED.
// A gate whose green state is unreachable is not a gate. This form asserts the
// property that actually matters: no row is unaccounted for, and nothing here
// claims credit for a test in another module.
func TestSentinel_A4RowsAreAccountedFor(t *testing.T) {
	// Where each row's evidence lives. "" means nothing drives it yet.
	drivenBy := map[int]string{
		1:  "TestSentinelRow1_APIKeyNeverLeavesTheAuthorizationHeader",
		2:  "proxy/logging_test.go TestLogNeverContainsSecrets (ANOTHER MODULE)",
		3:  "TestSentinelRow3_BearerTokenIsNotInTheRejection (LABELLED not load-bearing)",
		4:  "TestSentinelRow4_TypedPromptIsScrubbedOutbound",
		5:  "TestSentinel_RetrievedChunkIsScrubbedOnTheWireButNotInTheDebugLog",
		6:  "TestSentinelRow6_MCPEnvAllowListDropsForbiddenAndUnlisted",
		7:  "TestSentinelRow7_PersistedPromptIsStillRaw_TRIPWIRE (a tripwire, not a control)",
		8:  "TestSentinelRow8_ChunkTextAtRestIsRawByDesign",
		9:  "TestSentinelRow9_BackupIsByteExactByDesign",
		10: "TestSentinel_WarnSinkRecordsNoSecretMaterial",
		11: "TestSentinel_ToolAuditRecordsDigestNotArguments",
	}

	if len(a4Rows) != 11 {
		t.Fatalf("a4Rows has %d rows, want 11", len(a4Rows))
	}
	for n := 1; n <= 11; n++ {
		if a4Rows[n] == "" {
			t.Errorf("row %d has no description in a4Rows", n)
		}
		if drivenBy[n] == "" {
			t.Errorf("row %d (%s) is UNACCOUNTED FOR: no test claims it", n, a4Rows[n])
		}
	}

	// Row 2's evidence is in another module and this package cannot run it, so
	// it is named rather than counted as local coverage. Stated here because a
	// sweep that silently absorbs another module's test overstates its reach.
	if !strings.Contains(drivenBy[2], "ANOTHER MODULE") {
		t.Error("row 2 must stay marked as covered outside this package")
	}
}

// TestSentinel_TheSinkGrepsAreLoadBearing is the NEUTER for the two structural
// claims above, and it is permanent rather than a one-off.
//
// Rows 10 and 11 do not hold because something redacts; they hold because
// warnEvent and toolAuditEvent have no field that carries the value. Neutering
// scrub() therefore cannot make them fire — measured: under a no-op scrub, five
// pre-existing tests and two of mine went red, and neither structural test
// moved. So the neuter that means anything here is the opposite one: write a
// record that DOES carry the value and prove the same grep catches it. Without
// this, "zero hits" is indistinguishable from "the grep is broken".
func TestSentinel_TheSinkGrepsAreLoadBearing(t *testing.T) {
	secret := sentinelFor(10)
	dir := t.TempDir()

	warnPath := filepath.Join(dir, "warnmode.jsonl")
	newWarnSink(warnPath).write(warnEvent{
		Detector: "keyword", File: "config.go", StartLine: 1, EndLine: 1,
		Class: FileClassConfig, Shape: "mixed",
		Note:      "keyword=api_key value=" + secret, // the hole this grep exists to find
		Indicator: valueIndicator(secret),
	})
	warnRaw, err := os.ReadFile(warnPath)
	if err != nil {
		t.Fatalf("reading warn sink: %v", err)
	}
	if !bytes.Contains(warnRaw, []byte(secret)) {
		t.Error("the warn-sink grep is NOT load-bearing: a record built to carry the value did not trip it")
	}

	auditPath := filepath.Join(dir, "toolcalls.jsonl")
	sink := newToolAuditSink(auditPath)
	ev := auditFor(1, "auto", "fs/read", mcp.Tool{}, mcp.Policy("ask"), `{"path":"`+secret+`"}`)
	ev.DenyCause = secret // any free-text field would do; this proves the grep reads the whole line
	sink.record(ev)
	auditRaw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("reading audit sink: %v", err)
	}
	if !bytes.Contains(auditRaw, []byte(secret)) {
		t.Error("the audit-sink grep is NOT load-bearing: a record built to carry the value did not trip it")
	}
}

// TestSentinel_AuditDigestSurvivesTheArmsThatCouldFallBack pins the negative
// result B2.3b asked for: there is no fallback path that returns raw arguments.
// sha256 cannot fail, so malformed and oversized inputs are digested like any
// other — which is worth recording, because "the fallback leaks" was the
// hypothesis and it is FALSE here.
func TestSentinel_AuditDigestSurvivesTheArmsThatCouldFallBack(t *testing.T) {
	secret := sentinelFor(11)
	for _, args := range []string{
		`{"path":"` + secret + `"`,          // malformed JSON
		strings.Repeat("x", 1<<20) + secret, // oversized
		secret,                              // not JSON at all
		"",                                  // empty
	} {
		ev := auditFor(1, "auto", "fs/read", mcp.Tool{}, mcp.Policy("ask"), args)
		if strings.Contains(ev.ArgumentsSHA256, secret) {
			t.Errorf("digest carries the secret for %d-byte arguments", len(args))
		}
		if len(ev.ArgumentsSHA256) != 64 {
			t.Errorf("digest is %d hex chars for %d-byte arguments, want 64 — a fallback fired",
				len(ev.ArgumentsSHA256), len(args))
		}
		if ev.ArgumentsBytes != len(args) {
			t.Errorf("ArgumentsBytes = %d, want %d", ev.ArgumentsBytes, len(args))
		}
	}
}
