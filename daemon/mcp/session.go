package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"mochiii/protocol"
)

// THE BOUNDS ON A TOOL LIST. A server's tools/list is paged, and the SDK's own
// iterator follows cursors with no limit -- a server that answers every page
// with another cursor would keep it going for ever -- so the loop is ours, and
// stops at whichever of these it meets first. Each is far above a real server
// (the largest public ones list well under a hundred tools) and far below
// anything that would hurt: one page is already bounded by the message limit,
// so these bound how many pages, not how big one is.
//
// WHEN A BOUND IS MET THE LIST IS KEPT, NOT DROPPED, and the shortfall is
// reported (ErrToolListIncomplete). Cutting a list short without saying so is
// the flaw this replaced: a server's later tools simply never existed.
const (
	maxToolListPages  = 20
	maxToolsPerServer = 1000
	maxToolListBytes  = 8 << 20
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
	var (
		tools      []Tool
		names      = map[string]bool{}
		cursors    = map[string]bool{}
		cursor     string
		listBytes  int
		incomplete error // why the list stops short, if it does
	)

pages:
	for page := 1; ; page++ {
		res, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			if page == 1 {
				return nil, fmt.Errorf("%w: listing tools on server %s: %w", ErrServerUnavailable, server, err)
			}
			// Page one worked, a later page did not: keep what there is and
			// say so, rather than throw away tools the server did offer.
			incomplete = fmt.Errorf("%w: server %s: page %d of its tool list failed after %d tool(s): %w",
				ErrToolListIncomplete, server, page, len(tools), err)
			break
		}

		for _, t := range res.Tools {
			if err := ValidateToolName(t.Name); err != nil {
				// Skipped for the same reason and at the same cost as an
				// unserialisable schema below: one unusable tool must not cost the
				// user the server's other tools. See ValidateToolName for why a
				// name is a security-relevant string rather than a label.
				logf("mcp: server %s offered a tool this daemon will not advertise: %v", server, err)
				continue
			}
			if names[t.Name] {
				// A name repeated across pages: the first is kept. Two tools of
				// one name would be one menu entry the model cannot tell apart.
				logf("mcp: server %s listed the tool %q more than once; the first is used", server, t.Name)
				continue
			}
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				// A tool whose schema will not serialise cannot be advertised to
				// the model: it would arrive as an unusable entry that only widens
				// the menu. Skipped rather than fatal.
				continue
			}
			if len(tools) >= maxToolsPerServer {
				incomplete = fmt.Errorf("%w: server %s offers more than %d tools; the rest are not listed",
					ErrToolListIncomplete, server, maxToolsPerServer)
				break pages
			}
			if listBytes += len(t.Name) + len(t.Description) + len(schema); listBytes > maxToolListBytes {
				incomplete = fmt.Errorf("%w: server %s's tool list is larger than %d MiB; the rest are not listed",
					ErrToolListIncomplete, server, maxToolListBytes>>20)
				break pages
			}
			names[t.Name] = true
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
				OpenWorldHint:  annotationBool(t, openWorld),
			})
		}

		next := res.NextCursor
		if next == "" {
			break
		}
		if cursors[next] {
			incomplete = fmt.Errorf("%w: server %s sent the same page cursor twice; its tool list was cut off after %d tool(s)",
				ErrToolListIncomplete, server, len(tools))
			break
		}
		if page >= maxToolListPages {
			incomplete = fmt.Errorf("%w: server %s's tool list runs past %d pages; the rest are not listed",
				ErrToolListIncomplete, server, maxToolListPages)
			break
		}
		cursors[next] = true
		cursor = next
	}
	SortTools(tools)
	if incomplete != nil {
		logf("mcp: %v", incomplete)
	}
	return tools, incomplete
}

type annotationKind int

const (
	readOnly annotationKind = iota
	destructive
	openWorld
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
	case openWorld:
		// Also *bool with a spec default of true -- and here an absent hint
		// reads as false, the other way round from destructive, for the same
		// reason: "nothing said" must not become a claim. See Tool.OpenWorldHint.
		return t.Annotations.OpenWorldHint != nil && *t.Annotations.OpenWorldHint
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
//
// The SDK's own sentinels come first: ErrSessionMissing is what an HTTP 404 on
// a live session becomes (the server forgot it), and ErrConnectionClosed is a
// write to a connection that is closed or closing. Matching them by type is why
// callers below wrap the cause with %w and not %v -- a %v flattens the chain
// and the next layer up can no longer tell a forgotten session from a failed
// tool (flaw F5).
func isTransportDeath(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrServerUnavailable) ||
		errors.Is(err, sdk.ErrSessionMissing) || errors.Is(err, sdk.ErrConnectionClosed) {
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
			return Result{}, fmt.Errorf("%w: server %s stopped responding during %s: %w",
				ErrServerUnavailable, server, name, err)
		}
		return Result{}, fmt.Errorf("calling %s on server %s: %w", name, server, err)
	}

	return Result{Content: renderCallResult(res), IsError: res.IsError}, nil
}

// renderCallResult is everything the model is told about one tool result.
//
// The content blocks come first (flattenContent). A result may ALSO carry a
// structured value -- the spec's structuredContent -- and a server that sends
// ONLY that, with no block the model can read, used to reach the model as an
// empty string, which the daemon then labels "this call succeeded and returned
// nothing" (toolresult.go): a real answer reported as no answer.
//
// So when no block carries text for the model, the structured value is rendered
// as JSON. It is rendered ONLY then, never beside text that is already there:
// a conforming server sends both, and showing both would pay for the answer
// twice. It goes into Content, which is what makes it safe: Content is the one
// string the daemon truncates, scrubs for secrets, defuses delimiters in and
// strips control characters from (daemon/toolresult.go), and a second field
// would be a second path around that.
func renderCallResult(res *sdk.CallToolResult) string {
	text := flattenContent(res.Content)
	if res.StructuredContent == nil || hasModelText(res.Content) {
		return text
	}
	structured := renderStructured(res.StructuredContent)
	switch {
	case structured == "":
		return text
	case text == "":
		return structured
	}
	return text + "\n" + structured
}

// hasModelText reports whether any block carries text the model can read: a
// text block, or an embedded resource whose contents are text.
func hasModelText(content []sdk.Content) bool {
	for _, c := range content {
		switch v := c.(type) {
		case *sdk.TextContent:
			return true
		case *sdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				return true
			}
		}
	}
	return false
}

// renderStructured is a structured value as compact JSON, with HTML escaping
// OFF. json.Marshal turns "<" into \u003c, and a forged delimiter written that
// way would no longer look like a tag to the neutralisation that runs later --
// while still reading as one to the model. Unescaped, "<" stays "<" and is
// defused like any other text.
func renderStructured(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// flattenContent renders MCP content blocks as text for the model.
//
// Non-text blocks are named rather than dropped: a tool that returned an image
// has told the model something, and silently returning "" would leave the model
// to conclude the call produced nothing and try again. The name is a word the
// model can use ("image", "audio") and what the server said about it (its type
// and size), never a Go type name: this once printed "*mcp.EmbeddedResource".
//
// An embedded resource whose contents are TEXT is shown, headed by where the
// server says it came from. Its address and type are the server's words, so they
// are cut to one short line here; the text goes through the same scrubbing and
// delimiter defusing as any other (it is part of Content).
func flattenContent(content []sdk.Content) string {
	var b strings.Builder
	for _, c := range content {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch v := c.(type) {
		case *sdk.TextContent:
			b.WriteString(v.Text)
		case *sdk.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				b.WriteString(resourceHeader(v.Resource.URI, v.Resource.MIMEType))
				b.WriteString("\n")
				b.WriteString(v.Resource.Text)
				continue
			}
			b.WriteString(omitted("resource", mimeOf(v.Resource), blobLen(v.Resource)))
		case *sdk.ImageContent:
			b.WriteString(omitted("image", v.MIMEType, len(v.Data)))
		case *sdk.AudioContent:
			b.WriteString(omitted("audio", v.MIMEType, len(v.Data)))
		case *sdk.ResourceLink:
			b.WriteString(omitted("resource link", capField(v.URI, maxContentFieldBytes), 0))
		default:
			b.WriteString(omitted("", "", 0))
		}
	}
	return b.String()
}

// maxContentFieldBytes bounds an address or type a server put in a result
// before it is shown to the model: they are the server's words, and a header
// is not the place for a paragraph of them.
const maxContentFieldBytes = 256

func mimeOf(r *sdk.ResourceContents) string {
	if r == nil {
		return ""
	}
	return r.MIMEType
}

func blobLen(r *sdk.ResourceContents) int {
	if r == nil {
		return 0
	}
	return len(r.Blob)
}

// resourceHeader is the line before an embedded text resource's contents.
func resourceHeader(uri, mime string) string {
	h := "[resource " + capField(uri, maxContentFieldBytes)
	if mime != "" {
		h += " (" + capField(mime, maxContentFieldBytes) + ")"
	}
	return h + "]"
}

// omitted is the notice for a block the model is not shown. It keeps the words
// "content omitted" and "non-text": tests and the model's own reading of an
// earlier version both rely on them.
func omitted(kind, detail string, size int) string {
	label := "content omitted"
	if kind != "" {
		label = kind + " content omitted"
	}
	var facts []string
	if detail != "" {
		facts = append(facts, capField(detail, maxContentFieldBytes))
	}
	if size > 0 {
		facts = append(facts, fmt.Sprintf("%d bytes", size))
	}
	notice := "[" + label + ": this tool returned a non-text result"
	if len(facts) > 0 {
		notice += " (" + strings.Join(facts, ", ") + ")"
	}
	return notice + "]"
}

// capField makes a server-supplied address or type one short line: control
// characters (a line break among them) become spaces and the rest is cut at a
// character boundary.
func capField(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
