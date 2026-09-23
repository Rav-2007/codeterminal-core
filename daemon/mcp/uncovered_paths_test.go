package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests for behaviour the package had but never exercised. Each is a real
// guarantee someone relies on, not a line-coverage exercise: a redactor that
// drops output, a truncation that splices a rune in half, or a registry that
// accepts a builtin with no handler would all be defects a user could meet.

// THE REDACTOR NEVER DROPS BYTES. os/exec's copier reads a short count as a
// write error and stops the stream, so a writer holding a partial line must
// still report the whole write. And with nothing to redact it must be a
// pass-through rather than a buffer that swallows the last unterminated line.
func TestTheRedactingWriterHoldsPartialLinesWithoutLosingThem(t *testing.T) {
	var got strings.Builder
	w := newRedactingStderr(&got, []string{"SECRET=hunter2-longenough"}, []string{"SECRET"})

	// A partial line: fully accounted for, nothing emitted yet.
	n, err := w.Write([]byte("no newline yet"))
	if err != nil || n != len("no newline yet") {
		t.Fatalf("partial write returned (%d, %v), want (%d, nil)", n, err, len("no newline yet"))
	}
	if got.String() != "" {
		t.Errorf("a partial line was emitted early: %q", got.String())
	}

	// Completing the line flushes it, with the provisioned value redacted.
	if _, err := w.Write([]byte(" hunter2-longenough\n")); err != nil {
		t.Fatal(err)
	}
	if out := got.String(); strings.Contains(out, "hunter2-longenough") {
		t.Errorf("the provisioned value reached stderr: %q", out)
	}
	if out := got.String(); !strings.HasPrefix(out, "no newline yet") {
		t.Errorf("the held bytes were lost: %q", out)
	}
}

// A nil destination is a sink that still accounts for every byte, and no
// allowlist means no wrapper at all -- the caller keeps its own writer.
func TestTheRedactingWriterDegradesToAPassThrough(t *testing.T) {
	var sink strings.Builder
	if w := newRedactingStderr(&sink, nil, nil); w != nil {
		if _, ok := w.(*redactingWriter); ok {
			t.Error("an empty allowlist still produced a redacting wrapper")
		}
	}
	if w := newRedactingStderr(nil, nil, []string{"X"}); w != nil {
		t.Error("a nil destination produced a writer")
	}
	// The zero writer writes nowhere but reports success, so a caller that kept
	// one cannot mistake it for a failing pipe.
	zero := &redactingWriter{}
	if n, err := zero.Write([]byte("abc")); n != 3 || err != nil {
		t.Errorf("writing to a destination-less redactor returned (%d, %v), want (3, nil)", n, err)
	}
}

// Redaction is a no-op when there is nothing provisioned to redact, rather than
// a rewrite that could mangle an ordinary line.
func TestRedactEnvValuesLeavesUnprovisionedLinesAlone(t *testing.T) {
	const line = "plain server output\n"
	if got := redactEnvValues(line, []string{"A=1"}, nil); got != line {
		t.Errorf("an empty allowlist rewrote the line: %q", got)
	}
	if got := redactEnvValues("", []string{"A=1"}, []string{"A"}); got != "" {
		t.Errorf("an empty line became %q", got)
	}
	// The longest value is substituted first, so a value that contains another
	// cannot leave the longer one half-redacted.
	// Both are long enough to be redacted and one contains the other, so the
	// longest-first ordering is what stops "[REDACTED:[REDACTED:..]]" nesting.
	out := redactEnvValues("saw abcdefgh-extra here\n",
		[]string{"LONG=abcdefgh-extra", "SHORT=abcdefgh"}, []string{"LONG", "SHORT"})
	if strings.Contains(out, "abcdefgh") {
		t.Errorf("a provisioned value survived: %q", out)
	}
	if strings.Contains(out, "[REDACTED:[REDACTED:") {
		t.Errorf("placeholders nested, which longest-first ordering must prevent: %q", out)
	}
	// A value that is short is left alone by design (minRedactableEnvValue).
	if got := redactEnvValues("tiny=abc\n", []string{"T=abc"}, []string{"T"}); got != "tiny=abc\n" {
		t.Errorf("a value under the minimum length was redacted: %q", got)
	}
}

// A C1 control character in a tool name is refused. C1 bytes are acted on by
// terminals, and a tool name reaches the consent prompt, so this is the same
// class of defect as an escape sequence in an approval screen.
func TestValidateToolNameRefusesC1Controls(t *testing.T) {
	// U+0085 NEL, encoded as UTF-8, is valid UTF-8 and a C1 control.
	if err := ValidateToolName("bad\u0085name"); err == nil {
		t.Error("a tool name containing U+0085 was accepted")
	} else if !strings.Contains(err.Error(), "C1 control") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
	if err := ValidateToolName("ordinary_tool"); err != nil {
		t.Errorf("an ordinary name was refused: %v", err)
	}
}

// A truncated description is cut on a RUNE boundary: splicing half a rune into a
// consent prompt is how a prompt renders as something other than what was
// checked.
func TestSanitiseToolDescriptionTruncatesOnARuneBoundary(t *testing.T) {
	// THREE-byte runes, so the byte limit lands strictly INSIDE a rune and the
	// backtrack to a rune start is the code that has to run. (Two-byte runes
	// divide the 1024-byte limit exactly and would never exercise it.)
	desc := strings.Repeat("\u20ac", maxToolDescriptionBytes)
	if maxToolDescriptionBytes%3 == 0 {
		t.Fatal("test setup: the limit must not fall on a 3-byte rune boundary")
	}
	out := SanitiseToolDescription(desc)
	if !strings.Contains(out, "truncated by Mochiii") {
		t.Fatalf("a description over the limit was not truncated (%d bytes in, %d out)", len(desc), len(out))
	}
	if !utf8.ValidString(out) {
		t.Error("truncation produced invalid UTF-8: a partial rune was spliced into the description")
	}
}

// A builtin with no handler, or no name, is refused at registration rather than
// panicking on the first call.
func TestRegisterBuiltinRefusesAnIncompleteTool(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if err := r.RegisterBuiltin(Builtin{Tool: Tool{Name: "no_handler"}}); err == nil {
		t.Error("a builtin with no handler was registered")
	}
	if err := r.RegisterBuiltin(Builtin{Handler: func(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }}); err == nil {
		t.Error("a builtin with no name was registered")
	}
}

// A scope unit name whose pid field is not a usable pid is skipped, not reaped
// on a guess -- pid 0 names no process and must never select a scope to kill.
func TestParseSandboxScopeUnitsSkipsAnUnusablePid(t *testing.T) {
	out := strings.Join([]string{
		"mochiii-sandbox-0-1.scope    loaded active running pid zero",
		"mochiii-sandbox-12-3.scope   loaded active running fine",
	}, "\n")
	got := ParseSandboxScopeUnits(out)
	if len(got) != 1 || got[0].PID != 12 {
		t.Errorf("parsed %+v, want only the pid-12 unit", got)
	}
}

// closeErrClient is a server whose shutdown fails, so Close's error aggregation
// is exercised.
type closeErrClient struct {
	fakeClient
	err error
}

func (c *closeErrClient) Close() error { return c.err }

// LOOKUP ANSWERS FOR A NAME THE MODEL SUPPLIED, and every way that can go wrong
// resolves to DENY rather than to something a caller might treat as allowed: a
// server whose tool list fails, and a tool the server does not offer.
func TestLookupResolvesUnknownAndFailingServersToDeny(t *testing.T) {
	ctx := context.Background()

	// A server that offers the tool: found, with its policy.
	r := NewRegistry(mapPolicy{"ext__read_file": PolicyAllow}, 0)
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{{Name: "read_file"}}}); err != nil {
		t.Fatal(err)
	}
	tool, policy, err := r.Lookup(ctx, "ext__read_file")
	if err != nil || tool.Name != "read_file" || policy != PolicyAllow {
		t.Errorf("Lookup(ext__read_file) = (%v, %v, %v), want the tool with PolicyAllow", tool.Name, policy, err)
	}

	// A tool the server does not offer: deny, and the message names both.
	if _, p, err := r.Lookup(ctx, "ext__not_there"); err == nil || p != PolicyDeny {
		t.Errorf("an unoffered tool resolved to (%v, %v), want deny with an error", p, err)
	} else if !strings.Contains(err.Error(), "not_there") {
		t.Errorf("the error does not name the tool: %v", err)
	}

	// A server whose listing fails: deny, with the failure surfaced rather than
	// swallowed into "no such tool".
	rf := NewRegistry(mapPolicy{}, 0)
	if err := rf.AddServer("broken", &closeErrClient{fakeClient: fakeClient{name: "broken", listErr: errTestListing}}); err != nil {
		t.Fatal(err)
	}
	if _, p, err := rf.Lookup(ctx, "broken__anything"); err == nil || p != PolicyDeny {
		t.Errorf("a failing server resolved to (%v, %v), want deny with the listing error", p, err)
	}
}

// Close shuts every server down and reports the first failure, rather than
// stopping at it and leaving the rest running.
func TestRegistryCloseReportsAFailureAndStillClosesTheRest(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	bad := &closeErrClient{fakeClient: fakeClient{name: "bad"}, err: errTestListing}
	good := &fakeClient{name: "good"}
	if err := r.AddServer("bad", bad); err != nil {
		t.Fatal(err)
	}
	if err := r.AddServer("good", good); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err == nil {
		t.Error("Close hid a server's shutdown failure")
	}
	if !good.closed {
		t.Error("a later server was left running after an earlier one failed to close")
	}
}

// A tool the policy DENIES is never advertised to the model. Denying a tool must
// remove it from the menu, not merely refuse it after the model has asked.
func TestAdvertisedOmitsDeniedTools(t *testing.T) {
	r := NewRegistry(mapPolicy{"ext__dangerous": PolicyDeny}, 0)
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{
		{Name: "safe"}, {Name: "dangerous"},
	}}); err != nil {
		t.Fatal(err)
	}
	tools, errs := r.Advertised(context.Background())
	if len(errs) != 0 {
		t.Fatalf("Advertised reported errors: %v", errs)
	}
	for _, tool := range tools {
		if tool.Name == "dangerous" {
			t.Error("a denied tool was advertised to the model")
		}
	}
	if len(tools) == 0 {
		t.Error("the allowed tool was dropped along with the denied one")
	}
}

var errTestListing = errors.New("listing failed")

// A message-limit reader that has already tripped stays tripped: once a server
// has overrun the cap, every later read reports the same refusal instead of
// resuming mid-message.
func TestMessageLimitReaderStaysTrippedOnceItHas(t *testing.T) {
	l := &messageLimitReader{r: strings.NewReader("anything"), max: 4, server: "s", tripped: true}
	n, err := l.Read(make([]byte, 8))
	if n != 0 || err == nil {
		t.Errorf("a tripped reader returned (%d, %v), want (0, error)", n, err)
	}
	// And the error names the server, so the message is actionable.
	if !strings.Contains(err.Error(), "s") {
		t.Errorf("the refusal does not name the server: %v", err)
	}
}

// ANNOTATIONS NEVER READ AS A CLAIM OF SAFETY. A server that supplies no
// annotations at all is saying nothing, and "nothing" must not become
// "read-only": absent annotations read as not-read-only, and an absent
// destructive hint reads as destructive (the spec's default is true).
func TestAnnotationBoolTreatsSilenceAsUnsafe(t *testing.T) {
	bare := &sdk.Tool{} // no Annotations at all
	if annotationBool(bare, readOnly) {
		t.Error("a tool with no annotations was reported read-only")
	}
	if annotationBool(bare, destructive) {
		t.Error("a tool with no annotations reached the destructive branch")
	}
	// An unknown kind is not a claim either.
	if annotationBool(&sdk.Tool{Annotations: &sdk.ToolAnnotations{}}, annotationKind(99)) {
		t.Error("an unknown annotation kind returned true")
	}
	// Annotations present but no destructive hint: destructive by default.
	if !annotationBool(&sdk.Tool{Annotations: &sdk.ToolAnnotations{}}, destructive) {
		t.Error("an omitted destructive hint was read as safe")
	}
}

// failingWriter reports an error for every write, so the redactor's error path
// is exercised.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errTestListing }

// A destination that fails is REPORTED, not swallowed: os/exec's copier needs
// the error to stop, and a redactor that hid it would silently drop a server's
// diagnostics.
func TestTheRedactingWriterReportsADestinationFailure(t *testing.T) {
	w := newRedactingStderr(failingWriter{}, []string{"SECRET=hunter2-longenough"}, []string{"SECRET"})
	if _, err := w.Write([]byte("a complete line\n")); err == nil {
		t.Error("a failing destination was reported as a successful write")
	}
}
