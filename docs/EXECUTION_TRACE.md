# The execution trace

A daemon built with `-tags trace` and started with `MOCHIII_TRACE=1` writes one
line per stage a request reaches: which stage, how long, how it ended. It exists
so a person can see where a turn stopped (for example in the `mochiii_visual`
viewer). It cannot record what was said: the API has nowhere to put text.

A default build does not contain it. See the package comment of
[`daemon/trace`](../daemon/trace/enums.go) for the design, and
[`daemon/trace/trace-schema.json`](../daemon/trace/trace-schema.json) for the
format, which this repository owns.

## Running it

```sh
cd daemon && go build -tags trace -o mochiii-daemon-traced .
MOCHIII_TRACE=1 ./mochiii-daemon-traced --workspace "$PWD/.."   # plus the flags you normally use
```

The trace goes to `<socket dir>/trace/daemon-<pid>.jsonl` (the socket dir is
`protocol.SocketDir()`: `$XDG_RUNTIME_DIR/mochiii`, mode 0700; the trace dir is
0700 inside it; the file is 0600 and is opened without following a symlink). The
daemon logs the path once, when the first line is written. Nothing is created
before that, and nothing at all when the switch is off. Only the value `1`
turns it on.

A daemon that is already running does not start tracing: start the traced one
first. The TUI and the VS Code extension connect to whichever daemon is serving
the workspace, so run the traced daemon on the workspace and then open the
client on it.

## Joining a client's trace to the daemon's

A client that traces itself names each turn with eight lower-case hex digits and
sends them in the one optional field `trace` of `protocol.PromptRequest`. A
traced daemon (built with `-tags trace`, `MOCHIII_TRACE=1`) uses that id as the
`turn` of every line it writes for the request, so the two files join on it. The
field is absent from the wire unless a traced client sets it, so a request is
byte-for-byte what it always was without one, and it needed no
`ProtocolVersion` bump: an older daemon skips a key it does not know.

The id is untrusted input. The daemon uses it only for a prompt, only at the
exact key `trace`, and only when it is exactly eight characters of `0-9a-f`
(`protocol.ValidTraceID`); any other value, of any type, is ignored: the daemon
mints its own id, the request is answered as usual, and the bad value is not
logged, echoed or written. A default daemon, and a traced one with the switch
off, never look at it. `TestAnInvalidJoinIDIsReplacedAndIsNowhereInTheTraceTheLogOrTheAnswer`
and `TestTheJoinIDIsIgnoredWithNoTracingAndSaysNothing` are the checks.

Two requests that name the same id write lines under the same `turn`, each
numbering its own `n` from 1; the id says which turn a line belongs to, not that
it is unique. A client mints a fresh one per prompt.

## The terminal client's half

The terminal client (`clients/tui`) writes its own file, `tui-<pid>.jsonl`, in the
same directory, in the same format with `"src":"tui"`. One writer per file: the
viewer merges the two by `turn` and time. It is built the same two ways: the
recorder (`clients/tui/tuitrace`) exists only in a build made with `-tags trace`,
and still writes nothing unless `MOCHIII_TRACE=1` is set in the client's own
environment. A default client has no trace code and sends the same request bytes
as ever (`TestADefaultBuildHasNoTraceCode`; the traced client with the switch off
sends no `trace` key either).

The client mints an id for each turn (every Enter, whether it is a command or a
question) and sends it as `trace` on the prompt request, so the daemon's lines for
that turn carry the same `turn`. A command that never reaches the daemon has a turn
of its own with only the client's lines.

| Point | Where | Lines |
|---|---|---|
| T1 | `startTurn`, once per Enter | `command_parsed` (ok), subject = the catalog command the line names, or `unknown`; only for a line that starts with `/`; never its arguments |
| T2 | `handleSlash` and the other local branches | `command_local_done` (ok): the handler for a command that stays local returned. A request a command starts for later is not part of it |
| T3 | `streamPromptWith` | `dial` start, then ok or fail (class `no_lockfile`, `unreachable`, `handshake_rejected` or `other`, the ways `connectToDaemon` fails); a failed dial ends the turn's trace. `request_sent` start when the request goes, then ok when the daemon first answers (its duration is how long that took), or fail (`user_cancelled` when the person stopped it, else `other`) |
| T4 | `askForApprovalTraced`, the Done and error branches | `approval_shown` (ok), subject = the server's and the tool's name the daemon sent, as `server__tool`, else `unknown_tool` (the call number is the daemon's and the client does not have it, so it is 0); `result_rendered` (ok, or fail with the Done's `ErrorClass`, else `other`) when the result is handed to the screen. A turn that is stopped after the daemon answered ends with `result_rendered` fail `user_cancelled` |

**A request that never comes back** is `request_sent` start with nothing after it
(until the turn is stopped, which closes it). A daemon that took the request and
whose model never answered shows as the daemon's `model_call` start with no end,
and the client's `request_sent` ok with no `result_rendered`.

What is not traced: keystrokes, renders, `/connect`'s key entry, a command's
arguments, and anything from `View()`. Every line is written from the update
goroutine (once per Enter) or the stream goroutine (a few per turn).

**Why a second copy of the vocabulary.** `clients/tui` is its own module and the
daemon module requires it (the one binary is both), so the client cannot import
`daemon/trace`. The shared pieces are `protocol.ValidTraceID` (the id), the schema
file (every name), and parity tests: the client's tables against the schema in its
own module, and the daemon's tables against the client's in the daemon module
(`TestTheDaemonsStagesAndTheClientsAreTheSchemasStagesExactly`,
`TestTheClientsErrorClassesAndTheDaemonsAreTheSchemasClosedList`,
`TestTheClientsToolNameCheckAgreesWithTheDaemons`).

### Running the client traced

Start the traced daemon first (a client does not trace a daemon that is already
running, and a daemon that is already running does not trace), then the traced
client, with the switch on in both:

```sh
cd daemon && go build -tags trace -o mochiii-daemon-traced .
MOCHIII_TRACE=1 ./mochiii-daemon-traced daemon --workspace "$PWD/.."    # the daemon; plus the flags you normally use
# in another terminal. The client is the same binary under another name (only the
# name "mochiii-daemon" makes it the daemon), and a traced build is all it needs:
cp mochiii-daemon-traced /tmp/mochiii-traced
MOCHIII_TRACE=1 /tmp/mochiii-traced --workspace "$PWD/.."
```

Both write into `<socket dir>/trace/`. The client's file is created at its first
line and never when the switch is off.

## What each emit point records

| Point | Where | Lines |
|---|---|---|
| D1 | `serveConn`, after the body is decoded | `request` (ok), subject = the request kind (from the discriminator keys); the turn id is minted here; the first line also carries `sid`, the first 8 hex digits of the SHA-256 of the client's session name |
| D2 | the seven prompt refusals | `request_refused` (fail), class = the `ErrorClass` already on that Done |
| D3 | before and after retrieval | `retrieval` start, then ok or skip |
| D4 | each model call: the loop, the single-turn path, the wrap-up call | `model_call` start and ok/fail (class from `ModelErrorClass`, or `user_cancelled`); `first_token` once per call, only when text streams |
| D5 | `dispatchToolCall` / `resolveExecutable` | `tool_requested` (ok), subject = the name the registry resolved, or `unknown_tool`; `call` is the daemon's counter, not the model's id |
| D6 | the approval ask and its settlement | `approval_wait` start and ok (its duration is how long the person was asked); `approval_result` ok when a person approved, fail with `denied_user` / `denied_ask` / `denied_config` / `denied_credential` / `refused_by_tool` when the call was not allowed. A call the configuration allowed without asking writes neither |
| D7 | around `registry.Call` | `tool_run` for a built-in, `mcp_call` for a server's tool: start, then ok, or fail with `tool_failed` / `server_unavailable` |
| D8 | `budgetStop` | `budget_stop` (fail), class = the Incomplete reason (`agent_budget`, ...) |
| D9 | the persist and the Done | `persist` start and ok; `result` ok, or fail with the Done's `ErrorClass` or its Incomplete reason; its duration is the whole turn |

## The rules, and what enforces them

- Tool names are recorded only after the registry resolved them; anything else
  is `unknown_tool`. Names the model typed are never recorded.
- Call ids are a per-turn counter the daemon keeps. Ids the model sends are never recorded.
- Error classes are members of the daemon's existing closed sets, or `other`.
  `TestEveryClosedSetMemberIsATraceClass` fails when a set grows and the trace does not.
- Lines are at most 512 bytes and are built from a fixed template.
- `ToolActivity.Detail` and `decision.reason`, which can echo what the model typed, are never passed to the trace.

`daemon/trace_leak_test.go` runs a whole agent task through the real traced
daemon with a dummy secret planted in every place a value can enter, as a
recognised key shape and as a plain string, and reads every byte of the trace
directory for each in raw, base64, hex and URL-encoded form. It also checks that
every line has only schema keys and only enum or id values.

## Tests

`make trace-test` (CI runs it; `make vet` compiles the tag). The symbol test,
`TestADefaultBuildHasNoTraceEmitter`, builds the daemon both ways and fails if a
default build has any trace symbol or the `MOCHIII_TRACE` string.
