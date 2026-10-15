package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A REMOTE SERVER, reached over HTTP (remoteclient.go), against the SDK's own
// server handlers -- the real wire, not a fake of it.

type noArgs struct{}

// remoteTestServer is an MCP server over streamable HTTP or SSE that records
// the Authorization header it was sent.
func remoteTestServer(t *testing.T, sse bool, seen *atomic.Value) *httptest.Server {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "remote-test", Version: "v0"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "ping", Description: "Answer pong."},
		func(_ context.Context, _ *sdk.CallToolRequest, _ noArgs) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil, nil
		})
	var h http.Handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	if sse {
		h = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Store(r.Header.Get("Authorization"))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestARemoteServerIsReachedAndFramedAsThirdParty(t *testing.T) {
	for _, sse := range []bool{false, true} {
		name := map[bool]string{false: "http", true: "sse"}[sse]
		t.Run(name, func(t *testing.T) {
			var seen atomic.Value
			ts := remoteTestServer(t, sse, &seen)
			c, err := ConnectRemote(context.Background(), RemoteConfig{
				Name: "remote", URL: ts.URL, Transport: name,
				Headers: map[string]string{"Authorization": "Bearer tok-123456789"},
			})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer func() { _ = c.Close() }()
			tools, err := c.ListTools(context.Background())
			if err != nil || len(tools) != 1 {
				t.Fatalf("tools %v, %v", tools, err)
			}
			tool := tools[0]
			if tool.Confined || !tool.ReachesNetwork || tool.Remote == "" || tool.Server != "remote" {
				t.Errorf("a remote tool is framed as %+v", tool)
			}
			res, err := c.CallTool(context.Background(), "ping", nil)
			if err != nil || res.Content != "pong" {
				t.Fatalf("call: %+v, %v", res, err)
			}
			if got, _ := seen.Load().(string); got != "Bearer tok-123456789" {
				t.Errorf("the server was sent Authorization %q", got)
			}
		})
	}
}

// A redirect to another origin is not followed, and the token does not go
// with it.
func TestARemoteRedirectElsewhereIsRefused(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		http.Error(w, "nope", 500)
	}))
	defer other.Close()
	redirector := httptest.NewServer(http.RedirectHandler(other.URL+"/mcp", http.StatusTemporaryRedirect))
	defer redirector.Close()
	// Both are 127.0.0.1 but different ports: different origins.
	_, err := ConnectRemote(context.Background(), RemoteConfig{
		Name: "r", URL: redirector.URL, Headers: map[string]string{"Authorization": "Bearer tok-123456789"},
	})
	if err == nil {
		t.Fatal("connected through a redirect to another origin")
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the other origin was contacted %d time(s)", elsewhere.Load())
	}
}

// One message larger than the limit ends the connection rather than being
// read into memory.
func TestARemoteMessageOverTheLimitIsRefused(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"pad":"` + strings.Repeat("x", 4096) + `"}}`))
	}))
	defer big.Close()
	_, err := ConnectRemote(context.Background(), RemoteConfig{Name: "big", URL: big.URL, MaxMessageBytes: 1024})
	if err == nil {
		t.Fatal("a message over the limit was accepted")
	}
}

func TestRemoteURLsAndHeaders(t *testing.T) {
	for _, c := range []struct {
		url string
		ok  bool
	}{
		{"https://mcp.example.com/mcp", true},
		{"http://127.0.0.1:8080/mcp", true},
		{"http://localhost/mcp", true},
		{"http://mcp.example.com/mcp", false},
		{"https://user:pass@mcp.example.com/mcp", false},
		{"ftp://mcp.example.com", false},
		{"mcp.example.com/mcp", false},
	} {
		if _, err := ValidateRemoteURL(c.url); (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.url, err)
		}
	}
	for _, c := range []struct {
		name, value string
		ok          bool
	}{
		{"Authorization", "Bearer x", true},
		{"X-Api-Key", "k", true},
		{"Host", "evil", false},
		{"Bad Name", "v", false},
		{"X-Inject", "v\r\nX-Evil: 1", false},
	} {
		err := ValidateHeader(c.name, c.value)
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.name, err)
		}
		if err != nil && strings.Contains(err.Error(), "X-Evil") {
			t.Errorf("the error repeats the value: %v", err)
		}
	}
}

// A config that is wrong is refused before anything is dialled.
func TestConnectRemoteRefusesABadConfig(t *testing.T) {
	for _, cfg := range []RemoteConfig{
		{Name: "bad name!", URL: "https://mcp.example.com/mcp"},
		{Name: "r", URL: "http://mcp.example.com/mcp"},
		{Name: "r", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"X-Inject": "v\r\nX-Evil: 1"}},
		{Name: "r", URL: "https://mcp.example.com/mcp", Transport: "websocket"},
	} {
		if _, err := ConnectRemote(context.Background(), cfg); err == nil {
			t.Errorf("%+v was connected", cfg)
		}
	}
}

// A redirect within the origin is followed, through a transport the caller
// supplied -- still underneath the guards.
func TestARemoteRedirectWithinTheOriginIsFollowed(t *testing.T) {
	ts := remoteTestServer(t, false, nil)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/mcp", http.StatusTemporaryRedirect)
			return
		}
		proxyTo, _ := url.Parse(ts.URL)
		r.URL.Scheme, r.URL.Host, r.RequestURI = proxyTo.Scheme, proxyTo.Host, ""
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(front.Close)
	c, err := ConnectRemote(context.Background(), RemoteConfig{
		Name: "moved", URL: front.URL + "/old", HTTPClient: &http.Client{Transport: http.DefaultTransport},
	})
	if err != nil {
		t.Fatalf("connect through a same-origin redirect: %v", err)
	}
	defer func() { _ = c.Close() }()
	if tools, err := c.ListTools(context.Background()); err != nil || len(tools) != 1 {
		t.Fatalf("tools %v, %v", tools, err)
	}
}

// A URL the server hands back that leaves the origin is not requested.
func TestTheGuardedTransportStaysOnItsOrigin(t *testing.T) {
	var contacted atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted.Add(1) }))
	defer other.Close()
	origin, _ := url.Parse("https://mcp.example.com")
	g := &guardedTransport{origin: origin, server: "r", next: http.DefaultTransport,
		headers: map[string]string{"Authorization": "Bearer tok-123456789"}}
	req, _ := http.NewRequest(http.MethodGet, other.URL, nil)
	if resp, err := g.RoundTrip(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a request to another origin was sent")
	}
	if contacted.Load() != 0 {
		t.Error("the other origin was contacted")
	}
}

// A server that accepts the connection and never answers the handshake is
// given up on when the handshake's time is up, as unavailable.
func TestARemoteServerThatNeverAnswersIsGivenUpOn(t *testing.T) {
	quit := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-quit:
		}
	}))
	defer silent.Close()
	defer close(quit)
	const handshake = 300 * time.Millisecond
	start := time.Now()
	_, err := ConnectRemote(context.Background(), RemoteConfig{Name: "silent", URL: silent.URL, ConnectTimeout: handshake})
	if !errors.Is(err, ErrServerUnavailable) {
		t.Fatalf("err = %v, want ErrServerUnavailable", err)
	}
	// Given up on at the handshake's limit, not the transport's header timeout
	// (the SDK's own request context is never cancelled).
	if took := time.Since(start); took > 10*handshake {
		t.Errorf("gave up after %s; the handshake limit was %s", took, handshake)
	}
}

// A closed client says it is not connected, and closing twice is harmless.
func TestAClosedRemoteClientIsUnavailable(t *testing.T) {
	ts := remoteTestServer(t, false, nil)
	var logged []string
	c, err := ConnectRemote(context.Background(), RemoteConfig{Name: "r", URL: ts.URL,
		Logf: func(f string, a ...any) { logged = append(logged, f) }})
	if err != nil {
		t.Fatal(err)
	}
	c.logfSafe("note %d", 1)
	if len(logged) != 1 {
		t.Errorf("logged %v", logged)
	}
	(&RemoteClient{}).logfSafe("no logger: no panic")
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	if _, err := c.ListTools(context.Background()); !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("list after close: %v", err)
	}
	if _, err := c.CallTool(context.Background(), "ping", nil); !errors.Is(err, ErrServerUnavailable) {
		t.Errorf("call after close: %v", err)
	}
}
