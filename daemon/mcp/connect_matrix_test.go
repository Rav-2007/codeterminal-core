package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CONNECTION MATRIX: can this daemon, as an MCP host, create a client for a
// server, list its tools, call them, and tear the client down -- over every
// transport it supports, through the Registry the agent loop actually uses.
//
// Offline and deterministic: servers are the SDK's own handlers on httptest, or
// the echoserver fixture as a real subprocess. Nothing here touches the network
// beyond loopback. The opt-in tests that reach real third-party servers are in
// connect_public_live_test.go; the behaviours that are known to be wrong are in
// connect_flaws_test.go.

// matrixPolicy allows everything: these tests are about the wire, not consent.
type matrixPolicy struct{}

func (matrixPolicy) PolicyFor(string, string) Policy { return PolicyAllow }

func newMatrixSDKServer(opts *sdk.ServerOptions) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "matrix", Version: "v0"}, opts)
	sdk.AddTool(srv, &sdk.Tool{Name: "ping", Description: "Answer pong."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
		})
	return srv
}

func serveStreamable(t *testing.T, srv *sdk.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts
}

// A configured stdio server becomes a client, its tool is advertised under the
// qualified name, and a call routed through the Registry reaches it.
func TestMatrixStdioThroughRegistry(t *testing.T) {
	client := connectEcho(t, nil)
	reg := NewRegistry(matrixPolicy{}, 24)
	if err := reg.AddServer("echo", client); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	tools, errs := reg.Advertised(context.Background())
	if len(errs) != 0 {
		t.Fatalf("listing errors: %v", errs)
	}
	var found bool
	for _, tool := range tools {
		if tool.QualifiedName() == "echo__echo" {
			found = true
			if tool.Confined || tool.Remote != "" {
				t.Errorf("a stdio server's tool is framed as %+v", tool)
			}
		}
	}
	if !found {
		t.Fatalf("echo__echo not advertised; got %d tools", len(tools))
	}
	res, err := reg.Call(context.Background(), "echo__echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || res.IsError || !strings.Contains(res.Content, "hi") {
		t.Fatalf("call through registry: %+v, %v", res, err)
	}
}

// The same, for a remote server over streamable HTTP, with the user's header.
func TestMatrixRemoteThroughRegistry(t *testing.T) {
	var seen atomic.Value
	srv := newMatrixSDKServer(nil)
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("X-Api-Key"))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	c, err := ConnectRemote(context.Background(), RemoteConfig{
		Name: "rem", URL: ts.URL, Headers: map[string]string{"X-Api-Key": "k-123"},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	reg := NewRegistry(matrixPolicy{}, 24)
	if err := reg.AddServer("rem", c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	tools, errs := reg.Advertised(context.Background())
	if len(errs) != 0 || len(tools) != 1 || tools[0].QualifiedName() != "rem__ping" {
		t.Fatalf("advertised %+v, errs %v", tools, errs)
	}
	if !tools[0].ReachesNetwork || tools[0].Remote == "" {
		t.Errorf("a remote tool must say where its arguments go: %+v", tools[0])
	}
	res, err := reg.Call(context.Background(), "rem__ping", nil)
	if err != nil || res.Content != "pong" {
		t.Fatalf("call: %+v, %v", res, err)
	}
	if got, _ := seen.Load().(string); got != "k-123" {
		t.Errorf("server was sent X-Api-Key %q", got)
	}
}

// A server that cannot be reached is ErrServerUnavailable, quickly, for every
// way of not being reachable -- never a hang and never a panic.
func TestMatrixUnreachableRemoteIsUnavailable(t *testing.T) {
	// A port that was open a moment ago and is now closed.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + l.Addr().String()
	_ = l.Close()

	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an MCP server</html>"))
	}))
	t.Cleanup(html.Close)
	teapot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "short and stout", http.StatusTeapot)
	}))
	t.Cleanup(teapot.Close)

	for name, url := range map[string]string{
		"connection refused": closedURL,
		"404":                notFound.URL,
		"html 200":           html.URL,
		"418":                teapot.URL,
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			c, err := ConnectRemote(context.Background(), RemoteConfig{
				Name: "dead", URL: url, ConnectTimeout: 5 * time.Second,
			})
			if err == nil {
				_ = c.Close()
				t.Fatal("connected to something that is not an MCP server")
			}
			if !errors.Is(err, ErrServerUnavailable) {
				t.Errorf("error is not ErrServerUnavailable: %v", err)
			}
			if d := time.Since(start); d > 8*time.Second {
				t.Errorf("took %s to give up", d)
			}
		})
	}
}

// A local command that does not exist is a recognisable start failure.
func TestMatrixMissingCommandIsUnavailable(t *testing.T) {
	_, err := Connect(context.Background(), LaunchConfig{
		Name: "nope", Command: "/definitely/not/a/program", ConnectTimeout: 3 * time.Second,
	})
	if !errors.Is(err, ErrServerUnavailable) {
		t.Fatalf("want ErrServerUnavailable, got %v", err)
	}
}

// What each content block becomes, and that a tool-level error is data, not a
// transport failure.
func TestMatrixResultFlattening(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "flat", Version: "v0"}, nil)
	add := func(name string, res *sdk.CallToolResult) {
		srv.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) { return res, nil })
	}
	add("two_texts", &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.TextContent{Text: "one"}, &sdk.TextContent{Text: "two"}}})
	add("image", &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.ImageContent{Data: []byte("x"), MIMEType: "image/png"}}})
	add("tool_error", &sdk.CallToolResult{IsError: true, Content: []sdk.Content{
		&sdk.TextContent{Text: "bad input"}}})
	ts := serveStreamable(t, srv)

	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "flat", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	got, err := c.CallTool(context.Background(), "two_texts", nil)
	if err != nil || got.Content != "one\ntwo" {
		t.Errorf("two texts: %+v, %v", got, err)
	}
	got, err = c.CallTool(context.Background(), "image", nil)
	if err != nil || got.Content == "" || !strings.Contains(got.Content, "non-text") {
		t.Errorf("an image must be named, not silently dropped: %+v, %v", got, err)
	}
	got, err = c.CallTool(context.Background(), "tool_error", nil)
	if err != nil || !got.IsError || got.Content != "bad input" {
		t.Errorf("tool-level error: %+v, %v", got, err)
	}
	if _, err = c.CallTool(context.Background(), "no_such_tool", nil); err == nil {
		t.Error("calling a tool the server does not have must fail")
	}
}

// One client, many concurrent calls: no race, no lost or crossed answers.
// Meaningful under `go test -race`.
func TestMatrixConcurrentCallsOnOneClient(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "conc", Version: "v0"}, nil)
	type in struct {
		N int `json:"n"`
	}
	sdk.AddTool(srv, &sdk.Tool{Name: "square"},
		func(_ context.Context, _ *sdk.CallToolRequest, a in) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprint(a.N * a.N)}}}, nil, nil
		})
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "conc", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for n := 1; n <= 40; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.CallTool(context.Background(), "square", json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)))
			if err != nil {
				errs <- err
				return
			}
			if want := fmt.Sprint(n * n); res.Content != want {
				errs <- fmt.Errorf("n=%d: got %q want %q", n, res.Content, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// A closed client says so on every verb, and closing twice is harmless.
func TestMatrixClosedClientIsUnavailable(t *testing.T) {
	ts := serveStreamable(t, newMatrixSDKServer(nil))
	rc, err := ConnectRemote(context.Background(), RemoteConfig{Name: "r", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	sc := connectEcho(t, nil)
	for name, c := range map[string]Client{"remote": rc, "stdio": sc} {
		t.Run(name, func(t *testing.T) {
			if err := c.Close(); err != nil {
				t.Logf("first close: %v", err)
			}
			_ = c.Close()
			if _, err := c.ListTools(context.Background()); !errors.Is(err, ErrServerUnavailable) {
				t.Errorf("ListTools after Close: %v", err)
			}
			if _, err := c.CallTool(context.Background(), "ping", nil); !errors.Is(err, ErrServerUnavailable) {
				t.Errorf("CallTool after Close: %v", err)
			}
		})
	}
}

// A malformed argument object is refused to the model as data, over a remote
// transport as over a pipe.
func TestMatrixMalformedArgsRefusedNotSent(t *testing.T) {
	ts := serveStreamable(t, newMatrixSDKServer(nil))
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "r", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	res, err := c.CallTool(context.Background(), "ping", json.RawMessage(`{"a":`))
	if err != nil || !res.IsError {
		t.Fatalf("want a tool-level refusal, got %+v, %v", res, err)
	}
}

// A call whose context is cancelled returns promptly instead of waiting out a
// slow server.
func TestMatrixCancelledCallReturns(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "slow", Version: "v0"}, nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv.AddTool(&sdk.Tool{Name: "stall", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "late"}}}, nil
		})
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "slow", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(ctx, "stall", nil)
	if err == nil {
		t.Fatal("a cancelled call returned a result")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %s to take effect", d)
	}
}
