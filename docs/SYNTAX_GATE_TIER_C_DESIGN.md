# Tier C — LSP diagnostics in the syntax gate

**Status: DESIGN. No code. Written 2026-08-30 alongside Tier B.**

This is a design and not a patch because two things about it are larger than they look, and
both were found by reading the code rather than by estimating. They are stated first, because
if either is wrong the rest of this document is wrong with it.

---

## The gate as it stands

`editapply.checkSyntax` routes on `syntaxTierFor`:

| tier | what it is | what a failure may do |
|---|---|---|
| **A** — `tierParser` | `go/parser`, in-process | **refuse** — the only tier allowed to |
| **B** — `tierDelimiters` | bracket balance with skip states | note only, enforced by signature |
| — `tierNone` | nothing | say plainly that nothing checked it |

Tier A is right and narrow: Go only. Tier B covers TypeScript, JavaScript and Python and is
deliberately advisory, because a bracket count that refuses would block correct edits on every
construct its skip states fail to model.

Tier C would put a real compiler behind the other three languages. `LSPBridge` already runs
`gopls`, `typescript-language-server` and `pyright-langserver` ([daemon/lsp_bridge.go:156](../daemon/lsp_bridge.go#L156)),
which is exactly the set Tier B currently serves with brackets.

---

## Blocker 1 — diagnostics are asynchronous, and their absence proves nothing

`LSPBridge` has `Call` (request/response, correlated by id) and `Notify`. Its `readLoop`
dispatches by id, and drops anything without one:

```go
idRaw, ok := r["id"]
if !ok {
    continue // a notification from the server; nothing is waiting
}
```
— [daemon/lsp_bridge.go:460](../daemon/lsp_bridge.go#L460)

`textDocument/publishDiagnostics` is server-initiated and carries **no id**. Every diagnostic
this daemon has ever received has hit that `continue`. So Tier C needs a subscription
mechanism the bridge does not have: a registry keyed by document URI, populated from the
notification path, with the readLoop routing to it.

That is ordinary work. **This is not:**

> After `didOpen`/`didChange`, a language server publishes diagnostics *when it is ready*.
> There is no "no errors" message. A clean file and a server that has not finished analysing
> produce **byte-identical silence**.

So any Tier C gate is really a timeout, and the whole design turns on what a timeout is
allowed to mean:

- **Treat a timeout as "clean" and refuse on diagnostics** → the gate lets broken edits through
  whenever the machine is loaded, and does so silently. It also makes the gate's behaviour a
  function of CPU contention, which means it is not reproducible and a user cannot be told why
  it did or did not fire.
- **Treat a timeout as "unknown" and never refuse on it** → honest, and it makes Tier C an
  advisory tier like Tier B, just a far more accurate one.
- **Block until diagnostics arrive** → an unbounded wait on a subprocess in the apply path. No.

**The recommendation is the middle one, and it demotes the whole feature's ambition:** Tier C
should be *advisory-by-default*, with a refusal reserved for the case where diagnostics
**arrived** and contained a **parse/syntax-severity** error. That case is genuinely
distinguishable from silence, and it is the only one that is.

This is the same shape as the fault the delta rule was built to remove from Tier A: a gate that
made a confident claim about a file when the truthful answer was "I don't know". A Tier C that
reads a timeout as approval would reintroduce it in the other direction — a confident *pass*.

---

## Blocker 2 — the dependency points the wrong way

`checkSyntax` lives in `editapply`. `editapply` does not import `daemon`, and must not: the
package is the safety core, and the daemon is one of several things that call it (the CLI and
the TUI call `PrepareEdit` in their own processes — see `clients/tui/chat.go`).

`LSPBridge` lives in `daemon` and its accessor hangs off `*Server`
([daemon/lsp_bridge.go:178](../daemon/lsp_bridge.go#L178)).

So Tier C cannot be implemented the way Tiers A and B were — by adding a case to a function
inside `editapply`. It requires one of:

1. **Inject a checker.** `editapply` declares an interface (`SyntaxOracle`, say: given a
   language, a path and content, return diagnostics or "unknown"), and the daemon supplies an
   LSP-backed implementation. `PrepareEdit` gains an optional oracle; nil keeps today's
   behaviour exactly. Layering stays intact and `editapply` stays testable with a fake.
2. **Lift the gate.** Move the syntax decision out of `PrepareEdit` and into each caller. This
   is worse and the repo already records why: the create path and the edit path had *separate*
   syntax checks, which meant the same bytes were refused as an edit and written as a create —
   a model whose edit was refused could land them with an empty `SEARCH` instead. That bug was
   fixed by putting the gate in ONE function. Three callers each holding their own oracle is
   the same shape of drift waiting to happen.

**Recommendation: option 1.** It is the only one that keeps "one gate, one behaviour".

Note the consequence, and it is a real cost: the CLI and the TUI would get Tier C only if they
also grew an LSP bridge, so for the first time the three surfaces would genuinely differ in
what the gate can do. Today's asymmetry is about what reaches the *wire* and was just closed;
this would be an asymmetry in the *decision*. That needs to be said out loud in the note —
`SyntaxNote` would have to distinguish "no diagnostics available on this surface" from
"diagnostics were clean", or it becomes another confident silence.

---

## What Tier C would be worth

Tier B catches unbalanced brackets. It cannot see an undefined variable, a type error, a bad
import, or a call with the wrong arity — the errors a model actually makes when editing
TypeScript. Tier C sees all of them, on the three languages where this product currently has
no parser at all.

That is a large gain, and it is why this is written down rather than dropped.

## What it would cost

- **Latency in the apply path.** `didChange` + wait-for-diagnostics on every prepared edit,
  against a subprocess that may be cold. The apply path is currently pure local computation.
- **A server per language per workspace**, already true for the AST features, but Tier C makes
  it true on the *edit* path, which is far hotter.
- **A new failure mode for edits**: "the language server is not installed" must never become a
  refusal, which means a whole second axis of degradation to get right — and
  `lspServerForFile` already produces a good message for it.

---

## Sequencing

Not next. The honest order is:

1. **Bridge diagnostics as an observable first**, with no gate attached — subscribe, log what
   arrives and how long it took, and find out empirically what the timeout distribution
   actually looks like on a warm and a cold server. The timeout policy above is the whole
   design, and it should be chosen from measurement, not from a guess in this document.
2. **Then** decide whether the advisory tier is worth the latency.
3. **Only then**, if diagnostics prove fast and reliable, consider a refusal for the narrow
   arrived-and-syntactic case.

Step 1 is small, has no effect on any user-visible behaviour, and answers the only question
that matters. Everything after it is contingent on what it finds.
