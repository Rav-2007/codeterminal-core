# Warn-mode fire rate — the measurement the B-vs-C decision was waiting for

**Measured 2026-07-30** on `harden/proxy-spend-and-gates`. Corpus: this repository,
scanned through the production pipeline. Reproduce with:

```
go test -tags warnscan -run TestWarnScanFireRate -count=1 -v ./daemon
WARNSCAN_ROOTS=/path/a:/path/b go test -tags warnscan ... ./daemon   # more corpora
```

## Why this exists

`CHUNK_SCRUB_DESIGN.md` defers the opaque/novel-secret half of P3-FAIL-1 to a founder
choice between **Design B** (entropy redaction) and **Design C** (keyword redaction),
and makes that choice conditional on fire-rate data from warn-mode. Warn-mode has been
wired and durable since `064a00a`.

**It had recorded zero events.** `.mochiii/logs/` on this machine was created
2026-07-27 and contains no `warnmode.jsonl`; no such file exists anywhere on the box.
The sink is not broken — it is correctly constructed at `daemon/main.go:247` and
correctly called at `daemon/context.go:269` — it has simply never been reached, because
it only fills when a daemon serves grounded turns on a real workspace and this machine
has served none. Twelve days of "data is now accumulating" accumulated nothing, and
waiting longer had no mechanism by which to work.

The detectors are pure functions, so the data can be produced directly instead of
waited on. `daemon/warnscan_test.go` does that.

## What was measured, and in what order

The harness reproduces the production path exactly:

| Step | What it is | Why it must be in this order |
|---|---|---|
| `ScanWorkspace` | the real walk | applies the real skip gates — `MatchesSecretName`, `.gitignore`, size cap, binary sniff — so secret-**named** files never reach a detector, exactly as in production |
| `scrub()` | Option A, **live today** inside `renderChunk` | structural signatures are removed before the deferred detectors see anything |
| `detectHighEntropy` / `detectKeywordSecrets` | the deferred detectors | run on **post-scrub** text |

That last row is the point. These detectors exist to answer *"what opaque material
would still leave the machine after Option A has already run"*, so they must see what
Option A leaves behind. Measuring raw file text would inflate every count with secrets
that are already redacted before they reach the wire, and would overstate the case for
B and C. This is the ordering `logChunkScrub` documents at step 2.

## Corpus

```
files scanned = 243        chunks = 2,117
skipped: gitignored=8  noise=8  secret=6  ignored_dir=5
```

Measured at `7a70017`. **The corpus is this repository, so it is self-referential and
it drifts.** Re-running after the Phase-4 commits landed gives 247 files / 2,149
chunks, 1,366 entropy fires and 50 keyword fires — because this document and the tests
written alongside it are themselves indexed, and the `AKIAIOSFODNN7EXAMPLE` fixture in
`chunk_egress_test.go` now counts among Option A's redactions. Absolute counts will
keep moving. **The ratios are what the conclusions rest on, and they are stable:**
618.8 → 635.6 entropy fires per 1k chunks, 33.0% → 33.5% of chunks affected. Nothing
below changes.

Six files were excluded by the **name gate** and never reached a detector — Layer 1
doing its job silently. The noise filter excluded `package-lock.json`, which matters:
scanned raw it contributes hundreds of `sha512-…` integrity hashes at 5.5+ bits/char,
the single largest false-positive source in the tree, and the indexer already
drops it before any of this runs.

**Option A, the live redactor:** 13 of 2,117 chunks (6.1 per 1k) carried a structural
secret and were redacted — `aws_access_key=9  private_key_block=5  github_token=3
slack_token=2  openai_key=1`. Every one is correct. This is the baseline the deferred
detectors must beat.

## Design B — entropy

```
fires = 1,310  (618.8 per 1k chunks)
chunks with >=1 fire = 698 of 2,117  — 33.0% of everything retrieved
by shape: base64=1309  hex=1
by class: test=1051  code=119  doc=116  other=22  config=2
```

**True positives: zero.** Hand-classified against the corpus, the entire firing
population is:

| Class | What it actually is |
|---|---|
| Long identifiers | `buildAugmentedUserMessage`, `abortingResponseWriter`, `correctUsageMaxAttempts` — ordinary camelCase ≥20 chars |
| Hyphenated prose | `arbitrary-code-execution`, `adjacent-but-incomplete`, `case-for-consolidation` — English, in Markdown |
| URL/path fragments | `ai/docs/guides/routing/provider-selection` |
| Module checksums | `go.work.sum` base64 hashes, 4.8–5.1 bits/char |
| Test fixtures | `daemon/chunkscrub_test.go`'s own fake secrets — the scrubber's test data |

The shape label is misleading and worth stating plainly: `base64=1309` does **not**
mean 1,309 base64 blobs. `entropyTokenPattern`'s alphabet is `[A-Za-z0-9+/=_-]`, which
contains `-`, `_` and `/`, so hyphenated English and slash-separated paths classify as
"base64". The detector is, in practice, a long-token finder.

**No threshold rescues it.** From the bits/char distribution over all 5,636 tokens
considered:

| Threshold | Fires | Per 1k chunks | What remains |
|---:|---:|---:|---|
| 4.00 (current) | 1,310 | 618.8 | everything above |
| 4.25 | 513 | 242.3 | identifiers + checksums + fixtures |
| 4.50 | 72 | 34.0 | `go.work.sum` hashes + fixtures |
| 4.75 | 32 | 15.1 | same, fewer |
| 5.00 | 11 | 5.2 | same, fewer |

Raising 4.0 → 4.5 removes 95% of the noise and leaves module checksums. **Precision
stays zero at every threshold**, because the residual population is not
"secrets and near-secrets" — it is hashes. The distribution is unimodal around
3.5 bits/char with no second mode to separate; there is no valley to cut at.

**One thing the threshold does get right, which makes the rest worse.** Hex has a
theoretical ceiling of exactly 4.0 bits/char (log₂16), and real hex strings never
reach it — over 4,000 random samples, a 32-character SHA averaged 3.61 and topped out
at 3.93; even 128 characters averaged 3.91. So the 4.0 threshold already sits *above*
the practical hex ceiling and excludes essentially every git SHA and content hash in
the tree, which is exactly what the observed shape breakdown shows: **1 hex fire out
of 1,310**.

That is the strongest available argument against tuning as the remedy. The threshold
is already filtering out the false-positive class people reach for first, and it still
fires on a third of all chunks — because the dominant false positives are not hashes.
They are ordinary identifiers, hyphenated prose and paths, admitted by the token
*alphabet* rather than by the entropy cut. No threshold reaches them, because they are
not unusually random; they are just long.

**Cost if shipped as a redactor:** something would be redacted from **one chunk in
three**. That does not degrade grounding at the margin; it corrupts it as a matter of
routine, and it would do so to remove nothing.

## Design C — keyword

```
fires = 41  (19.4 per 1k chunks)
chunks with >=1 fire = 33 of 2,117 — 1.6%
by keyword: token=13  tokens=11  password=4  api_key=3  apikey=3  secret=3  client_secret=2  secrets=2
```

Two orders of magnitude cheaper, and the failures are specific rather than diffuse.
All 41 classified:

| Count | Class | Example site |
|---:|---|---|
| 16 | Test fixtures — fake secrets in the scrubber's own tests | `daemon/chunkscrub_test.go`, `daemon/scrub_test.go` |
| 11 | **Semantic collision**: `tokens` meaning rate-limiter or LLM token *counts* | `proxy/ratelimit.go` `tokenBucket{tokens: l.burst}` |
| 9 | **Type annotations**: the captured "value" is the type name | `clients/vscode/src/chatPanel.ts` `onToken: (token: string)` → value = `string`, len 6 |
| 3 | Prose in comments and design docs | `proxy/logging.go` "…with respect to secrets: request and…" |
| 2 | **Env-var indirection** in language idiom | `daemon/main.go` `apiKey := os.Getenv(…)` → value = `os.Getenv` |

**Real credentials: zero.** The 16 fixture hits are arguably correct firings — the
values *are* credential-shaped — but they are test data, not user secrets.

The three non-fixture classes are all fixable, and two of them are gaps in a filter
that already exists. `isNonSecretValue` rejects `${VAR}`, `$(cmd)` and `$VAR`, but not:

- `os.Getenv(…)`, `process.env.X`, `config.Get(…)` — the same indirection written in
  the language rather than the shell;
- type names in an annotation position (`string`, `number`, `Promise`);
- `token`/`tokens` used as a count, where the value is a number or an identifier.

Fixing those three removes 22 of 41 fires (54%) and every non-fixture false positive
except the three prose hits, taking the rate to roughly **9 per 1k chunks**.

## What this corpus cannot tell you

Stated so the recommendation is not read as stronger than it is:

1. **It contains no real secrets.** A git repository with a secret-name gate and a
   `.gitignore` is close to a worst case for finding credentials. So this measures the
   **false-positive rate well and the recall not at all.** Neither detector's ability to
   catch a genuine opaque secret is evidenced here, in either direction.
2. **One corpus, one language mix.** Go-dominant with some TypeScript and Markdown. The
   type-annotation false positive would be heavier in a TS/Java-dominant tree; the
   hyphenated-prose one heavier in a docs-heavy tree.
3. **Fire rate is not leak rate.** These are chunks *eligible* for retrieval; only the
   top-k of them are sent on any given turn.

The FP side is nonetheless the side that decides, because the benefit is bounded: the
detectors run **after** Option A, so anything they could catch is by construction a
secret with no recognizable structure — and no such value appears in this corpus at all.

## Recommendation

**Reject Design B. Do not ship entropy redaction — not at 4.0, not at any threshold.**
It fires on a third of all retrieved chunks, its precision on this corpus is zero at
every cut point, and its population is hashes and identifiers rather than near-misses.
The cost is measured and severe; the benefit is unevidenced.

**Hold Design C, do not ship it yet.** Its rate is tolerable (1.6% of chunks) and its
failures are nameable and fixable rather than inherent. But it caught no real secret
here either, so shipping it now would be paying a known cost for an unmeasured benefit.
If it is to be pursued, the order is: extend `isNonSecretValue` for the three classes
above → re-run this harness → measure against a corpus that actually contains
credentials before any redaction is enabled.

**The honest third option, and the recommended one: neither, for now.** Option A is
doing real work at 6.1 redactions per 1k chunks with no false positives observed. The
highest-value next move for opaque-secret coverage is not a content heuristic at all —
it is extending Option A's structural signature list, which is precise by construction,
as new provider key formats appear.

**Whatever is decided, warn-mode should stay log-only until it has been measured on a
corpus containing real secrets.** This document establishes the cost side only.

## Addendum — the live drill, and the one data point on the other side

Run 2026-07-30 after the corpus scan: a real daemon, real BGE/ONNX embedder, real
index, against a capturing fake provider, serving one grounded turn over the Unix
socket. The workspace held a file with a structural secret (`AKIA…`) and an opaque
36-character token side by side, and the prompt was written to retrieve it.

What went over the wire, read off the captured request body:

| Check | Result |
|---|---|
| Structural secret present in the outbound POST | **absent** |
| Placeholder in its position | `[REDACTED:aws_access_key]` |
| Surrounding code (`func stagingCreds`) preserved | yes — span-scoped, not chunk-scoped |
| Opaque token present in the outbound POST | **yes** — log-only, exactly as designed |
| `warnmode.jsonl` written | **yes**, 0600, one event |
| Raw secret material anywhere in the sink | none — labels and a `sha256:` indicator only |

This is the first time that file has ever been written. The daemon then drained
cleanly on `SIGTERM` (`drain complete, no requests in flight`) and removed its socket.

**And the single recorded fire is a true positive.** The entropy detector fired on
exactly the opaque token — `len=36 bits_per_char=5.00`, `class=code`,
`shape=base64` — and on nothing else in the turn.

That is worth stating precisely, because it cuts against the corpus result without
overturning it. The corpus scan found the detector's *precision* to be zero across
1,310 fires; this run shows its *recall* is not zero when a genuine opaque secret is
actually present. Both can be true, and together they describe the real trade: the
detector does find the thing, and it also finds a thousand things that are not it.

**It does not change the recommendation.** n = 1, the secret was planted by the same
person reading the result, and it was chosen to be found — 5.00 bits/char is at the
extreme right tail of the corpus distribution, where only 11 of 5,636 real tokens sit.
A real credential in a real repository has no obligation to be that conspicuous. What
this run establishes is narrower and still useful: the pipeline works end to end, the
live redactor is genuinely on the wire, warn-mode is genuinely log-only, and the
detectors are capable of catching the class they were designed for. The reason to
hold B is the false-positive cost, not an inability to detect.

## Residual: the sink still has no *organic* data

The drill above proves the path writes when a grounded turn reaches it, and
`TestLogChunkScrub_WritesFiresToTheDurableSink` keeps that wire from silently
breaking. Neither produces organic data. That comes only from the daemon actually
being used on real workspaces — no code change will manufacture it, which is the
thing twelve days of waiting demonstrated.

---

## Re-measurement — 2026-08-08, at `23550e4`

The 2026-07-30 numbers above were taken on `harden/proxy-spend-and-gates`. This
repeats the same harness on `main` at `23550e4`, nine days and a great deal of
code later, to check whether the B-vs-C conclusion still holds against a grown
corpus. **It does.**

Reproduced offline in **1.10s** — the whole point of this harness, and the reason
the decision it feeds should not be waiting on organic data that has never
arrived.

| | 2026-07-30 | 2026-08-08 (`23550e4`) |
|---|---|---|
| Files scanned | — | 445 |
| Chunks | — | 3,711 |
| **Option A** — live structural redactor | on the wire | **21 chunks (5.7 per 1k)** |
| **Design B** — entropy, chunks with ≥1 fire | ~33% of chunks | **1,075 (29.0%)** |
| **Design C** — keyword, chunks with ≥1 fire | — | **44 (1.2%)** |

Option A's 21 redactions break down as `aws_access_key=17`, `private_key_block=5`,
`openai_key=4`, `github_token=3`, `slack_token=2` — structural patterns, matching
what they are named for.

**Design B still fails on the same ground, and the top-firing files say why:**
`proxy/main_test.go=103`, `clients/tui/chat_test.go=79`,
`docs/ARCHIVE/BACKLOG_2026-07.md=64`, `daemon/chunkscrub_test.go=50`. Entropy fires
hardest on test fixtures, archived prose and `go.work.sum` — a redactor that blanks
29% of a repository's chunks, concentrated in exactly the files a developer asks
questions about, destroys the product to protect against a class Option A already
covers structurally. The earlier ~33% and today's 29.0% are the same finding; the
small difference is corpus composition, not a change in behaviour.

**Design C fires on 1.2% of chunks**, and inspection shows those hits are
themselves mostly test fixtures containing literal `password` / `api_key` /
`client_secret` strings. Note the self-referential case: this very document fires
three times (`docs/CHUNK_SCRUB_FIRE_RATE.md` — `tokens`, `token`, `secrets`),
which is a fair illustration of keyword redaction's own false-positive mode on
documentation *about* secrets.

The entropy distribution is unchanged in shape: 8,735 tokens considered, mass
centred at 3.25–3.75 bits/char, with 1,921 fires above the 4.00 threshold. Hex
content caps at exactly 4.0 bits/char, which is why the threshold sits where it
does and why so much ordinary code sits just underneath it.

**Conclusion: no change to the standing recommendation — reject Design B, defer
Design C.** D5 remains the founder's call; this re-measurement removes "the data
might be stale" as a reason to defer it.

**Also fixed on this date:** the `warnscan` build tag was compiled by nothing —
not `make check`'s vet, not any CI job, not the pre-push hook. `make vet` now
includes `-tags warnscan` (commit `87af8d7`), so this harness cannot rot silently
between the rare occasions someone needs it.
