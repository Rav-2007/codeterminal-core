package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
