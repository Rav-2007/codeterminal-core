# Scoping — scrubbing secret content out of retrieved chunks before they leave the machine

Status: **scoping analysis, not a design-to-implement, not a decision.** No fix
is written and none is picked here. The founder decides which (if any) of the
options in §4 to build, same as H6 and the North Star directions. Everything
below the "Conclusion up front" was re-verified against the real code in this
session — the exit path is *not* carried forward as assumed-true.

## 0. Conclusion up front

- **The exit is real and re-confirmed.** Retrieved chunk content is folded into
  the outbound completion request unscrubbed and POSTed to OpenRouter on every
  grounded prompt. Only the user's typed prompt is scrubbed. Verified end to
  end in §1.
- **This is layer two.** The secret-*file-name* gate (`MatchesSecretName`) is
  layer one: "does this file look like a secret?" Content scrubbing is layer
  two: "does this chunk's text contain a secret, regardless of filename?" Layer
  two is what makes every gap in layer one non-fatal. That's why it's the
  class-closer.
- **The whole difficulty is one tradeoff, and it is not free.** Every legitimate
  chunk (or span) wrongly redacted is retrieval context the model doesn't get —
  degrading answers on exactly the code the user asked about. Any design
  presented as pure upside is wrong. §3 is the crux.
- **There is a single, clean retrieval-time choke point** — `renderChunk`
  (`context.go:136`) — through which every chunk that reaches the prompt passes.
  And **the cheapest possible detector already exists**: `scrub()`
  (`scrub.go:52`) already does precision-first, span-level redaction; today it
  is simply never called on chunk content.
- **Two comments encode the wrong assumption**, not one: `server.go:201-203`
  *and* `scrub.go:50-51`. Whatever gets built must correct both (§5).

---

## 1. The exit path and the exact insertion point(s)

### 1a. Re-verified outbound trace (grounded prompt)

Every hop checked against current source this session:

1. `server.go:184` — `outcome := s.gatherContext(ctx, promptReq.Prompt)`.
2. `context.go:97-112` — `gatherContext` calls `retrieveTopK`, budget-truncates,
   and returns `retrievalOutcome{Chunks: kept}`. **`Chunk.Content` is carried
   raw** — nothing on this path scrubs it.
3. `server.go:204` — `cleanPrompt, redactions := scrub(promptReq.Prompt, …)`.
   This scrubs **only the typed prompt**. `outcome.Chunks` is not passed to
   `scrub`.
4. `server.go:207` — `augmentedPrompt = buildAugmentedUserMessage(cleanPrompt, outcome.Chunks)`.
5. `context.go:146-165` — `buildAugmentedUserMessage` loops chunks through
   `renderChunk`.
6. `context.go:135-137` — `renderChunk` folds `c.Content` into the message. The
   **only** transform applied is `neutralizeDelimiters` — and that is a
   prompt-injection defense (it swaps `<`/`>` in tag-like text for lookalikes),
   **not** a secret scrub. A private key or API token passes through it
   untouched.
7. `server.go:231` — `streamCompletion(…, augmentedPrompt, …)`.
8. `provider.go:168` — `buildChatMessages` places `augmentedPrompt` verbatim as
   the final `"user"` message.
9. `provider.go:170-176` → `provider.go:182-192` — JSON-marshalled and
   `http.DefaultClient.Do(POST {apiBase}/chat/completions)`.

**Conclusion:** chunk content leaves the machine, unscrubbed, on every grounded
completion. The prior sessions' finding holds.

### 1b. Candidate insertion points, and what each one costs

There are exactly two places a scrubbing step could sit. They are not
equivalent.

**Index-time** (scrub before the content is embedded/stored). The content is the
embedding input and the stored payload both:
- `chunker.go:256` sets `Chunk.Content`.
- `index_cmd.go:66-70` — `texts[i] = c.Content` is **what gets embedded**.
- `index_cmd.go:78` → `vectorstore.go:97` — `Content: c.Content` is **what gets
  persisted** (and `index_cmd.go:83` feeds the lexical/FTS5 tier too).

  So scrubbing here changes *both* the vector *and* the stored text. Upside:
  scrub once per index build, secret never lands in the DB at rest. Downside,
  and it is permanent: **the embedding is computed from the redacted text**, so a
  redaction alters that chunk's position in vector space for *every future
  query* — you pay a retrieval-quality cost on that chunk forever, even on
  queries that had nothing to do with the secret, and re-indexing is the only
  way to undo a bad rule.

**Retrieval-time** (scrub after fetch, before folding into the prompt). The
narrowest choke point is **`renderChunk` (`context.go:136`)** — both the
outbound `buildAugmentedUserMessage` (`context.go:155`) and the budget sizer
`truncateToBudget` (`context.go:122`) go through it, so a scrub placed inside
`renderChunk` covers the send *and* keeps size accounting self-consistent.
Upside: **the index is untouched** — vectors, stored text, ranking, and recall
are exactly as today; only the bytes handed to the model change, so a bad rule
degrades one send, not the corpus, and is reverted by shipping a code change,
not a re-index. Downside: it runs on every request (negligible — regex over a
few KB of already-budgeted text) and it does **not** protect the index at rest
(the secret still sits in the local vector DB; this item is scoped to the
network exit, not at-rest storage).

**What the rest of this doc assumes:** the primary insertion point is
**retrieval-time at `renderChunk`**, because it is the one that closes the
verified network exit *without* mortgaging retrieval quality. Index-time is
treated as an *optional add-on* for at-rest protection (Design C), never as the
sole mechanism — its permanent embedding pollution is the exact cost §3 warns
against, made irreversible.

---

## 2. Detection approaches, with honest tradeoffs

No single approach is complete. The design question is *which combination buys
the most coverage for the least false-positive damage.*

### 2a. Known-prefix / structural signatures — **already in the repo**
`-----BEGIN … PRIVATE KEY-----`, `AKIA…` (AWS), `ghp_`/`github_pat_` (GitHub),
`sk-…` (OpenAI-style), `xoxb-`/`xox[baprs]-` (Slack), `AIza…` (Google),
`sb_secret_…`, `mochi_…`, JWT shape, etc.
- **Catches:** known, prefixed formats with high precision.
- **Misses:** novel/opaque secrets with no recognizable prefix; a bare 40-char
  hex password with no marker.
- **FP profile:** lowest of any approach. Note this repo *already trusts these
  exact patterns* — `scrub.go:30-43` runs them against every user prompt today,
  span-redacting matches. Reusing them on chunk content inherits that trust and
  adds **zero new false-positive surface** beyond what production already
  accepts.

### 2b. High-entropy string detection
Flag long random-looking strings (Shannon entropy over a threshold).
- **Catches:** opaque secrets the signatures miss — the whole reason to
  consider it.
- **Misses:** structured secrets that *aren't* high-entropy (a dictionary-word
  passphrase).
- **FP profile — the worst, and this must not be hand-waved.** Source code is
  *full* of legitimately high-entropy strings: git SHAs, UUIDs, content hashes,
  base64-embedded assets/icons, minified JS, test fixtures, checksums, lockfile
  integrity hashes. The entropy-threshold tuning problem has no good setting:
  low threshold → shreds normal code; high threshold → misses real secrets that
  happen to be shortish or partly structured. There is no repo-independent knee
  — the "right" threshold differs between a crypto library and a CRUD app. Any
  entropy tier **must** ship behind a warn/measure mode first (§3) before it is
  ever allowed to redact.

### 2c. Assignment / keyword heuristics
`password =`, `api_key:`, `secret:`, `token =`, etc.
- **Catches:** config-shaped secrets where the value carries no prefix but the
  *key* names it.
- **Misses:** bare credential values with no adjacent keyword; and the inverse —
  the value may be a `${ENV_VAR}` reference or an empty string, i.e. not a
  secret at all.
- **FP profile: moderate and code-specific.** `password` and `secret` appear
  constantly in *non*-secret code: function names (`validatePassword`), struct
  fields, doc comments, test data (`password = "hunter2"` in a fixture),
  variable declarations. Redacting the value after such a keyword will
  frequently blank out legitimate identifiers the user is asking about.
  Span-redacting *only the value token* (not the line) limits the blast radius.

### 2d. Existing library rather than hand-rolling
- **gitleaks** — Go, and to my knowledge MIT-licensed and actively maintained;
  its value is a large, battle-tested **regex ruleset** more than as an imported
  package (it's built as a CLI). Borrowing/adapting its patterns is more
  attractive than vendoring the whole tool.
- **TruffleHog v3** — broad detection incl. live-credential verification, **but
  licensed AGPL-3.0** to my knowledge, which is a real landmine for a
  proprietary codebase — a copyleft obligation you almost certainly don't want
  linked into the daemon. Flagging it specifically so it isn't reached for
  casually.
- **Caveat, non-negotiable:** I do **not** have web access this session to
  re-verify license and maintenance status. Any dependency named here must have
  its license *and* current maintenance re-checked at build time before it's
  taken on. Do not adopt on the strength of this paragraph alone.
- **Cheapest reuse of all is internal:** the existing `scrub()` /
  `scrubPatterns` already *is* a maintained, precision-first structural
  detector this codebase owns and trusts. The minimal design (§4, Design A) adds
  **no dependency at all** — it just calls the function that already exists on a
  second input.

---

## 3. The false-positive cost — the crux, quantified where possible

Scrubbing is not free. Every legitimate chunk or span wrongly redacted is
context the model never sees, degrading answer quality on precisely the code the
user asked about. Three things must be decided honestly.

### 3a. Drop the chunk, or redact the span?
- **Whole-chunk drop** is maximally destructive: one matched token removes an
  entire ranked window — the top hit could vanish, and the model may lose the
  exact function the question was about.
- **Span redaction** (replace only the matched substring with
  `[REDACTED:<kind>]`, keep everything around it) is far less destructive and is
  **strongly worth preferring**. It is also *what `scrub()` already does today*
  (`scrub.go:66`, `ReplaceAllString`) — so the low-cost path is also the
  low-damage one. Recommend any design span-redact, never drop.

### 3b. Interaction with the evals
A scrubbing step changes which bytes reach the model, so it can move retrieval
eval results, and this **must be measured**, not assumed neutral:
- **Locate eval** — per the standing saturation flag, it is **too coarse to
  detect subtle degradation**. A scrub that quietly strips a few lines from
  otherwise-correct chunks can leave the locate headline unmoved while real
  answer quality drops. Do not read "locate eval unchanged" as "no harm."
- **Edit eval** — `n=4` (standing small-sample flag). Underpowered to catch a
  small regression; a single flipped case swings the number.
- **Index-time scrubbing additionally moves the *embedding*** (§1b), so it can
  change *ranking and recall*, not just visible content — a strictly larger
  eval-measurement burden than retrieval-time, against evals already flagged as
  too blunt to trust for subtle moves.

### 3c. A "warn / log-but-don't-scrub" mode first — recommended precursor
Before committing to redaction that alters output, run detection in
**log-only** mode over real repos to measure *how often it actually fires and on
what*. This directly de-risks §3a/§3b: you learn the true false-positive rate on
real code before any user answer is affected, and you can tune (or reject) a
tier — especially the entropy tier (§2b) — on evidence instead of guesswork.
Cheap to build, and it mirrors the existing redaction-notice plumbing
(`server.go:209-220` already logs redaction *kinds* without leaking matched
text — the same discipline extends naturally to a chunk-scrub log line).

---

## 4. Candidate designs, sized (not ranked, not chosen)

Three end-to-end options, minimal → thorough. **The founder picks; this section
does not.**

### Design A — Minimal: reuse `scrub()` at retrieval-time, span-redact
- **Detection:** the existing `scrubPatterns` (structural signatures only).
- **Insertion:** call `scrub()` inside `renderChunk` (`context.go:136`) on
  `c.Content` before/after `neutralizeDelimiters`. Retrieval-time only.
- **FP handling:** span redaction (inherited from `scrub()`).
- **Build cost:** very small — one call site, no new patterns, no new
  dependency, no config surface. Extend the existing redaction-notice log.
- **Coverage:** every known key format layer one recognizes, now caught by
  *content* regardless of filename — closes the "secret in a file the name gate
  didn't flag" hole for those formats.
- **FP risk:** ~as low as it gets; identical precision to what production
  already trusts on prompts.
- **Eval burden:** low but not zero — measure locate + edit eval for movement;
  a chunk that legitimately *shows* an `sk-…` example (docs, this very file)
  would get redacted.
- **Surface:** contained fix. Does **not** catch opaque/novel secrets (§2a
  misses) and does **not** protect the index at rest.

### Design B — Middle: signatures + keyword heuristics, warn-mode first
- **Detection:** Design A patterns **plus** assignment/keyword heuristics
  (§2c), value-span redaction only.
- **Insertion:** retrieval-time at `renderChunk`; ship in **log-only mode
  first** (§3c), flip to redaction after measuring fire rate on real repos.
- **FP handling:** span-redact the value token, never the line/chunk.
- **Build cost:** moderate — new patterns, a warn/redact toggle, log plumbing.
- **Coverage:** adds config-shaped secrets (bare values named by an adjacent
  key) that structural signatures miss.
- **FP risk:** moderate and code-specific (`password`/`secret` in fixtures,
  field names, comments) — which is exactly why the warn-first step is part of
  the design, not optional.
- **Eval burden:** moderate; the keyword tier is the part most likely to move
  answer quality, and the coarse locate eval may not show it — lean on the
  warn-mode measurement.
- **Surface:** still retrieval-time and contained, but it opens the
  "heuristic tuning" surface (which keywords, how wide the value span).

### Design C — Thorough: signatures + tuned entropy + keyword, index **and** retrieval time
- **Detection:** all three tiers (§2a+§2b+§2c), entropy threshold tuned against
  warn-mode data.
- **Insertion:** **both** — index-time (protect the DB at rest, scrub before
  embed/store) *and* retrieval-time (belt-and-suspenders on the send).
- **FP handling:** span redaction throughout; entropy tier gated behind a proven
  threshold.
- **Build cost:** high — entropy detector + tuning, a second insertion point,
  and an **index migration / re-index** requirement (existing indexes must be
  rebuilt to gain at-rest protection).
- **Coverage:** highest — catches opaque secrets the other designs miss, and
  removes the secret from local storage, not just the wire.
- **FP risk:** highest — the entropy tier's SHA/UUID/base64/minified-code
  problem (§2b), now applied to the corpus.
- **Eval burden:** highest — index-time scrubbing **changes embeddings**, so it
  can move *ranking and recall*, and it must be measured against evals already
  flagged (saturation, `n=4`) as too coarse to trust for subtle regressions.
  Permanent: a bad rule is only undone by re-indexing.
- **Surface:** opens the widest surface — two code paths, entropy tuning, a
  migration story, and irreversible corpus effects.

---

## 5. The `server.go:203` comment (and its twin)

Whatever gets built must correct the false "retrieval never leaves this machine"
assumption at its source — and it lives in **two** places, not one:

- `server.go:201-203` — "outcome.Chunks (…gathered above…, which is fine since
  retrieval never leaves this machine) is never scrubbed."
- `scrub.go:50-51` — "Only ever scrub the user's own typed prompt; never
  RAG-retrieved chunk content, which is local workspace text … and must reach
  the model unmodified."

Both encode the exact wrong belief that let this exit exist. This is small, but
it belongs to this fix's lineage: the comment is *why* the gap was there, so
correcting it is part of closing the gap, not a cleanup afterthought. (Note the
scrub.go comment doesn't merely misdescribe — it *instructs* future code never
to scrub chunk content, so leaving it in place would actively argue against any
design here.)

---

## 6. Interaction with the other open items

- **FAIL-1 policy breadth (name gate) — complementary, not substitutable.** The
  name gate (`MatchesSecretName`, layer one) asks "does this *file* look like a
  secret?"; content scrubbing (layer two) asks "does this *text* contain a
  secret?" Broadening the name gate shrinks layer one's gaps; content scrubbing
  makes whatever gaps remain non-fatal. You want both — neither replaces the
  other, and this task does not touch FAIL-1's breadth work.
- **The ZDR question — orthogonal, and content scrubbing wins regardless of ZDR's
  outcome.** ZDR governs whether the *provider* retains what we send; content
  scrubbing governs *what we hand over in the first place*. Even a perfectly
  honored ZDR route still receives the raw secret in-flight; scrubbing reduces
  exposure on that route no matter how the OpenRouter/ZDR retention finding
  resolves. So its value does **not** depend on ZDR being fixed — it's
  independent defense in depth on the same completion hop.
- **Sequencing vs. the socket-axis review (still owed) — no hard ordering, but
  one-way coupling.** The socket review is a different axis (who can drive the
  daemon / request forgery); this is about what leaves on the completion hop.
  Neither blocks the other. But note the direction of influence: this exit is
  only reachable by a party that can already drive a grounded prompt — so if the
  socket review finds the socket is *more* exposed than assumed, that **raises
  the priority** of content scrubbing (more parties could trigger exfil). Build
  order is the founder's call; the dependency is on *priority*, not on
  correctness — chunk scrubbing is correct and buildable independent of the
  socket review's outcome.

---

*Scope only. No code was written or committed for this item. Options in §4 are
sized, not ranked; the design decision is the founder's.*
