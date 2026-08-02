package mcp

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The three pure functions that turn a server's answer into something this
// daemon will act on. They are small, they are on the path every Lane B tool
// call takes, and two of them encode a decision rather than a transformation —
// which is what makes them worth pinning rather than reading.

// An absent destructiveHint must read as DESTRUCTIVE. This is the security
// default the SDK's *bool exists for: the MCP spec's default is true, so a
// server that omits the hint is saying nothing, and "nothing" must not become
// "safe". A server cannot earn a weaker policy by staying silent.
func TestAnnotationBool_AbsentDestructiveHintIsDestructive(t *testing.T) {
	silent := &sdk.Tool{Annotations: &sdk.ToolAnnotations{}}
	if !annotationBool(silent, destructive) {
		t.Error("a tool whose destructiveHint is absent was read as non-destructive; " +
			"an unanswered question must not resolve to the permissive answer")
	}

	no := false
	explicit := &sdk.Tool{Annotations: &sdk.ToolAnnotations{DestructiveHint: &no}}
	if annotationBool(explicit, destructive) {
		t.Error("an explicit destructiveHint:false was ignored")
	}

	yes := true
	loud := &sdk.Tool{Annotations: &sdk.ToolAnnotations{DestructiveHint: &yes}}
	if !annotationBool(loud, destructive) {
		t.Error("an explicit destructiveHint:true was ignored")
	}
}

// No annotations at all is the commonest real case and must not panic or
// default to permissive.
func TestAnnotationBool_NoAnnotationsBlock(t *testing.T) {
	bare := &sdk.Tool{}
	if annotationBool(bare, readOnly) {
		t.Error("a tool with no annotations block was read as readOnly")
	}
	if annotationBool(bare, destructive) {
		t.Error("a tool with no annotations block should not be classified from a nil block")
	}
}

func TestAnnotationBool_ReadOnlyHint(t *testing.T) {
	on := &sdk.Tool{Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}
	if !annotationBool(on, readOnly) {
		t.Error("readOnlyHint:true was not read back")
	}
	off := &sdk.Tool{Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false}}
	if annotationBool(off, readOnly) {
		t.Error("readOnlyHint:false was read as true")
	}
}

// flattenContent is what the model actually sees. The branch that matters is
// the non-text one: a server can return an image, an embedded resource, or
// anything else the SDK models, and the result must be a stated omission rather
// than silence — a tool that appears to have returned nothing is a tool the
// model will reason about wrongly.
func TestFlattenContent_NonTextIsAnnouncedNotDropped(t *testing.T) {
	got := flattenContent([]sdk.Content{
		&sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"},
	})
	if got == "" {
		t.Fatal("a non-text result flattened to the empty string; the model would be told the tool returned nothing")
	}
	if !strings.Contains(got, "content omitted") {
		t.Errorf("got %q, want an explicit omission notice", got)
	}
}

func TestFlattenContent_JoinsWithNewlinesAndKeepsOrder(t *testing.T) {
	got := flattenContent([]sdk.Content{
		&sdk.TextContent{Text: "first"},
		&sdk.TextContent{Text: "second"},
	})
	if got != "first\nsecond" {
		t.Errorf("got %q, want %q", got, "first\nsecond")
	}
}

// A mixed result must keep both halves, and the separator must not be dropped
// just because the omission notice is synthesized rather than server-supplied.
func TestFlattenContent_MixedContentKeepsBoth(t *testing.T) {
	got := flattenContent([]sdk.Content{
		&sdk.TextContent{Text: "here is the chart"},
		&sdk.ImageContent{MIMEType: "image/png"},
	})
	if !strings.HasPrefix(got, "here is the chart\n") {
		t.Errorf("got %q, want the text first followed by a newline", got)
	}
	if !strings.Contains(got, "content omitted") {
		t.Errorf("got %q, want the non-text part announced too", got)
	}
}

func TestFlattenContent_EmptyIsEmpty(t *testing.T) {
	if got := flattenContent(nil); got != "" {
		t.Errorf("flattenContent(nil) = %q, want empty", got)
	}
}

// logfSafe exists so no call site has to nil-check. The nil case is the default
// for a client built without a logger, so it is the one that must not panic.
func TestLogfSafe_NilLoggerIsSilentNotFatal(t *testing.T) {
	c := &StdioClient{name: "test"}
	c.logfSafe("this must not panic: %d", 1)

	var got string
	c2 := &StdioClient{name: "test", logf: func(f string, a ...any) { got = f }}
	c2.logfSafe("delivered")
	if got != "delivered" {
		t.Errorf("a client WITH a logger did not receive the message; got %q", got)
	}
}
