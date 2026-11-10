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
