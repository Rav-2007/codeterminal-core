// A real MCP server, used by stdioclient_test.go to exercise the actual SDK
// transport rather than a fake. Kept deliberately small: it exists to prove the
// wire works and that the environment discipline reaches a genuine subprocess.
//
// Under testdata/ so `go build ./...` and the coverage ratchet ignore it; the
// test builds it by explicit path.
package main

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
	Text string `json:"text" jsonschema:"the text to echo back"`
}

type noArgs struct{}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "echoserver", Version: "v0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "echo",
		Description: "Echo the supplied text back to the caller.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}},
		}, nil, nil
	})

	// Reports the environment variable NAMES this process can see. This is how
	// the test proves ServerEnv's allow-list reaches a real subprocess rather
	// than merely producing the right []string in memory.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "env_names",
		Description: "Report the environment variable names visible to this server.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		var names []string
		for _, kv := range os.Environ() {
			names = append(names, strings.SplitN(kv, "=", 2)[0])
		}
		sort.Strings(names)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(names, ",")}},
		}, nil, nil
	})

	// Always fails, so the test can check that a tool-level error reaches the
	// model as a readable result rather than a transport error.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "always_fails",
		Description: "Always returns an error result.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "this tool always fails"}},
		}, nil, nil
	})

	// THE SERVER THAT CLAIMS TO BE HARMLESS.
	//
	// Every annotation here is the server's own description of itself, and a
	// third-party server is exactly the party with a motive to describe itself
	// well. Without a tool that actually makes such a claim, a client that
	// STARTED trusting one would pass every test in this package: the other
	// three tools assert nothing, so there is no input for the trust to show up
	// on. Measured -- neutering StdioClient.ListTools to read confinement off
	// this annotation left the whole suite green until this tool existed.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "claims_to_be_safe",
		Description: "Declares itself read-only and non-destructive. Says so, at least.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: boolPtr(false),
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "a claim is not a property"}},
		}, nil, nil
	})

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

func boolPtr(b bool) *bool { return &b }
