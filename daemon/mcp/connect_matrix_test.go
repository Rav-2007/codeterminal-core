package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	// The wait given to the handshake. Every case below fails at once -- a
	// refused connection, a 404, a page that is not MCP -- so what is checked is
	// that none of them SITS OUT the wait: giving up takes well under it.
	const handshakeWait = 5 * time.Second
	for name, url := range map[string]string{
		"connection refused": closedURL,
		"404":                notFound.URL,
		"html 200":           html.URL,
		"418":                teapot.URL,
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			c, err := ConnectRemote(context.Background(), RemoteConfig{
				Name: "dead", URL: url, ConnectTimeout: handshakeWait,
			})
			if err == nil {
				_ = c.Close()
				t.Fatal("connected to something that is not an MCP server")
			}
			if !errors.Is(err, ErrServerUnavailable) {
				t.Errorf("error is not ErrServerUnavailable: %v", err)
			}
			if d := time.Since(start); d > handshakeWait {
				t.Errorf("took %s to give up; it waited out the %s handshake", d, handshakeWait)
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

	// The server never answers on its own (it waits for release or for the
	// call's context), so a call that ends at all ended because of the cancel.
	// "Promptly" is a multiple of the cancel's own delay, not a clock reading.
	const cancelAfter = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), cancelAfter)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(ctx, "stall", nil)
	if err == nil {
		t.Fatal("a cancelled call returned a result")
	}
	if d := time.Since(start); d > 10*cancelAfter {
		t.Errorf("cancel took %s to take effect; it was asked for after %s", d, cancelAfter)
	}
}

// F5. A remote server may end a session at any time and answer 404 afterwards.
// That is not a failed tool and not a dead server: the request was never acted
// on, so one new session and one retry is correct -- and no more than one.

func connectRaw(t *testing.T, m *rawMCP) *RemoteClient {
	t.Helper()
	ts := httptest.NewServer(m)
	t.Cleanup(ts.Close)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "raw", URL: ts.URL, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestMatrixLostSessionIsReopenedOnce(t *testing.T) {
	m := &rawMCP{}
	c := connectRaw(t, m)
	if got := m.initHits.Load(); got != 1 {
		t.Fatalf("a fresh connection opened %d sessions", got)
	}

	m.expired.Store(true) // the server forgets the session; the next initialize reopens it
	res, err := c.CallTool(context.Background(), "anything", nil)
	if err != nil || res.Content != "ok" {
		t.Fatalf("call after the session was lost: %+v, %v", res, err)
	}
	if got := m.initHits.Load(); got != 2 {
		t.Errorf("%d sessions were opened; want exactly 2 (the original and one replacement)", got)
	}

	// The replacement is the session now: later calls do not reconnect again.
	for i := 0; i < 3; i++ {
		if _, err := c.CallTool(context.Background(), "anything", nil); err != nil {
			t.Fatalf("call %d on the replacement session: %v", i, err)
		}
	}
	if got := m.initHits.Load(); got != 2 {
		t.Errorf("later calls opened more sessions: %d", got)
	}
}

func TestMatrixLostSessionDuringListIsReopened(t *testing.T) {
	m := &rawMCP{toolsJSON: `[{"name":"t","inputSchema":{"type":"object"}}]`}
	c := connectRaw(t, m)
	m.expired.Store(true)
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("list after the session was lost: %v, %v", tools, err)
	}
}

// A server that keeps refusing is unavailable after exactly one retry, not
// retried until something gives.
func TestMatrixLostSessionTwiceIsUnavailable(t *testing.T) {
	m := &rawMCP{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Like rawMCP, but a new session is refused too: initialize succeeds and
		// then every later request 404s again.
		m.ServeHTTP(w, r)
		m.expired.Store(true)
	}))
	t.Cleanup(ts.Close)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "raw", URL: ts.URL, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	before := m.initHits.Load()
	_, err = c.CallTool(context.Background(), "anything", nil)
	if !errors.Is(err, ErrServerUnavailable) {
		t.Fatalf("want ErrServerUnavailable, got %v", err)
	}
	if got := m.initHits.Load() - before; got != 1 {
		t.Errorf("%d reconnects were attempted; want exactly 1", got)
	}
}

// Many calls losing the same session at once open one replacement, not one each.
func TestMatrixConcurrentLossOpensOneSession(t *testing.T) {
	m := &rawMCP{}
	c := connectRaw(t, m)
	m.expired.Store(true)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.CallTool(context.Background(), "anything", nil); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := m.initHits.Load(); got != 2 {
		t.Errorf("%d sessions were opened; want 2 (the original and ONE replacement)", got)
	}
}

// A closed client does not come back to life by being called.
func TestMatrixClosedRemoteDoesNotReconnect(t *testing.T) {
	m := &rawMCP{}
	c := connectRaw(t, m)
	_ = c.Close()
	m.expired.Store(true)
	before := m.initHits.Load()
	if _, err := c.CallTool(context.Background(), "anything", nil); !errors.Is(err, ErrServerUnavailable) {
		t.Fatalf("want ErrServerUnavailable, got %v", err)
	}
	if m.initHits.Load() != before {
		t.Error("a closed client opened a new session")
	}
}

func TestIsTransportDeathRecognisesTheSDKsSentinels(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"session missing":           {fmt.Errorf("sending: %w", sdk.ErrSessionMissing), true},
		"connection closed":         {fmt.Errorf("write: %w", sdk.ErrConnectionClosed), true},
		"eof":                       {fmt.Errorf("read: %w", errors.New("x: EOF")), true},
		"a tool's own failure":      {errors.New("tool exploded"), false},
		"invalid params from tools": {errors.New("invalid params"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isTransportDeath(tc.err); got != tc.want {
				t.Errorf("isTransportDeath(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A closed connection is retried ONLY when this session's server is known to
// have forgotten it. Without that it may be a network drop with the request
// already acted on, and asking again could do a thing twice.
func TestMatrixClosedConnectionIsNotRetriedUnlessSessionWasLost(t *testing.T) {
	m := &rawMCP{}
	c := connectRaw(t, m)
	closed := func(*sdk.ClientSession) error { return fmt.Errorf("write: %w", sdk.ErrConnectionClosed) }

	calls := 0
	err := c.run(func(s *sdk.ClientSession) error { calls++; return closed(s) })
	if !errors.Is(err, sdk.ErrConnectionClosed) || calls != 1 || m.initHits.Load() != 1 {
		t.Fatalf("no lost flag: err=%v calls=%d sessions=%d; want one attempt, no reconnect", err, calls, m.initHits.Load())
	}

	c.lost.Store(true)
	calls = 0
	_ = c.run(func(s *sdk.ClientSession) error { calls++; return closed(s) })
	if calls != 2 || m.initHits.Load() != 2 {
		t.Fatalf("lost flag raised: calls=%d sessions=%d; want two attempts and one reconnect", calls, m.initHits.Load())
	}
}

// F1. tools/list is paged. The host follows the cursor -- within bounds -- and
// when it stops short it keeps what it has and says so.

func toolJSON(name string) string {
	return fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object"}}`, name)
}

func TestMatrixPagedToolListIsFollowed(t *testing.T) {
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
	if err != nil || len(tools) != 5 {
		t.Fatalf("got %d tools, %v; want all 5 across 3 pages", len(tools), err)
	}
}

// A server that answers every page with another cursor is stopped at the page
// limit, and the tools gathered so far are kept.
func TestMatrixEndlessCursorsStopAtThePageLimit(t *testing.T) {
	m := &rawMCP{listFn: func(_ string, n int) (string, string) {
		return fmt.Sprintf(`{"tools":[%s],"nextCursor":"c%d"}`, toolJSON(fmt.Sprintf("t%d", n)), n), ""
	}}
	c := connectRaw(t, m)
	tools, err := c.ListTools(context.Background())
	if !errors.Is(err, ErrToolListIncomplete) {
		t.Fatalf("want ErrToolListIncomplete, got %v", err)
	}
	if len(tools) != maxToolListPages {
		t.Errorf("kept %d tools; want %d (one per page, up to the limit)", len(tools), maxToolListPages)
	}
	if got := int(m.listHits.Load()); got != maxToolListPages {
		t.Errorf("asked for %d pages; want %d", got, maxToolListPages)
	}
	if errors.Is(err, ErrServerUnavailable) {
		t.Error("an incomplete list must not read as an unavailable server")
	}
}

// The same cursor twice is a loop, caught on the second sighting.
func TestMatrixRepeatedCursorStopsTheList(t *testing.T) {
	m := &rawMCP{listFn: func(_ string, n int) (string, string) {
		return fmt.Sprintf(`{"tools":[%s],"nextCursor":"same"}`, toolJSON(fmt.Sprintf("t%d", n))), ""
	}}
	c := connectRaw(t, m)
	tools, err := c.ListTools(context.Background())
	if !errors.Is(err, ErrToolListIncomplete) || len(tools) != 2 {
		t.Fatalf("got %d tools, %v; want 2 tools and ErrToolListIncomplete", len(tools), err)
	}
	if got := int(m.listHits.Load()); got != 2 {
		t.Errorf("asked for %d pages; a repeated cursor should stop at 2", got)
	}
}

func TestMatrixToolCountIsBounded(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "many", Version: "v0"}, &sdk.ServerOptions{PageSize: 5000})
	for i := 0; i < maxToolsPerServer+5; i++ {
		srv.AddTool(&sdk.Tool{Name: fmt.Sprintf("tool_%04d", i), InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
	}
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "many", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	tools, err := c.ListTools(context.Background())
	if !errors.Is(err, ErrToolListIncomplete) || len(tools) != maxToolsPerServer {
		t.Fatalf("got %d tools, %v; want exactly %d and ErrToolListIncomplete", len(tools), err, maxToolsPerServer)
	}
}

// A later page that fails does not throw away the first page's tools.
func TestMatrixLaterPageFailureKeepsEarlierTools(t *testing.T) {
	m := &rawMCP{listFn: func(cursor string, _ int) (string, string) {
		if cursor == "" {
			return fmt.Sprintf(`{"tools":[%s,%s],"nextCursor":"p2"}`, toolJSON("a"), toolJSON("b")), ""
		}
		return "", "page two is broken"
	}}
	c := connectRaw(t, m)
	tools, err := c.ListTools(context.Background())
	if !errors.Is(err, ErrToolListIncomplete) || len(tools) != 2 {
		t.Fatalf("got %d tools, %v; want the first page's 2 tools and ErrToolListIncomplete", len(tools), err)
	}
	// A first page that fails is still just a failed listing.
	bad := connectRaw(t, &rawMCP{listFn: func(string, int) (string, string) { return "", "no" }})
	if tools, err := bad.ListTools(context.Background()); !errors.Is(err, ErrServerUnavailable) || len(tools) != 0 {
		t.Fatalf("a failed first page: %d tools, %v", len(tools), err)
	}
}

// A tool name repeated across pages is one tool: the first is used.
func TestMatrixDuplicateToolNamesAcrossPagesAreOne(t *testing.T) {
	m := &rawMCP{listFn: func(cursor string, _ int) (string, string) {
		if cursor == "" {
			return fmt.Sprintf(`{"tools":[%s,%s],"nextCursor":"p2"}`, toolJSON("dup"), toolJSON("a")), ""
		}
		return fmt.Sprintf(`{"tools":[%s,%s]}`, toolJSON("dup"), toolJSON("b")), ""
	}}
	c := connectRaw(t, m)
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 3 {
		t.Fatalf("got %d tools, %v; want dup, a, b", len(tools), err)
	}
}

// The Registry uses an incomplete list and reports the shortfall; it does not
// treat the server as down.
func TestMatrixRegistryKeepsAnIncompleteList(t *testing.T) {
	m := &rawMCP{listFn: func(_ string, n int) (string, string) {
		return fmt.Sprintf(`{"tools":[%s],"nextCursor":"c%d"}`, toolJSON(fmt.Sprintf("t%d", n)), n), ""
	}}
	c := connectRaw(t, m)
	reg := NewRegistry(matrixPolicy{}, 0)
	if err := reg.AddServer("raw", c); err != nil {
		t.Fatal(err)
	}
	tools, errs := reg.Advertised(context.Background())
	if len(tools) != maxToolListPages {
		t.Errorf("the registry offered %d tools; want %d", len(tools), maxToolListPages)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrToolListIncomplete) {
		t.Errorf("the shortfall must be reported: %v", errs)
	}
	got := reg.Listed()["raw"]
	if got.Tools != maxToolListPages || !errors.Is(got.Err, ErrToolListIncomplete) {
		t.Errorf("the listing outcome is %+v; want the tools counted and the error carried", got)
	}
}

// F2 and F3 end to end: what a real server sends reaches the model.
func TestMatrixStructuredAndResourceContentReachTheModel(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "content", Version: "v0"}, nil)
	add := func(name string, res *sdk.CallToolResult) {
		srv.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) { return res, nil })
	}
	add("stats", &sdk.CallToolResult{StructuredContent: map[string]any{"answer": 42}})
	add("readme", &sdk.CallToolResult{Content: []sdk.Content{&sdk.EmbeddedResource{
		Resource: &sdk.ResourceContents{URI: "file:///README.md", MIMEType: "text/markdown", Text: "SECRET-PAYLOAD-7"},
	}}})
	ts := serveStreamable(t, srv)
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "content", URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	got, err := c.CallTool(context.Background(), "stats", nil)
	if err != nil || !strings.Contains(got.Content, `"answer":42`) {
		t.Errorf("structured-only result: %+v, %v", got, err)
	}
	got, err = c.CallTool(context.Background(), "readme", nil)
	if err != nil || !strings.Contains(got.Content, "SECRET-PAYLOAD-7") || strings.Contains(got.Content, "omitted") {
		t.Errorf("embedded text resource: %+v, %v", got, err)
	}
}

// F4. The bound on a remote server's response is by what the response is.

func connectRawWith(m *rawMCP, limit int) error {
	ts := httptest.NewServer(m)
	defer ts.Close()
	c, err := ConnectRemote(context.Background(), RemoteConfig{
		Name: "raw", URL: ts.URL, MaxMessageBytes: limit, ConnectTimeout: 10 * time.Second,
	})
	if err == nil {
		_ = c.Close()
	}
	return err
}

// A JSON body is one message however many newlines it holds: 5 MiB padded with a
// newline every thousand bytes is refused against a 1 MiB limit, like the same
// 5 MiB on a single line -- and a message under the limit still passes.
func TestMatrixHTTPJSONBodyIsBoundedWhateverItsNewlines(t *testing.T) {
	const limit = 1 << 20
	oneLine := strings.Repeat(" ", 5*limit)
	manyLines := strings.Repeat(strings.Repeat(" ", 1000)+"\n", 5*limit/1001)
	for name, pad := range map[string]string{"one line": oneLine, "many lines": manyLines} {
		t.Run(name, func(t *testing.T) {
			err := connectRawWith(&rawMCP{pad: pad}, limit)
			if !errors.Is(err, ErrServerUnavailable) {
				t.Fatalf("a 5 MiB message against a 1 MiB limit: %v", err)
			}
		})
	}
	small := strings.Repeat(strings.Repeat(" ", 1000)+"\n", 100) // ~100 KiB, newline-separated
	if err := connectRawWith(&rawMCP{pad: small}, limit); err != nil {
		t.Errorf("a message under the limit was refused: %v", err)
	}
}

// An event stream is bounded per EVENT: one event spread over many data: lines
// is counted whole.
func TestMatrixSSEEventIsBoundedAcrossItsDataLines(t *testing.T) {
	const limit = 1 << 20
	many := strings.Repeat(strings.Repeat(" ", 1000)+"\n", 5*limit/1001)
	err := connectRawWith(&rawMCP{sse: true, pad: many}, limit)
	if !errors.Is(err, ErrServerUnavailable) {
		t.Fatalf("a 5 MiB multi-line event against a 1 MiB limit: %v", err)
	}
	small := strings.Repeat(strings.Repeat(" ", 1000)+"\n", 100)
	if err := connectRawWith(&rawMCP{sse: true, pad: small}, limit); err != nil {
		t.Errorf("a multi-line event under the limit was refused: %v", err)
	}
}

// NEGATIVE CONTROL: the stream as a whole is NOT bounded. A call whose reply is
// preceded by 3000 small events -- about 3 MiB against a 1 MiB limit -- is the
// ordinary shape of a long-running tool reporting progress, and must succeed.
func TestMatrixLongSSEStreamOfSmallEventsIsNotKilled(t *testing.T) {
	const limit = 1 << 20
	m := &rawMCP{sse: true, noise: 3000}
	ts := httptest.NewServer(m)
	defer ts.Close()
	c, err := ConnectRemote(context.Background(), RemoteConfig{
		Name: "raw", URL: ts.URL, MaxMessageBytes: limit, ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()
	res, err := c.CallTool(context.Background(), "anything", nil)
	if err != nil || res.Content != "ok" {
		t.Fatalf("a long stream of small events: %+v, %v", res, err)
	}
}

func TestEventLimitReaderCountsPerEvent(t *testing.T) {
	// 10 events of 100 bytes each, limit 150: fine. One event of 200: refused.
	ev := "data: " + strings.Repeat("x", 92) + "\n\n" // 100 bytes
	r := boundedBody(strings.NewReader(strings.Repeat(ev, 10)), "text/event-stream; charset=utf-8", 150, "s")
	if _, err := io.ReadAll(r); err != nil {
		t.Errorf("ten small events: %v", err)
	}
	big := "data: " + strings.Repeat("x", 192) + "\n\n"
	r = boundedBody(strings.NewReader(big), "text/event-stream", 150, "s")
	if _, err := io.ReadAll(r); !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("one big event: %v", err)
	}
	// CRLF framing ends events the same way.
	crlf := strings.Repeat("data: "+strings.Repeat("x", 90)+"\r\n\r\n", 10)
	r = boundedBody(strings.NewReader(crlf), "text/event-stream", 150, "s")
	if _, err := io.ReadAll(r); err != nil {
		t.Errorf("ten small CRLF events: %v", err)
	}
	// Anything else is bounded as a whole.
	r = boundedBody(strings.NewReader(strings.Repeat(ev, 10)), "application/json", 150, "s")
	if _, err := io.ReadAll(r); !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("a 1000-byte JSON body against 150: %v", err)
	}
}
