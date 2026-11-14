package mcp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// LIVE: this daemon's remote client against real public MCP servers.
//
// NOT PART OF `make check`: it needs the internet and depends on third parties
// staying up. Opt in with MCP_LIVE_PUBLIC=1. It sends no credentials and no
// project data; the one tool call names a public GitHub repository.
//
// A single server being down is logged, not failed -- the test fails only if
// NO public server could be reached, which is the question being asked.

type publicServer struct {
	name, url, transport string
	// call, if set, is a tool called after listing, with its JSON arguments.
	call, args string
}

var publicServers = []publicServer{
	{name: "deepwiki", url: "https://mcp.deepwiki.com/mcp", transport: "http",
		call: "read_wiki_structure", args: `{"repoName":"modelcontextprotocol/go-sdk"}`},
	{name: "cloudflare-docs", url: "https://docs.mcp.cloudflare.com/mcp", transport: "http"},
	// SSE has no live entry: the public servers this was tried against have
	// retired it (deepwiki's /sse answers 410 Gone, 2026-11-14). The SSE client
	// is covered offline by TestARemoteServerIsReachedAndFramedAsThirdParty.
}

func TestLivePublicRemoteServers(t *testing.T) {
	if os.Getenv("MCP_LIVE_PUBLIC") == "" {
		t.Skip("set MCP_LIVE_PUBLIC=1 to connect to real public MCP servers")
	}
	reached := 0
	for _, s := range publicServers {
		t.Run(s.name, func(t *testing.T) {
			start := time.Now()
			c, err := ConnectRemote(context.Background(), RemoteConfig{
				Name: s.name, URL: s.url, Transport: s.transport, ConnectTimeout: 30 * time.Second,
			})
			if err != nil {
				t.Logf("NOT REACHED: %v", err)
				return
			}
			defer func() { _ = c.Close() }()
			tools, err := c.ListTools(context.Background())
			if err != nil {
				t.Logf("connected in %s but tools/list failed: %v", time.Since(start).Round(time.Millisecond), err)
				return
			}
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Name)
				if tool.Confined || !tool.ReachesNetwork || tool.Remote == "" {
					t.Errorf("tool %s framed as %+v", tool.Name, tool)
				}
			}
			reached++
			t.Logf("REACHED in %s: %d tool(s): %s", time.Since(start).Round(time.Millisecond), len(tools), strings.Join(names, ", "))
			if s.call != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				res, err := c.CallTool(ctx, s.call, []byte(s.args))
				if err != nil {
					t.Logf("call %s failed: %v", s.call, err)
					return
				}
				head := res.Content
				if len(head) > 160 {
					head = head[:160] + "..."
				}
				t.Logf("call %s -> isError=%v, %d bytes: %q", s.call, res.IsError, len(res.Content), head)
			}
		})
	}
	if reached == 0 {
		t.Fatal("no public MCP server could be reached")
	}
}

// A public server that wants a sign-in must fail fast and as "unavailable" when
// no OAuth handler is configured (the daemon turns the 401 into "needs
// sign-in" a layer above) -- never hang on a browser nobody opened.
func TestLivePublicOAuthServerWithoutSignInFailsFast(t *testing.T) {
	if os.Getenv("MCP_LIVE_PUBLIC") == "" {
		t.Skip("set MCP_LIVE_PUBLIC=1 to connect to real public MCP servers")
	}
	for name, url := range map[string]string{
		"notion": "https://mcp.notion.com/mcp",
		"sentry": "https://mcp.sentry.dev/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			c, err := ConnectRemote(context.Background(), RemoteConfig{
				Name: name, URL: url, ConnectTimeout: 20 * time.Second,
			})
			if err == nil {
				_ = c.Close()
				t.Skip("server answered without a sign-in; nothing to check")
			}
			if !errors.Is(err, ErrServerUnavailable) {
				t.Errorf("error is not ErrServerUnavailable: %v", err)
			}
			t.Logf("refused in %s: %v", time.Since(start).Round(time.Millisecond), err)
			if time.Since(start) > 25*time.Second {
				t.Errorf("took too long to give up")
			}
		})
	}
}
