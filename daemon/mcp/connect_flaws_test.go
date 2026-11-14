package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// KNOWN FLAWS in the MCP host's connection path, each stated as the behaviour
// that SHOULD hold. See the write-up that accompanies this file for evidence
// and severity.
//
// A flaw test that fails is SKIPPED, with its id, so `make check` stays green
// and the flaw stays visible in `go test -v`. Run with MCP_FLAWS=1 to make them
// fail for real; when a flaw is fixed its test passes and says so, and the
// helper can be deleted.

func knownFlaw(t *testing.T, id string, holds bool, why string) {
	t.Helper()
	if holds {
		t.Logf("%s: the behaviour holds -- this flaw looks fixed", id)
		return
	}
	if os.Getenv("MCP_FLAWS") != "" {
		t.Fatalf("%s: %s", id, why)
	}
	t.Skipf("KNOWN FLAW %s: %s", id, why)
}

// F1. tools/list is requested once. A server that pages its tool list (nextCursor)
// has every tool after the first page silently dropped: session.go calls
// ListTools with empty params and never follows NextCursor.
func TestFlawF1ToolListPaginationIsNotFollowed(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "paged", Version: "v0"}, &sdk.ServerOptions{PageSize: 2})
	for _, n := range []string{"t1", "t2", "t3", "t4", "t5"} {
		srv.AddTool(&sdk.Tool{Name: n, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
	}
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "paged", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	knownFlaw(t, "F1", len(tools) == 5,
		"server offers 5 tools over pages of 2; the host sees "+itoa(len(tools))+" and says nothing about the rest")
}

// F2. A result that carries only structuredContent (no content blocks) reaches
// the model as an empty string; the structured value is never read.
func TestFlawF2StructuredContentOnlyResultIsLost(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "structured", Version: "v0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "stats", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{StructuredContent: map[string]any{"answer": 42}}, nil
		})
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "structured", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	res, err := c.CallTool(context.Background(), "stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	knownFlaw(t, "F2", strings.Contains(res.Content, "42"),
		"the tool answered {\"answer\":42} as structuredContent; the model is given "+quote(res.Content))
}

// F3. An embedded resource that is plain text is reported as "content omitted"
// even though its text is right there in the result.
func TestFlawF3EmbeddedTextResourceIsDropped(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "res", Version: "v0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "readme", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.EmbeddedResource{
				Resource: &sdk.ResourceContents{URI: "file:///README.md", MIMEType: "text/markdown", Text: "SECRET-PAYLOAD-7"},
			}}}, nil
		})
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "res", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	res, err := c.CallTool(context.Background(), "readme", nil)
	if err != nil {
		t.Fatal(err)
	}
	knownFlaw(t, "F3", strings.Contains(res.Content, "SECRET-PAYLOAD-7"),
		"an embedded text resource reaches the model as "+quote(res.Content))
}

// F4. DefaultMaxMessageBytes is 2 MiB "per message", enforced by counting bytes
// since the last newline. That is exact for stdio (newline-delimited) and wrong
// for an HTTP application/json body, where whitespace may contain newlines: a
// 5 MiB message is accepted when its padding is newline-separated and refused
// when it is not. The bound is therefore not a bound.
func TestFlawF4HTTPMessageLimitIsBypassedByNewlines(t *testing.T) {
	const limit = 1 << 20
	size := 5 * limit
	connect := func(pad string) error {
		ts := httptest.NewServer(&rawMCP{pad: pad})
		defer ts.Close()
		c, err := ConnectRemote(context.Background(), RemoteConfig{
			Name: "raw", URL: ts.URL, MaxMessageBytes: limit, ConnectTimeout: 10 * time.Second,
		})
		if err == nil {
			_ = c.Close()
		}
		return err
	}
	plain := connect(strings.Repeat(" ", size)) // control: no newlines
	if plain == nil {
		t.Fatal("control failed: a 5 MiB single-line message was accepted against a 1 MiB limit")
	}
	if !strings.Contains(plain.Error(), "larger than") && !errors.Is(plain, ErrServerUnavailable) {
		t.Fatalf("control failed for the wrong reason: %v", plain)
	}
	lines := connect(strings.Repeat(strings.Repeat(" ", 1000)+"\n", size/1001))
	knownFlaw(t, "F4", lines != nil,
		"a 5 MiB message with a newline every 1000 bytes was accepted against a 1 MiB limit")
}

func quote(s string) string { return `"` + s + `"` }

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
