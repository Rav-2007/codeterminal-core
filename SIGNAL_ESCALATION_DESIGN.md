# Design: additional signals for reasoning-tier escalation (Phase 1)

> **Status: Phase 2 SHIPPED. This is the design record, not work remaining.**
> *(Corrected 2026-08-08.)*
>
> This line used to read *"No client/protocol wiring yet (that's Phase 2, gated
> on review of this doc)."* The wiring exists: `PromptKind` is a field on
> `PromptRequest` in [`protocol/protocol.go`](protocol/protocol.go), and
> `reasoningPromptKinds` in [`daemon/router.go`](daemon/router.go) consumes it,
> admitting exactly `PromptKindReason` and `PromptKindRefactor`.
>
> One thing below is still true and is the reason the feature is invisible:
> **the `reasoning` tier ships `active: false` in `models.json`**, so escalation
> resolves back to the default tier. The plumbing is done; the tier is off.

## 1. Candidate signal sources

The brief asked for 2-4 structural signals, sourced from things a client or
the daemon already computes, never from reading/guessing prompt intent. I
looked at the actual call sites before proposing anything, because two of
the obvious candidates turn out to have real plumbing blockers today.

### A. Explicit user intent (`PromptKind`) — recommended, buildable now

A slash command or keybind (`/reason`, `/refactor`) sets
`RouteInput.PromptKind` directly. This is the strongest signal because the
user stated it — there's no inference at all. It's also the only one of the
three that requires zero reordering of existing daemon logic: `PromptKind`
is already a field on `RouteInput` (currently unwired), and nothing else
needs to move.

This is what Phase 1 implements.

### B. Retrieval scale (chunks retrieved) — real signal, but not wireable without reordering

`daemon/context.go` already logs `retrieval: chunks=%d ...` after
`gatherContext` runs, so the count is genuinely being computed for other
reasons — that part of the brief is accurate. But `daemon/server.go:175`
calls `s.route()` *before* `s.gatherContext()` is called at line 181. As the
code is laid out today, the chunk count for *this* request does not exist
yet at the moment `Route()` needs to decide.

Using it as a signal would mean either:
- reordering `server.go` so retrieval runs before routing (changes request
  latency shape — retrieval would block routing/logging that currently
  happens first), or
- using the *previous* turn's chunk count as a proxy for the current one
  (weaker, and only applies with conversation history).

Neither is a `router.go`-only change, so this is out of scope for Phase 1.
Flagging it as a legitimate Phase 2+ candidate, not a rejected one.

### C. Structural scope (files touched) — real signal, but temporally backwards for same-turn routing

`editapply.ParseEditBlocks` is only ever called on the model's *response*
(`daemon/server.go:557`, and `daemon/apply_cmd.go:67`) — after
`streamCompletion` has already finished. So "number of files touched by a
proposed edit" cannot inform the routing decision for the turn that
produces it; the count doesn't exist until the turn is over. It could only
inform a *subsequent* prompt in the same session ("last turn touched 4
files, so treat this follow-up as complex too") — a carry-forward signal,
not a same-turn one.

Separately, "number of files in the current multi-file selection" (the
other half of the brief's proposal) assumes client-side multi-file-selection
state. I checked both clients: neither the TUI (`clients/tui/chat.go`) nor
the VS Code extension track a selection concept at all today — both only
know a single `workspace` root. That variant needs new client UI/state
before it's a real signal, not just new plumbing.

So (C) is real but weaker and costlier than the brief implies. Lowest
priority of the three; not implemented in Phase 1.

### Explicitly not proposed: prompt-text/length heuristics

Per the brief, not proposed as a primary signal. Flagging per instruction:
if (A)-(C) prove insufficient later, keyword/length heuristics on the
prompt text are a weak, secondary fallback only — same reasoning as why
exit-code escalation was chosen over guessing in the first place: an
explicit signal beats a sniffed one, and a false-positive escalation is a
cost-and-latency mistake this project has otherwise been careful to avoid
making silently.

## 2. `RouteInput` / `Route()` change

`RouteInput.PromptKind` already exists; this patch gives it a job. No new
fields needed for Phase 1 (B and C above aren't being wired yet).

Two closed, exact-match values recognized: `"reason"` and `"refactor"`
(matching the `/reason` and `/refactor` commands from the brief). Any other
value — including empty string, which is what every request sends today —
is inert. This is the same fail-closed shape as the exit-code branch: an
unrecognized or missing signal must never escalate.

`Route()` keeps the exit-code branch's exact position (checked first,
unchanged condition, unchanged fallback path) and adds the `PromptKind`
check as a second, independent condition feeding the *same* escalation
target (there is still only one escalation tier, `tierReasoning`). See
`daemon/router.go` diff.

## 3. Fail-closed / additive

- Empty or unrecognized `PromptKind` → `Route()` behaves byte-identically
  to today (falls through to `defaultDecision`).
- `PromptKind` set to `"reason"`/`"refactor"` but the reasoning tier is
  inactive or missing → falls back to the default tier, exactly like the
  exit-code branch's own fallback (this reuses the same
  `cfg.Tiers[tierReasoning]` active+slug check, not a second copy of the
  logic with different rules).
- The exit-code branch's own condition, escalation, and fallback are
  untouched in shape and behavior.

## 4. Interaction with the existing exit-code signal (required answer)

**Can both fire on the same request?** Yes — nothing prevents a request
from having both `HasExitSignal && LastExitCode != 0` and
`PromptKind == "reason"` at once (e.g., a user re-runs a failed command
*and* explicitly asks for `/reason` on the retry).

**Is this the same class of bug as the header collision?** No, and it's
worth being precise about why not, rather than pattern-matching on
surface similarity. The header bug dropped *user-visible information*
(two independent true facts, one silently not shown) because there were
two independent display slots sharing one hardcoded-height string. Here,
there is only ever **one** escalation target (`tierReasoning`) — both
signals, alone or combined, resolve to the exact same `Tier`/`Slug`. There
is no second "slot" for a second tier to occupy, so the *decision* can
never be wrong or lossy the way the header was.

What *can* still happen, on a smaller scale: if `Route()` used a simple
"check exit-code, then check PromptKind" sequential-return shape, and the
exit-code branch returns first, the `Reason` string logged would say
`"non-zero exit escalation"` and silently omit that `PromptKind` was
*also* true — an incomplete log line, not a wrong decision. Applying the
same "don't silently drop a true cause" principle from the header fix
(scaled to what's actually at stake here — a diagnostic string, not a UI
element), `Route()` collects every true escalation cause and joins them
into one `Reason` string, e.g. `"non-zero exit escalation; explicit prompt
kind \"reason\""`, rather than reporting only the first-checked one. This
is a same-shape, additive change (no new branches, no changed conditions,
no changed fallback rules) — only the `Reason` string composition is
shared between the two causes.

## 5. What's NOT in this patch

- No protocol change (`RouteInput` is daemon-internal; nothing crosses the
  wire yet).
- No client wiring — no slash command, no keybind, in either TUI or VS
  Code.
- No change to `s.route()` in `server.go` (still always sends
  `PromptKind: ""`, so production behavior is unaffected until Phase 2).
