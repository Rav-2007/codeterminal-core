# MCP host: connection-path flaws (2026-11-14)

Scope: how the daemon creates MCP clients and moves data between a server and the
model (`daemon/mcp/`: `stdioclient.go`, `remoteclient.go`, `session.go`,
`registry.go`). Docs were not considered, only code and behaviour.

Method: read the code, then wrote tests that fail when the behaviour is wrong.
Every flaw below is **reproduced by a test**, not inferred. SDK: go-sdk v1.7.0.

Run them:

```
cd daemon/mcp
go test -race -run TestMatrix .                      # connection matrix, offline, all pass
MCP_FLAWS=1 go test -run TestFlaw .                  # the flaws below, each fails for real
MCP_LIVE_PUBLIC=1 go test -v -run TestLivePublic .   # real public servers (internet)
```

Without `MCP_FLAWS=1` the flaw tests are skipped with their id, so `make check`
stays green and the flaw stays visible in `go test -v`.

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
Fix: loop on `NextCursor` (or use `session.Tools`), with an upper bound on pages.

### F2 (low to medium): a result with only `structuredContent` reaches the model as ""
`callSessionTool` returns `flattenContent(res.Content)` and never reads
`res.StructuredContent`.
Test `TestFlawF2`: tool answers `{"answer":42}` as structured content only; the
model is given an empty string. The spec tells servers to also send a text block,
so impact depends on the server, but the daemon labels an empty third-party
result "this call succeeded and returned nothing" (`toolresult.go:243,252`), so the
model is told a real answer was empty. (An earlier draft said this also feeds stall
detection; it does not: third-party tools weigh 0 in `stall.go`.)
Fix: if `Content` is empty and `StructuredContent` is set, render it as JSON.

### F3 (low to medium): embedded text resources are reported as omitted
`flattenContent` handles only `*TextContent`. An `EmbeddedResource` carrying text
becomes `[*mcp.EmbeddedResource content omitted: ...]` although the text is in the
result. Test `TestFlawF3`. Same fix area as F2. Binary blobs may stay omitted.

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
Fix: for HTTP, bound total bytes per response body, not bytes per line.

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
Fix: classify the SDK's session-not-found / failed-to-connect errors as
unavailable, and reconnect once if the transport allows it.

### F6 (medium, evidence is one run): the default connect timeout is shorter than a cold `npx` start
`DefaultConnectTimeout` is 20 s and the budget default resolves to 0, so 20 s
applies. The interop run on this machine took 21.8 s to handshake with
`npx -y @modelcontextprotocol/server-everything`; the interop test itself passes
`ConnectTimeout: 120s` to get around that. A user who adds an `npx` server for the
first time can therefore see it fail, and succeed on the next try once npm has
cached the package.
A per-server `connect_timeout_seconds` exists (cap 120), but nothing sets it for a
first run. Not tested end to end; one measurement, machine and network dependent.
Fix: warm start (a longer first-run bound, or retry once on timeout), or say
"first run can take a minute" in the add flow.

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

- `daemon/mcp/connect_matrix_test.go`: offline connection matrix (passes)
- `daemon/mcp/connect_flaws_test.go`: F1 to F5 as tests (skipped unless `MCP_FLAWS=1`)
- `daemon/mcp/connect_public_live_test.go`: opt-in live public-server test
