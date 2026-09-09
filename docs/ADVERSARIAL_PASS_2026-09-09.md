# Adversarial pass — daemon, editapply, helper, proxy — 2026-09-09

<!-- coderefs: enforced -->

Branch `audit/adversarial-pass`. Scope: the ~55 commits the previous TUI-focused
pass did not audit, concentrated in `daemon/`.

Every number below is labelled **DETERMINISTIC** (counts, ratios, byte sizes,
constants) or **WALL-CLOCK** (one machine, one run, varies). Provenance for
every wall-clock number: this Linux dev box, go1.25.13, one run. This project
has twice presented a wall-clock artefact as a measurement; the split is what
stops a third.

---

## C.1 — Findings

Three states, never collapsed. **FIXED** means a change landed and a test fails
without it. **OPEN BY DECISION** means someone decided, with reasoning and a
trigger recorded — a closed row. **PENDING A DECISION** means nobody has
decided, and it is the only kind that blocks.

### FIXED — eight, each with the neuter that proves its test is load-bearing

| # | Issue | Failure mode | Fix | Test | Neuter |
|---|---|---|---|---|---|
| 1 | `RejectUnprintablePath` refused C0+DEL, not C1 | U+009B (CSI) reaches the approval terminal | `unicode.IsControl` — `193fb95` | 65-char class sweep + count floor | revert → 33 C1 chars pass |
| 2 | Two discriminator keys dispatched by declaration order | `{"edit":…,"undo":true}` runs the edit, discards the undo | `countDiscriminators` — `026fe48` | 7 rows asserting the file on disk is unchanged | remove the count → ambiguous bodies dispatch |
| 3 | `TOO_LARGE` marker read uncapped | 256 MB marker → 512 MiB heap, 268 MB wire message | 4096 cap + fstat + `O_NONBLOCK` — `5f49b91` | 7 tests incl. FIFO, symlink, oversize | remove cap → both blow out |
| 4 | `serveConn` used `context.Background()` | a disconnected client's work runs to completion | `cancelConn` through 5 call sites — `dc8192f` | `cancellation_test.go` | revert → cancellation never propagates |
| 5 | Lexical query cost quadratic, unbounded | a 1 MB prompt occupies the daemon 89 s | `maxLexicalQueryChars = 32768` at the shared chokepoint — `d817bd7` | `lexicalbound_test.go`, 7 tests | remove bound → 1 M chars = 1 m 28.894 s |
| 6 | `helper/handleConn` had no `recover` | a tokenizer panic kills the embedder process | `recover` + stack — `2a1c948` | second connection served after a panic | remove → **the test binary itself dies, no FAIL line** |
| 7 | `--debug-context` logged raw chunk content | a retrieved key `scrub` would catch, written to a durable log | scrub through the same predicate — `36ff71f` | `TestSentinel_RetrievedChunk…` | revert → sentinel reappears |
| 8 | Unparseable tool name recorded unbounded | 1,048,596-byte name → 1,048,851-byte audit line | `truncateForClient(…, maxReportedToolName)` — `36ff71f` | `TestSentinel_ToolAuditBounds…` | remove bound → unbounded again |

### OPEN BY DECISION — reasoning and trigger recorded

R1.16 (gate-count class, **five** instances), R1.17 (F3's inherited message),
R1.18 (silent U+FFFD embedding), R1.19 (the tripwires), R1.20 (unbounded refusal
`Detail`), R1.21 (the warn-mode oracle), R1.23 (five uncovered goroutine
layers), R1.24 (unbounded LSP header read), R1.25 (LSP kills process not group).

**Weak triggers, flagged as weak rather than dressed up:** R1.20's waits on
someone noticing an oversized refusal message; R1.23's middle trigger waits on
an unexplained daemon exit being reported with enough detail to reach the row;
R1.25's waits on someone correlating a stray `gopls` with a daemon that exited
an hour earlier. Three flagged weak beats fourteen that all look equally solid.

### PENDING A DECISION — one row, and it is the one that matters

**R1.22 — the scrub protects turn N; turn N+1 sends the same bytes raw.**
Opened **2026-09-04** as item 1 of `docs/DECISION_MEMO_2026-09-04.md`.
Blocked on **nobody having been told**, not on anyone's inattention.

### Negative results — equal weight

A boundary tested and found sound is a result, and a report listing only
findings reads as though everything examined was broken.

- **Frame handling held under every constructed attack**: oversized, truncated,
  duplicate-key, ambiguous-discriminator, traversal, symlink, overlong UTF-8,
  surrogates, C1.
- **The helper boundary is clean by construction** — `encoding/json` substitutes
  U+FFFD on **decode as well as encode**, so the helper defends itself and a
  hostile same-uid client cannot deliver the panicking bytes either.
- **No secret in 638 commits**: 33 pattern hits, every one a fixture, a doc
  example, or a pattern definition in `scrub.go`.
- **The audit digest survived all four arms that could have had a fallback** —
  malformed JSON, 1 MiB of arguments, non-JSON, empty. The hypothesis that a
  fallback returns raw arguments is **false**: SHA-256 cannot fail.
- **Boundaries 6 and 7 are among the better-tested surfaces in the daemon** —
  two purpose-built hostile harnesses (`badserver`, 11 modes; `fakelsp`, 12
  modes) and **40 adversarial tests**, all passing.
- **Both boundaries route output through one choke point**, `renderToolResult`
  at `daemon/agentloop.go:838` — truncate, scrub, strip control characters.
- **`prefixWriter` is bounded and escaped**, pinned by two live tests.
- **The LSP demux is sound.** I hypothesised an unbounded `pending` map and a
  replay deadlock; both are wrong — `daemon/lsp_bridge.go:471` deletes inside
  the same lock as the send.
- **Rows 1, 3, 4, 6 of the credential sweep are controls that hold**; rows 8 and
  9 are raw **by design** and correctly so — a redacting backup would corrupt
  undo.
- **The existing suite is load-bearing on `scrub`**: neutering it fails **five
  pre-existing tests**, not only the new ones.

---

## C.2 — Measurements

### The F1 quadratic table

Corpus-independent: 50 / 500 / 5000 turns → 726 / 672 / 731 ms at 100 k
(WALL-CLOCK).

| query chars | FIXED | NEUTERED | label |
|---|---|---|---|
| 10,000 | 20 ms / 15 ms | 14 ms | WALL-CLOCK — below the bound, unchanged by design |
| 100,000 | **0 s, refused** | 690 ms | WALL-CLOCK |
| 400,000 | **0 s, refused** | 15.535 s | WALL-CLOCK |
| 1,000,000 | **0 s, refused** | 1 m 28.894 s | WALL-CLOCK |
| `gatherContext`, 400 k prompt | 0.12 s | 14.05 s | WALL-CLOCK |

Growth **×4.05 per doubling**, k ≈ 6.1e-8 ms/char² — DETERMINISTIC ratios
derived from WALL-CLOCK points.

### Everything else

| Quantity | Value | Label |
|---|---|---|
| Control characters in the swept class | **65** | DETERMINISTIC |
| Indicator search space | **32 bits** | DETERMINISTIC |
| Oracle: accepted / rejected candidates | **1 / 39** | DETERMINISTIC |
| Tool name in → audit line out | 1,048,596 B → 1,048,851 B; bounded **131 B** | DETERMINISTIC |
| Proxy log volume, info vs debug | 441 B → **523 B** | DETERMINISTIC |
| Tests failing when `scrub` is neutered | **7** (5 pre-existing) | DETERMINISTIC |
| Commits scanned / hits / live secrets | **638 / 33 / 0** | DETERMINISTIC |
| helper tokenizer, 10 hostile inputs | 6 clean, **0 errored, 4 panicked** (2 signatures) | DETERMINISTIC |
| Adversarial tests already covering boundaries 6+7 | **40**, all passing | DETERMINISTIC |
| Hostile-harness modes (badserver / fakelsp) | **11 / 12** | DETERMINISTIC |
| `recover()` calls in `go-sdk@v1.7.0/mcp/` | **0** | DETERMINISTIC |
| Uncovered goroutine layers on untrusted output | **5** | DETERMINISTIC |
| Register references resolving under the gate | **51**, across 4 enforced docs | DETERMINISTIC |
| Coverage: daemon | 78.6 % → **78.8 %** (floor 78.0) | DETERMINISTIC — **measured once; NOT a floor** |
| `TOO_LARGE` 256 MB marker | 512.0 MiB TotalAlloc, 268,435,598 B wire, 1.20 s | **runtime measurement with a 64× margin — NOT deterministic**, despite reading as a count |
| FTS5 cancellation | CTE 511 ms; **FTS5 51.32 s**; client gone at 59 ms, handler worked 11.27 s | WALL-CLOCK |

---

## C.3 — Register

Ten rows touched: R1.16–R1.25. New rows carry all five fields. R1.20, R1.24 and
R1.25 state plainly that **nothing pins them**; R1.23 states that nothing *can*
pin the SDK layers without an input that panics them, which is its own open
question.

---

## C.4 — What I did not verify

It has grown every time it was written: 8 → 10 → 12 → 16 → **19**. That is the
right direction.

1. **Whether any input panics the SDK.** Needs fuzzing a third-party parser.
   **This is the unknown that decides how urgent the missing recovers are**, and
   nothing in this pass can settle it.
2. **`typescript-language-server` and `pyright-langserver` are not installed
   here** — two of three LSP languages were untestable.
3. **Shutdown mid-call correctness.** `shutdown()` closes `done` and
   `CallContext` selects on it, so it *reads* correct. Per the F1 lesson,
   visibly correct plumbing is not evidence that it has the property.
4. **Whether `messageLimitReader` binds before the SDK allocates, or only
   after.** The cap is at `daemon/mcp/stdioclient.go:218`; where the SDK's
   decoder allocates relative to it is unread.
5. Whether the unbounded LSP header read (R1.24) is reachable in practice.
6. Whether gopls orphans children on `Process.Kill()` (R1.25).
7. `daemon/mcpruntime.go:206` and the os/exec copy goroutines — found, reported,
   **not fixed**.
8. Boundaries 6 and 7 have no adversarial input **that I** constructed; 40 tests
   by others exist.
9. R1.20 unfixed, no test.
10. R1.22 unfixed — awaiting a reply nobody has been asked for.
11. R1.18 unfixed — needs eval measurement.
12. R1.16's fuzz-gate instance has a known blind spot and was used to audit
    anyway.
13. Coverage floors **not raised**: helper 61.5 % is environment-dependent (the
    tests load the real cached model and skip without it; CI would measure
    ~22 %), and daemon 78.8 % was measured once where the ratchet requires three.
14. **CI unverified** — no `gh` auth, and `gh` resolves to the wrong repo here.
15. Row 3 of the credential sweep is not driven end to end; nothing selects
    `protocol.TransportTCP`.
16. The three `MACOS_*`-blocked items, if macOS ever rejoins.
17. **A single embedder text between 32 KiB and the 16 MiB wire cap.** The
    `huge` arm was reduced from 2 MB to `maxLexicalQueryChars` to fit inside the
    race gate; a non-daemon client can still send more, and that range is now
    untested.
18. **Whether `maxSequenceLength` (512) bounds tokenizer WORK.** It does not —
    `tokenize` truncates the OUTPUT after `EncodeSingle` has walked the whole
    input. The consequence of that on a large text is unmeasured.
19. **The signed macOS release path**, which `release-signing-guard` states it
    does not cover and does not claim to: unsigned path 4 and the signed path
    both need a macOS runner and real Apple credentials.

---

## C.5 — What I would not trust in my own work

### The corrections this pass produced, in order of how badly they read

**1. "No adversarial input has ever been constructed for boundaries 6 and 7."**
False, and it stood in a delivered report until re-reading the code. There are
two purpose-built hostile harnesses and 40 passing adversarial tests. I wrote it
in C.4 — the section whose whole purpose is to be honest about gaps — and it was
the least honest line in the document.

**2. The process-asymmetry table was wrong in the SAFE-SOUNDING direction.** It
said Lane B calls were "covered transitively by `handleConn` — same goroutine".
The SDK decodes hostile stdout on its own goroutines. Wrong from "not covered"
to "covered" is the direction that lets someone skip work.

**Both of those were claims I endorsed without checking, and this is the part
worth recording: review did not catch them. Re-reading the code did.** They
survived a written report, a summary, and a restatement in a brief.

**3. The `s.pending` deadlock theory, falsified.** I reasoned that the success
path never calls `forget`, so the map grows unboundedly and a replayed id blocks
`readLoop` while holding the mutex. `daemon/lsp_bridge.go:471` deletes inside
the same lock. **I was two lines short of the answer when I formed the theory.**

**4. R1.22 was five days old and I filed it as new** — and characterised it as
needing a design ruling where the existing memo argues it is not a design
question and recommends FIX.

### The largest entry: five defects I shipped, five gates I did not run

`make check` was run **six times** to reach green. Every failure was in code I
had committed, and **not one was reachable by the checks I actually ran** — my
sequence was `go vet ./...`, the package tests, and the coverage ratchet.

| Run | Gate | Position | What it caught |
|---|---|---|---|
| 1 | `gofmt` | 2 of 13 | trailing blank line in `sentinel_secrets_test.go` |
| 2 | `crossvet` | 4 of 13 | `syscall.Mkfifo` undefined on Windows — the daemon package **did not build** there |
| 3 | `race` | 6 of 13 | `helper` timed out at **600.166s**; a 2 MB tokenizer input under race instrumentation |
| 4 | `lint` | 7 of 13 | staticcheck ST1008, error returned first |
| 5 | `errcheck` | 9 of 13 | daemon 93 vs ceiling 92 — a `defer f.Close()` I added in the F4 fix |

**The second is the one to learn from.** The test carried
`if runtime.GOOS == "windows" { t.Skip(...) }`, which reads like adequate
protection and is not: **a runtime skip cannot save a compile-time symbol.** It
shipped with `go vet` and the full package suite green on Linux.

**The third is the one that would have hurt most.** It did not fail — it *hung*,
for ten minutes, and a gate that takes ten minutes to say nothing is one people
stop running.

**None of these are subtle, and that is the point.** They are the ordinary
output of verifying with the tools I reached for instead of the gate set that
exists. The gate set was there the whole time; I ran a subset and called it
verification. That is the same defect as the prior-art one — not looking at what
already exists before acting — in a different costume, which now makes **five
instances in one pass**.

Two smaller notes from the same runs, both worth keeping:

- I first "fixed" the errcheck failure in a **test** file. It changed nothing:
  the gate runs `errcheck -ignoretests`. Believing for a minute that I had
  fixed it is exactly how a vacuous fix gets written up as a real one.
- A bare `staticcheck ./...` also flags ST1005 at `helper/main.go:102`
  ("Intel Mac…"), which `scripts/lint.sh` excludes deliberately and documents.
  Running the default set and fixing what it says would have edited code the
  gate intentionally does not flag — **a gate's own configuration is part of the
  gate.**

### The standing entries

- **The gate-count class**: now five instances. General form — *a vacuity floor
  derived from the target set proves the gate ran, not that the set is right.*
  I added a sixth instance myself: a gate asserting at least one row was still
  undriven, which turns red the moment the work finishes.
- **Two vacuity findings caught only by neutering.** My own F1.b bound silently
  vacated two of my own F1.a tests — they passed in ~171 µs because the bound
  refused before context mattered — three commits after I wrote a warning about
  that exact class. And the first no-recover test used Latin-1 `café`, which
  tokenizes cleanly. Same error, same hour.
- **Neuter A on helper cannot be made load-bearing** without a production change
  (`server.embedder` is a concrete type, not an interface). Labelled in the test.
- **Falsified predictions and what each was worth.** *"Context threading is what
  makes Ctrl-C work"* — false; `sqlite3_interrupt` is not polled inside an FTS5
  phrase match. Worth the most of anything here: implementing without measuring
  would have shipped a commit message claiming a property the code lacks, and it
  would have read as verified. *"The F1 fix will relocate the cost"* — false,
  avoided by placement, not foresight. *"A third vacuous test will appear"* —
  none did, but rows 10 and 11 would have been vacuous had I stopped at the
  scrub neuter.
- **Two figures for one quantity**: "a 16 MiB query costs ~7 hours" versus
  "~4.8 hours". 4.8 h supersedes.
- **A test asserts a limitation persists**, with inverted polarity that invites
  deletion.
- **Prior art, three times.** `proxy/logging_test.go`, the two harnesses, and
  R1.22. Nothing in my method looks for it first.

---

## C.6 — What needs a person, and who

**A RECORDED DEFERRAL WITH A TRIGGER CLOSES A ROW AS WELL AS A FIX DOES.**
"Accepted, revisit when X" is a complete answer. So is "fix it". What does not
close a row is silence. **None of what follows is a work request, and none of it
needs implementation time today.**

| Needs | Who | Why it is not mine |
|---|---|---|
| **R1.22** — the ingest-vs-sink ruling, **and** whether existing `memory.db` files are migrated | **daemon owner — UNIDENTIFIED** | Cross-module design decision with user-visible data consequences. The migration question is deliberately not guessed. |
| Memo items 2–5, incl. the `mcpruntime.go:206` recover | **same, still unidentified** | Item 5's SDK half is a dependency-policy call |
| Raising coverage floors | anyone with CI access | Needs three uncached runs on `main`; helper's number is environment-dependent |
| CI verification | someone with `gh` auth to the right repo | `gh` resolves to the wrong repo here |
| The three `MACOS_*`-blocked items | whoever restores macOS CI | No macOS runner |

### The ranking, stated because it will otherwise be got wrong

**R1.22 outranks everything found in the last two units of work.** Its blast
radius is the only one that **leaves the machine**. It is **confirmed by
measurement**, not predicted. It is **unblocked by everything except a reply**.
The newer findings concern boundaries that turn out to be *better defended* than
the path R1.22 describes — 40 adversarial tests and two hostile harnesses,
against a control that is simply not applied on the second turn.

A fresh finding reads as more urgent than an old one. Here it is not.

### The delivery problem, stated plainly

`docs/DECISION_MEMO_2026-09-04.md` has been committed since 2026-09-04 and
**handed to nobody**. No `daemon/` owner has been named. On 2026-09-08 a later
pass re-derived its item 1 from scratch and filed it as new, because nothing
pointed at the memo.

**These items are blocked on nobody having been told — not on anyone's
inattention.** The five items need about ten minutes and a written reply.
