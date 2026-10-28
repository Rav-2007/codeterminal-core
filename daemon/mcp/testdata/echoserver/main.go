// A real MCP server, used by stdioclient_test.go to exercise the actual SDK
// transport rather than a fake. Kept deliberately small: it exists to prove the
// wire works and that the environment discipline reaches a genuine subprocess.
//
// Under testdata/ so `go build ./...` and the coverage ratchet ignore it; the
// test builds it by explicit path.
package main

import (
	"context"
	"fmt"
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
	// STARTUP STDERR, for the R1.5 redaction test. A real MCP server that
	// echoes its own configuration at startup is the shape that put a
	// credential on the user's screen via `/mcp-server`; this reproduces it on
	// demand rather than hoping a third-party server does it during a test.
	// Named variable only, so this fixture never prints an environment it was
	// not asked to.
	if name := os.Getenv("ECHOSERVER_PRINT_ENV"); name != "" {
		fmt.Fprintf(os.Stderr, "startup: %s=%s\n", name, os.Getenv(name))
	}

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

	// WHERE THIS PROCESS WAS STARTED. Offered only when asked for, so every
	// other test still sees the tools it counts. This is how a test proves the
	// folder a server starts in is the one the daemon chose -- a launcher such
	// as npx decides what to run partly from that folder, so starting in the
	// project being worked on lets the project choose.
	if len(os.Args) > 1 && os.Args[1] == "--report-cwd" {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "cwd",
			Description: "Report this server's working directory.",
		}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			dir, err := os.Getwd()
			if err != nil {
				dir = "error: " + err.Error()
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: dir}},
			}, nil, nil
		})
	}

	// A SERVER THAT NEEDS A KEY, the way most real ones do: started without
	// the variable it names, it says so on stderr and exits before the
	// handshake (perplexity's "PERPLEXITY_API_KEY environment variable is
	// required"). With it, it offers "secret", which says whether the value
	// arrived -- never the value.
	if len(os.Args) > 2 && os.Args[1] == "--require-env" {
		name := os.Args[2]
		if os.Getenv(name) == "" {
			fmt.Fprintf(os.Stderr, "Error: %s environment variable is required\n", name)
			os.Exit(1)
		}
		mcp.AddTool(s, &mcp.Tool{
			Name:        "secret",
			Description: "Say how long the required variable's value is.",
		}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s has %d bytes", name, len(os.Getenv(name)))}},
			}, nil, nil
		})
	}

	// THE DESTRUCTIVE LABEL, three ways a server describes a tool. "reads"
	// declares readOnlyHint and leaves destructiveHint out, as the Filesystem
	// server's read tools do; "says_nothing" sends an empty annotations block;
	// "destroys" declares destructiveHint. Offered only when asked for.
	if len(os.Args) > 1 && os.Args[1] == "--read-only-hints" {
		for _, t := range []*mcp.Tool{
			{Name: "reads", Description: "Read something.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			{Name: "says_nothing", Description: "Do something.", Annotations: &mcp.ToolAnnotations{}},
			{Name: "destroys", Description: "Delete something.", Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)}},
		} {
			mcp.AddTool(s, t, func(_ context.Context, _ *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "done: " + args.Text}},
				}, nil, nil
			})
		}
	}

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

func boolPtr(b bool) *bool { return &b }
