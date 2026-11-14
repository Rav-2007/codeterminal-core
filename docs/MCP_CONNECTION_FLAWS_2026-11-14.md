# MCP host: connection-path flaws (2026-11-14)

Scope: how the daemon creates MCP clients and moves data between a server and the
model (`daemon/mcp/`: `stdioclient.go`, `remoteclient.go`, `session.go`,
`registry.go`). Docs were not considered, only code and behaviour.

Method: read the code, then wrote tests that fail when the behaviour is wrong.
Every flaw below was **reproduced by a test**, not inferred. SDK: go-sdk v1.7.0.

**Status (2026-11-14, branch `fix/mcp-connection-hardening`): F1 to F6 are fixed**, each
in its own commit with regression tests. F7 is scope, left open. The flaw tests that
reproduced them were turned into regression tests and the `MCP_FLAWS` gate removed.

```
cd daemon/mcp
go test -race -run 'TestMatrix|TestRender|TestFlatten|TestEventLimit' .   # connection matrix, offline
MCP_LIVE_PUBLIC=1 go test -v -run TestLivePublic .                          # real public servers (internet)
MCP_INTEROP=1 go test -v -run TestInterop .                                 # the reference server over npx
```

## Verdict: does it connect?

Yes. Stdio and remote (streamable HTTP, SSE) clients are created, tools listed,
calls routed through the Registry, and clients torn down, with no race under
`-race`. Against real public servers (2026-11-14):

| Server | Result |
|---|---|
| `https://mcp.deepwiki.com/mcp` (streamable HTTP) | connected in ~1.1 s, 3 tools, `read_wiki_structure` call returned 1278 bytes |
| `https://docs.mcp.cloudflare.com/mcp` (streamable HTTP) | connected in ~0.1 to 0.5 s, 2 tools |
| `https://mcp.notion.com/mcp`, `https://mcp.sentry.dev/mcp` (OAuth, no sign-in given) | refused in ~0.2 s as `ErrServerUnavailable` ("Unauthorized"), no hang |
| `https://mcp.deepwiki.com/sse` | `410 Gone`: the server retired SSE. Not a Mochiii fault |
| reference `server-everything` via `npx` (stdio) | connected, 14 tools, `echo` returned |

Not verified today: the full `mcp add` then model-turn flow. It was not run, so its
status is unchanged.

## Flaws, ranked

### F1 (medium): paged tool lists are silently truncated
`listSessionTools` (`session.go`) calls `session.ListTools(ctx, &ListToolsParams{})`
once and never follows `NextCursor`. The SDK has an iterator (`ClientSession.Tools`)
that does.
Test `TestFlawF1`: server offers 5 tools in pages of 2; the host sees 2. No log
line, no notice. (By reading the code, `find_tools` searches only tools the
daemon already listed, so these should be unreachable there too; not tested.)

**FIXED** (`0e0aabb`). The loop is ours, not `session.Tools`: that SDK iterator follows
cursors with no limit, so a server answering every page with another cursor would keep
it going for ever. It stops at 20 pages, 1000 tools or 8 MiB of listed text, on a
repeated cursor, or when a later page fails; names repeated across pages keep the
first. A list that stops short is **kept and reported** (`ErrToolListIncomplete`): the
Registry offers the tools and carries the error in `ListOutcome`, and the server's
status row stays running with the shortfall in `ToolsError`. Tests:
`TestMatrixPagedToolListIsFollowed`, `...EndlessCursorsStopAtThePageLimit`,
`...RepeatedCursorStopsTheList`, `...ToolCountIsBounded`, `...LaterPageFailureKeepsEarlierTools`,
`...DuplicateToolNamesAcrossPagesAreOne`, `...RegistryKeepsAnIncompleteList`,
`TestListedOutcomesBecomeServerStates`.

### F2 (low to medium): a result with only `structuredContent` reaches the model as ""
`callSessionTool` returns `flattenContent(res.Content)` and never reads
`res.StructuredContent`.
Test `TestFlawF2`: tool answers `{"answer":42}` as structured content only; the
model is given an empty string. The spec tells servers to also send a text block,
so impact depends on the server, but the daemon labels an empty third-party
result "this call succeeded and returned nothing" (`toolresult.go:243,252`), so the
model is told a real answer was empty. (An earlier draft said this also feeds stall
detection; it does not: third-party tools weigh 0 in `stall.go`.)

**FIXED** (`9742724`). When no block carries text for the model, the structured value
is rendered as JSON, never beside text that is already there (a conforming server sends
both). It is rendered with HTML escaping off: `json.Marshal` writes `<` as a unicode
escape that the delimiter defence does not match and the model still reads as a tag.
Everything goes into `Result.Content`, which is what makes it safe: that one string is
truncated, scrubbed, delimiter-defused and control-stripped by `renderToolResult`.
`PreNeutralized` is not touched. Tests: `TestRenderCallResult*`,
`TestRenderStructuredDoesNotEscapeHTML`, `TestMatrixStructuredAndResourceContentReachTheModel`,
and `TestForgedEnvelopeTagThroughEveryContentPath` (package `main`), which drives a
forged closing tag through every content path with a real client.

### F3 (low to medium): embedded text resources are reported as omitted
`flattenContent` handles only `*TextContent`. An `EmbeddedResource` carrying text
becomes `[*mcp.EmbeddedResource content omitted: ...]` although the text is in the
result. Test `TestFlawF3`. Same fix area as F2. Binary blobs may stay omitted.

**FIXED** (`9742724`, with F2). An embedded text resource is shown under a one-line
header (address and type cut to 256 bytes, control characters turned into spaces).
Blobs, images, audio and links stay omitted but are named by kind and by what the
server said ("image content omitted: ... (image/png, 12 bytes)"), never by a Go type.
Tests: `TestFlattenContent*`, `TestCapFieldCutsOnACharacterBoundary`.

### F4 (medium to low): the HTTP message-size bound can be bypassed with newlines
`messageLimitReader` counts bytes since the last newline. That is exact for stdio
(newline-delimited) and wrong for an HTTP `application/json` body, where
whitespace between tokens may contain newlines. `guardedTransport` wraps every
remote body in the same reader.
Test `TestFlawF4`: against a 1 MiB limit, a 5 MiB single-line message is refused,
and the same message with a newline every 1000 bytes is accepted.
Not measured: how far past the limit it goes, or the memory cost. The bound is
5x exceeded at least, so the claim "a server cannot make this daemon allocate
without limit" (comment in `remoteclient.go`) does not hold for remote servers.

**FIXED** (`f11b67f`). An HTTP response is bounded by what it is (`httpbound.go`):
`text/event-stream` per EVENT (an event ends at an empty line, as the SDK's own parser
ends it, so a message over many `data:` lines is counted whole and a long session of
small events is never killed); anything else, the whole body. The stdio bound is
untouched. Tests: `TestMatrixHTTPJSONBodyIsBoundedWhateverItsNewlines`,
`...SSEEventIsBoundedAcrossItsDataLines`, the negative control
`...LongSSEStreamOfSmallEventsIsNotKilled` (3000 events, about 3x the limit, succeeds),
and `TestEventLimitReaderCountsPerEvent` (LF and CRLF).

### F5 (low to medium): losing a remote session is not classified as "unavailable"
When a remote server answers a later request with 404 (session forgotten) the
error is `calling "tools/call": ... session not found`, which does not satisfy
`errors.Is(err, ErrServerUnavailable)`. `isTransportDeath` matches EOF, closed
pipe and a few strings, but not HTTP session loss. The SDK does return a typed
`sdk.ErrSessionMissing`, but `callSessionTool` wraps the cause with `%v`, so the
sentinel is lost from the error chain even where it would be recognised.
Test `TestFlawF5`. Effect: the loop cannot tell "this server is gone" from "this
tool failed", so the user may not get the degraded-server notice. The downstream
effect in the loop was not traced.

**FIXED** (`dd8d671`). `isTransportDeath` knows `sdk.ErrSessionMissing` and
`sdk.ErrConnectionClosed`, and the call and list paths wrap the cause with `%w`. A
remote client keeps its config and, on a forgotten session, opens **one** new session
(single-flight) and retries once: a 404 means the server never acted on the request, so
the retry cannot repeat a side effect. A concurrent caller whose call fails with
"connection closed" because a neighbour got the 404 is recognised by a per-session
`lost` flag raised in the guarded transport, before the SDK reacts (found by a test
that failed under `-race` with 20 concurrent callers). A closed connection **without**
that flag is not retried (it may be a network drop after the request ran), and a stdio
server is never reconnected. Tests: `TestMatrixLostSession*`,
`TestMatrixConcurrentLossOpensOneSession`, `TestMatrixClosedConnectionIsNotRetriedUnlessSessionWasLost`,
`TestIsTransportDeathRecognisesTheSDKsSentinels`.

### F6 (medium, evidence is one run): the default connect timeout is shorter than a cold `npx` start
`DefaultConnectTimeout` is 20 s and the budget default resolves to 0, so 20 s
applies. The interop run on this machine took 21.8 s to handshake with
`npx -y @modelcontextprotocol/server-everything`; the interop test itself passes
`ConnectTimeout: 120s` to get around that. A user who adds an `npx` server for the
first time can therefore see it fail, and succeed on the next try once npm has
cached the package.
A per-server `connect_timeout_seconds` exists (cap 120), but nothing sets it for a
first run. Not tested end to end; one measurement, machine and network dependent.

**FIXED** (`2c87b46`), **not re-measured end to end**. `connectTimeout` is the one
resolver every path uses (a turn, the check, the list/trial deadline): the server's own
setting, else the budget's, else, for a launcher (`npx uvx bunx pnpx docker`, by program
name, ignoring a path and a `.exe`/`.cmd`/`.bat` suffix), **90 s**, else 20 s. Plain
programs and remote servers are unchanged. A launcher that still runs out of time on
`add` is told it is "still starting" instead of "fix the command". Trade-off: a launcher
that really hangs now holds its first start for up to 90 s; connects run in parallel and
neither client times the request out. Tests: `TestConnectTimeout*`,
`TestEveryPathAgreesOnALaunchersBound`, `TestTimeoutHintNamesTheBoundThatRanOut`,
`TestAnAddedLauncherThatIsStillDownloadingSaysSoNotFixTheCommand`. What was not done: a
real cold-cache `npx` add through the daemon, whose confirm prompt is the user's to
answer.

### F7 (scope, not a bug): only the tools surface of MCP is used
No calls for resources, prompts, sampling or roots anywhere in `daemon/`. Servers
whose value is in resources or prompts show only their tools. Server-initiated
notifications are not consumed either (the standalone SSE stream is disabled in
`remoteclient.go`); tool lists are re-read each turn, so a changed tool list is
still picked up.

## Checked and found sound

Environment scrub (child sees only `PATH`, `HOME`, allow-listed names); no shell;
process-tree teardown; cross-origin and https-to-http redirects refused; header
only sent to the server's origin; unreachable, 404, HTML-200 and 418 endpoints
each fail fast as `ErrServerUnavailable`; closed clients report unavailable on
every verb and close twice safely; 40 concurrent calls on one client return the
right answers with no race; a cancelled call returns in well under a second;
malformed arguments are refused to the model rather than sent.

## Not tested

DNS or private-address targets for remote URLs (`https://` to a private IP is
allowed; reasonable for internal servers and gated by the user's approval); OAuth
sign-in end to end; Windows job-object teardown; the sandbox (`bwrap`/landlock)
wrapping a live server.

## Files

- `daemon/mcp/connect_matrix_test.go`: offline connection matrix and the regression tests above
- `daemon/mcp/rawmcp_test.go`: a scriptable bare-JSON/SSE MCP server (expired sessions, scripted pages, padding, event streams)
- `daemon/mcp/content_render_test.go`, `daemon/mcpforgedcontent_test.go`: what the model is shown, and that no content path can close the envelope
- `daemon/mcpstatelist_test.go`, `daemon/mcplaunchtimeout_test.go`: status rows for an incomplete list; the launcher bound
- `daemon/mcp/connect_public_live_test.go`: opt-in live public-server test
