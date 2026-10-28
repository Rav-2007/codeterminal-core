package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"mochiii/protocol"
)

// WHAT EVERY THIRD-PARTY SESSION SHARES, whatever carries it -- a subprocess's
// pipes (stdioclient.go) or HTTP to a remote server (remoteclient.go): how its
// tools are translated, and how a call is made and its answer flattened. One
// copy, so a server reached over the network is framed exactly as one started
// on this machine: Lane B, Confined:false, its description sanitised.

// listSessionTools lists a session's tools as this package's Tool.
//
// Every tool comes back Confined:false and Lane:third_party, unconditionally
// and without consulting anything the server said. That is not a default this
// function computes -- it is a fact about a program this project did not write,
// and there is no input that changes it. remote is the host a remote server is
// reached at ("" for a subprocess), so an approval prompt can say where the
// arguments go.
func listSessionTools(ctx context.Context, session *sdk.ClientSession, server, remote string,
	logf func(string, ...any)) ([]Tool, error) {
	res, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("%w: listing tools on server %s: %v", ErrServerUnavailable, server, err)
	}

	tools := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if err := ValidateToolName(t.Name); err != nil {
			// Skipped for the same reason and at the same cost as an
			// unserialisable schema below: one unusable tool must not cost the
			// user the server's other tools. See ValidateToolName for why a
			// name is a security-relevant string rather than a label.
			logf("mcp: server %s offered a tool this daemon will not advertise: %v", server, err)
			continue
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			// A tool whose schema will not serialise cannot be advertised to
			// the model: it would arrive as an unusable entry that only widens
			// the menu. Skipped rather than fatal.
			continue
		}
		tools = append(tools, Tool{
			Server: server,
			Name:   t.Name,
			// SANITISED, NOT COPIED. This string is read by the model on
			// every request AND printed to the human at the approval prompt
			// (ToolApprovalRequest.Detail). See SanitiseToolDescription.
			Description: SanitiseToolDescription(t.Description),
			Schema:      schema,
			Lane:        protocol.LaneThirdParty,
			Confined:    false,
			Remote:      remote,
			// A remote server's call SENDS its arguments to that host, so a
			// "yes for this turn" covers those arguments only (grantKey), as
			// for web_search.
			ReachesNetwork: remote != "",
			ReadOnlyHint:   annotationBool(t, readOnly),
			Destructive:    annotationBool(t, destructive),
		})
	}
	SortTools(tools)
	return tools, nil
}

type annotationKind int

const (
	readOnly annotationKind = iota
	destructive
)

// annotationBool reads a server's own hint about its tool, defensively.
//
// They never decide WHETHER a call is asked about (see Tool.ReadOnlyHint). The
// defaults are chosen so that a server saying nothing gets the least reassuring
// reading: absent readOnlyHint reads as "not read-only", and absent
// destructiveHint as destructive, the spec's own default -- neither silence is
// treated as a claim of safety, because neither is one.
//
// Destructive is NOT display only: it also narrows a "yes for this task" to the
// exact arguments approved (grantKey, daemon/agentloop.go). Whether the prompt
// SAYS "marked destructive" is decided where the request is built, which leaves
// it off a read-only tool -- the spec calls destructiveHint meaningful only
// when readOnlyHint is false.
func annotationBool(t *sdk.Tool, kind annotationKind) bool {
	if t.Annotations == nil {
		return false
	}
	switch kind {
	case readOnly:
		return t.Annotations.ReadOnlyHint
	case destructive:
		// The SDK models this as *bool because the spec's default is true.
		// A server that omits it is saying nothing, and "nothing" must not
		// become "safe" -- so an absent hint reads as destructive.
		if t.Annotations.DestructiveHint == nil {
			return true
		}
		return *t.Annotations.DestructiveHint
	}
	return false
}

// isTransportDeath reports whether an error means the connection is gone, as
// distinct from the call having failed.
//
// Sentinel checks first, because those are contractual. The string check is
// the ugly part and it is deliberate: the SDK surfaces a closed pipe as prose
// wrapping an unexported error, so there is nothing else to match on. It is
// narrow, and it is additive -- a future SDK that returns a proper sentinel is
// caught by the lines above without this ever being consulted.
func isTransportDeath(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrServerUnavailable) {
		return true
	}
	text := err.Error()
	return strings.HasSuffix(text, ": EOF") ||
		strings.Contains(text, "file already closed") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection closed")
}

// callSessionTool invokes one tool and flattens its content into text.
//
// args is the model's raw JSON argument object. It is decoded here rather than
// passed through opaquely because the SDK takes a map; a malformed object is
// refused as a tool error the model can see and correct, never guessed at.
func callSessionTool(ctx context.Context, session *sdk.ClientSession, server, name string, args json.RawMessage) (Result, error) {
	var decoded map[string]any
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &decoded); err != nil {
			return Result{
				Content: fmt.Sprintf("the arguments were not a valid JSON object: %v", err),
				IsError: true,
			}, nil
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: decoded})
	if err != nil {
		// A SERVER THAT DIED MID-CALL IS AN UNAVAILABLE SERVER, and it must say
		// so in a way the caller can branch on: the SDK reports a closed stream
		// as a bare io.EOF wrapped in prose, and passed through unclassified
		// the loop could not tell "this server is gone" from "this tool failed".
		if isTransportDeath(err) {
			return Result{}, fmt.Errorf("%w: server %s stopped responding during %s: %v",
				ErrServerUnavailable, server, name, err)
		}
		return Result{}, fmt.Errorf("calling %s on server %s: %w", name, server, err)
	}

	return Result{Content: flattenContent(res.Content), IsError: res.IsError}, nil
}

// flattenContent renders MCP content blocks as text for the model.
//
// Non-text blocks are named rather than dropped: a tool that returned an image
// has told the model something, and silently returning "" would leave the model
// to conclude the call produced nothing and try again.
func flattenContent(content []sdk.Content) string {
	var b strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case *sdk.TextContent:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(v.Text)
		default:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%T content omitted: this tool returned a non-text result]", v)
		}
	}
	return b.String()
}
