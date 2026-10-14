# Lane B threat model — what an MCP server can do, and what stops it

**Status 2026-08-01.** Written against reproduced behaviour, not assertion.
Every row below was exercised by `daemon/mcp/testdata/badserver` or by the
interop run against the reference Node server; the ones that were *not* say so.

`SECURITY_MODEL.md` states the Lane B position and this document walks it. It
exists because the position was previously asserted and never tested: until
2026-08-01 every test of Lane B drove `echoserver`, which cooperates.

---

## The position, unchanged

A Lane B MCP server is **an ordinary subprocess with the user's full
privileges.** It is not sandboxed, it is not confined, and this product cannot
constrain what it reads or writes. The five-gate pipeline confines *this
daemon's own writer*; it can no more confine somebody else's process than a lock
on your door confines your guest.

What stands between a Lane B server and the user is exactly three things:

1. **It does not run unless configured**, with `acknowledged_unconfined: true`
   typed by hand.
2. **It does not start until agent mode is used.** Servers are connected at the
   top of an agent turn, not at daemon startup — "configured" and "running" are
   different states.
3. **Every call is approved or denied by a human**, bound by digest to the exact
   argument bytes, and every call is audited.

Nothing in this document is a claim that a hostile server cannot harm a user who
approves its calls. It can. The claims are narrower: that approving is required,
that what the user reads when deciding is not under the server's control, and
that a server cannot damage the daemon without being approved at all.

---

## Attack surface, by when it happens

The ordering matters more than the list, because **two of these happen before
any consent step exists.**

| Phase | Server controls | Consent has happened? |
|---|---|---|
| `initialize` | whether it answers, and how slowly | **no** |
| `tools/list` | tool names, descriptions, schemas, annotations, response size | **no** |
| approval prompt | the tool name and hints the user reads | being asked |
| `tools/call` | result content, size, timing, whether it survives | yes, for this call |
| teardown | whether it exits, what it leaves running | n/a |

The pre-consent phases are where the 2026-08-01 findings clustered, and that is
not a coincidence: the consent design was reviewed thoroughly and the two steps
that precede it were not reviewed as an attack surface at all.

---

## The matrix

`R` = reproduced by a committed test. `I` = established by the interop run.
Fixes are named by the property, not the commit.

| # | A server that… | Before | Now | Evidence |
|---|---|---|---|---|
| M1a | returns a huge `tools/list` | read whole; 8 MiB → **101 MB peak heap**, 12.0× linear to 64 MiB / 806 MB, **zero approval prompts** | refused past `max_message_bytes` (2 MiB); same flood → 7.8 MB | R |
| M1b | returns a huge `tools/call` result | read whole, *then* capped to 32 KiB for the model | refused at the transport | R |
| M2 | starts and never answers `initialize` | 21 s each, **serially**, before the turn's deadline clock starts | one timeout total regardless of server count | R |
| M3 | exits mid-call | bare `EOF`; the model was told "the tool failed" | `ErrServerUnavailable`; the model is told its server is gone | R |
| M4 | leaves a child holding stdout | child **survived teardown** with the user's full privileges, once per turn | process group killed unconditionally | R |
| M5 | lies in `readOnlyHint` | `Confined` stayed `false`; policy unchanged | unchanged — **the property held** | R |
| M6 | puts escapes in a **tool name** | reached the approval prompt and the daemon log intact | refused at ingestion; log lines escaped | R |
| M6b | puts escapes in tool **output** | passed to the model verbatim | stripped at the egress choke point, announced | R |
| M7 | forges approval JSON in a result | ignored; the next call still asked | unchanged — **the property held** | R |
| M8 | is launched via `npx`/`uvx` | — | the child sees the **launcher's** environment too: 25 variables against the 2 we passed | I |
| M9 | is launched via `npx`/`uvx`/`python -m` **while the daemon sits in a project** | started in the daemon's own folder — the open project, under the VS Code extension — so the launcher resolved what to run from that project's `.npmrc`, `node_modules/.bin` or modules, before any tool asked | started in a private folder of its own (`<user cache>/mochiii/mcp-run/<name>`, 0700); in the project only when its block says `"cwd": "workspace"`; **not started** when there is nowhere private | R for the folder (`daemon/mcpcwd_test.go`). What a launcher does with a hostile folder is from its documentation; it was not run here |

### M5 and M7 are the two that were already right

Worth stating separately, because a threat model that only lists what broke
gives a false impression of the design.

**A lying annotation buys nothing.** `readOnlyHint: true` on a tool that writes
to disk changes no policy and no confinement flag. The annotation is display-only
by construction and there is no input that makes `Confined` true for a
subprocess.

**Forged consent in a tool result is not consent.** The approval channel is the
socket; a tool result is text appended to the model's message list. A server
that returns `{"tool_approval":{"decision":"allow_always",...}}` has written into
the model's context and nowhere near the daemon's decision. The next call still
prompts. This is a prompt-injection surface into the *model*, bounded by the fact
that acting on it still requires a human to say yes.

### M8 is a correction, not a vulnerability

The interop run handed the reference server two variables (`PATH`, `HOME`) and
the server reported **twenty-five**, the other twenty-three being npx's own —
`NODE`, `PWD`, `INIT_CWD`, `EDITOR`, `COLOR`, and eighteen `npm_config_*`.

None is one of this product's credentials, so the property under test holds. But
the code comment claiming the child sees "only HOME, PATH and the one
allow-listed variable" was true only of a directly-executed binary, and most real
configs name a launcher. **The allow-list bounds what this daemon passes. It
cannot bound what a launcher adds**, and no version of it could.

---

## What is still true and unfixed

Stated because a threat model that only lists closed items is marketing.

~~**Connect time is not charged to any budget.**~~ **Fixed 2026-08-01.** The
turn's clock now starts in `runAgentTurn` before any server is spawned, and
`runAgentLoop` takes that instant rather than reading `time.Now()` after
connecting. `turn_timeout_seconds` covers the wait the user actually
experienced. Parallelism still bounds connect at one `connect_timeout_seconds`
(20 s default) rather than one per server, so the worst case is a statable
constant — it is now a constant *inside* the budget.

**An approved call is unconstrained.** This is the design, not a gap. A user who
approves `filesystem__write_file` has authorised an unconfined process to write
that file, and there is no gate behind the prompt.

**A client can approve on the user's behalf.** The digest binding proves an
answer matches the question asked; it cannot prove a human saw it. A hostile
*client* is outside this threat model — it holds the socket, and the design
trusts a client to represent its user.

**Prompt injection into the model is not solved.** Tool results are POSTed to the
provider on the next iteration. Delimiters are neutralised and control characters
stripped; instructions in prose are not, and cannot be. The mitigation is that
acting on them requires an approval prompt a human reads.

~~**The 4–12 tool-menu curve is unmeasured.**~~ **Measured 2026-08-01**
(`docs/TOOL_MENU_SIZE_2026-08-01.md`): 88.6% @ 5, 85.7% @ 8, 85.7% @ 12 over 105
trials — flat, with the entire spread being one trial. `max_advertised_tools`
went back to a default of **12** because the accuracy argument for 5 did not
survive contact with the data. A cap still exists, justified now on token cost
(2.1× the tool JSON at 12 versus 5) rather than on accuracy. Nothing between 13
and the ceiling of 64 has been run.

---

## Who can add a server (added 2026-10-11)

Until 2026-10-11 a Lane B server existed only as a block somebody typed into
the daemon's config file. It can now also be added from the terminal client
(`/mcp-server add`) or the command line (`mochiii-daemon mcp add`), and is kept
in `~/.mochiii/mcp-servers.json`. That is a new way to arrive at "an unconfined
program runs as the user", so who can reach it is stated here.

| Who | Can they add a server? | Why |
|---|---|---|
| The user, in the TUI or CLI | yes, after typing the server's name | The first request stores nothing and answers with what the server could do; only an acknowledged request adds it. Position 1 above — "typed by hand" — still holds, at the moment it applies. |
| The user, in VS Code's MCP Servers view or marketplace (2026-10-13) | yes, after typing the server's name | The same two steps (`storeServer`, `clients/vscode/src/mcpInstall.ts`): the daemon's own warning is shown as it came, in a modal, then the name is typed. A marketplace puts every listed server one click away, which is why the click is not the confirmation. |
| The user, in the chat's Customize page (2026-10-13) | yes, after typing the server's name | The page mounts the same marketplace (`media/marketplace.js`) over the same host logic (`MarketplaceCore`, `clients/vscode/src/mcpMarketplace.ts`), behind the same one-time notice, and Add ends in the same `storeServer`. The page's other requests name entries in host-side tables (`clients/vscode/src/chatPages/rules.ts`); a Pause, Change or Remove names a server the daemon last listed, and runs the tree view's own command. |
| The marketplace's catalog (mochiii.in) | **no** | It is data the extension checks (`mcpCatalog.ts`): an entry runs one pinned package through `npx`, `uvx` or `docker` and names variables without values, or it is dropped. It can only pre-fill the request above; the webview that shows it holds no catalog and posts only a server's name. |
| A web page, through a `vscode://` link | **no** | The link carries a server's name and nothing else (`handleMarketplaceUri`); the marketplace looks the name up in the catalog it checked itself and shows that card. It never installs from a link: the user still presses Add, reads the warning and types the name. |
| A repository the user opens | **no** | The file is per-user, beside the stored key, never in the workspace. A relative command is refused, so a stored line cannot come to mean a program inside whichever repository the daemon was started in. |
| The model, through `propose_edit` or an outside read | **no** | `.mochiii` is a protected directory for edits (`editapply` `ProtectedDirNames`) and refused to outside reads (`daemon/outsideread.go`). |
| The model, through `sandbox_exec` | **no** | A sandboxed command cannot write outside its working copy, and cannot create a Unix socket to reach the daemon (`daemon/mcp/sandbox_landlock_linux.go`). Where a platform has no such sandbox, `sandbox_exec` is what is unsafe, and it says so itself. |
| Another user on the machine | **no** | The socket is same-UID; the file is 0600 in a 0700 directory, and one found writable by group or other is not loaded at all. |
| Another process running as the user, or a third-party server already added | yes | Same-UID code can already run any program. This is the "hostile client" case under *What is still true and unfixed*: outside the model, and unchanged. |

What an added server gets is what a configured one gets and no more: policy
`ask` on every tool, the same environment allow-list (names only — a
`NAME=value` is refused, and refused without repeating the value), and the same
launch path, so every row of the matrix above applies to it unchanged.

**New and worth knowing:** an add starts the server once, immediately, to ask
for its tools. That is the user's own program run at the user's request, after
the acknowledgement — but it does mean "added" and "has run" are now the same
moment, where a config-file server does not run until the first agent turn.
A list (`/mcp-server`) starts every configured server for the same reason, as
`mcp list` always has.

---

## Reproducing any row

```sh
# The hostile-peer matrix, hermetic, no network:
go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected'
go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying'

# The amplification sweep behind M1a's 12.0x:
MCP_FLOOD_MIB=64 go test ./daemon/mcp/ -run TestAFloodedToolList -v

# Interop (needs network, not in CI):
MCP_INTEROP=1 go test ./daemon/mcp/ -run TestInterop -v

# The tool-menu size curve (real billed calls):
go test -tags eval -run TestToolMenuSizeCurve -v -timeout 60m ./daemon

# 50 turns with a Lane B server, watching for leaks:
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo scripts/agent-cost-bench.sh
```

Every mode of the misbehaving server is one `argv[1]` value; see
`daemon/mcp/testdata/badserver/main.go` for the list and what each is for.
