# F1 — Proxy-Side ZDR Enforcement: Design (for approval, NOT implemented)

**Status:** Design artifact only. No enforcement code exists yet. Un-applied,
un-committed. This document exists so the founder can approve or reject an
approach before any code is written.

**What F1 is** (BACKLOG.md:2485): *the proxy does not itself enforce ZDR — it
forwards the request body byte-for-byte, and the zero-data-retention routing
flags are set client-side in the daemon.* Founder-gated, part of the open P3/3A
gate. While F1 is open, customer-facing ZDR language was rewritten to the
**requested-not-enforced** framing (BACKLOG.md:2487); closing F1 is the trigger
to restore the enforced-guarantee wording in `PRODUCT_OVERVIEW.md`.

---

## 1. Where the body is forwarded today, and where a gate would insert

The proxy's single forward path is `handleChatCompletions`
([proxy/main.go:440](proxy/main.go#L440)):

| Step | Location | What happens |
|---|---|---|
| Read body once, bounded | [main.go:478-484](proxy/main.go#L478-L484) | `io.ReadAll` into `bodyBytes` (cap `maxRequestBodyBytes`, 4MB). |
| **Cost-authz peek (precedent)** | [main.go:489](proxy/main.go#L489) | `peekModel(bodyBytes)` → one-field decode, rejects with 403 if the model is off the allow-list. |
| Quota reservation | [main.go:497-511](proxy/main.go#L497-L511) | `peekMaxTokens` + `reserveQuota`. |
| Build upstream request | [main.go:516](proxy/main.go#L516) | `bytes.NewReader(bodyBytes)` — **the body forwarded byte-for-byte**. |
| Forward | [main.go:532](proxy/main.go#L532) | `p.client.Do(upstreamReq)`. |

**A ZDR enforcement step inserts at the same point the model-allow-list check
already lives — immediately after the body is read
([main.go:489](proxy/main.go#L489)), before the reservation and the forward.**
This is not a new architectural seam: the proxy *already* does a one-field,
content-blind peek of the body at exactly this spot and *already* rejects a
request (403 `model_not_allowed`) on policy grounds. F1 enforcement is a second
peek of the same shape reading the `provider` object instead of `model`.

The precedent matters for the ZDR posture argument (§4): `peekModel`
([main.go:996](proxy/main.go#L996)) and `peekMaxTokens`
([main.go:1040](proxy/main.go#L1040)) decode into a **single-field struct**, so
message content is never parsed. The SSE scrubber `stripSSEAccountMetadata`
([main.go:883](proxy/main.go#L883)) goes further: it decodes into
`map[string]json.RawMessage`, mutates only top-level *key names*, and leaves
every value (including `messages`) an **opaque byte slice**. Both disciplines
are directly reusable here.

---

## 2. What the daemon actually sends (grounds the whole design)

The routing object is OpenRouter's `provider` field. The daemon's wire struct
is `providerRouting` ([daemon/provider.go:44-48](daemon/provider.go#L44-L48)):

```go
type providerRouting struct {
    ZDR            bool   `json:"zdr"`
    DataCollection string `json:"data_collection"`
    AllowFallbacks bool   `json:"allow_fallbacks"`
}
```

It is serialized on **every** request inside `chatCompletionRequest.Provider`
([daemon/provider.go:60-66](daemon/provider.go#L60-L66), marshalled at
[provider.go:237-243](daemon/provider.go#L237-L243)). Critically, **no field is
`omitempty`** — the struct comment ([provider.go:43](daemon/provider.go#L43))
states this is deliberate: *"every request must state all three explicitly … so
enforcement is never silently absent from the wire body."*

Values come from `ZDRConfig.resolvedProviderRouting`
([daemon/config.go:83-93](daemon/config.go#L83-L93)), which is **secure-by-
default**: an absent/legacy `zdr` section resolves to `zdr:true`,
`data_collection:"deny"` (the weaken-bools default to the strict polarity,
[config.go:66-78](daemon/config.go#L66-L78)). The shipped `daemon/models.json`
sets `allow_non_zdr:false`, `allow_data_collection:false`, `allow_fallbacks:true`,
so today's resolved wire body is:

```json
{"zdr":true,"data_collection":"deny","allow_fallbacks":true}
```

confirmed live (BACKLOG.md:1628, D1 capture table).

**Consequence for enforcement:** because the current daemon *always* sends all
three flags with `zdr:true`/`data_collection:"deny"`, a rejection gate keyed on
those two is **invisible in practice for the shipped client** — it can only ever
fire for a *different* caller (a modified daemon, a direct API user holding a
valid Mochiii key, or a future client). That is exactly the population F1 is
about: enforcement that does not depend on trusting the client.

---

## 3. Two enforcement options

### Option (a) — REJECT: fail-closed on missing/wrong flags

The proxy decodes only the `provider` object (single-field struct, same
discipline as `peekModel`) and refuses the request unless it carries the required
ZDR flags. The body is **never mutated** — on the pass path it is still forwarded
by the same `bytes.NewReader(bodyBytes)` at [main.go:516](proxy/main.go#L516).

```go
// peekProvider — content-blind, one-field decode, mirrors peekModel.
func peekProvider(body []byte) (zdr bool, dataCollection string, present bool) {
    var peek struct {
        Provider *struct {
            ZDR            bool   `json:"zdr"`
            DataCollection string `json:"data_collection"`
        } `json:"provider"`
    }
    if err := json.Unmarshal(body, &peek); err != nil || peek.Provider == nil {
        return false, "", false
    }
    return peek.Provider.ZDR, peek.Provider.DataCollection, true
}

// Inserted right after peekModel's check (~main.go:489), before reserveQuota:
if zdr, dc, present := peekProvider(bodyBytes); !present || !zdr || dc != "deny" {
    // 403, JSON error body, same shape as model_not_allowed
    return
}
```

- **Guarantee:** no request lacking `zdr:true`+`data_collection:"deny"` is ever
  forwarded to OpenRouter through the proxy. Enforcement is now server-side: the
  proxy is the authority on *whether a non-ZDR request may pass*, not the client.
- **Byte-for-byte posture:** fully preserved. The pass path re-uses the existing
  `bytes.NewReader(bodyBytes)`; nothing is re-marshalled.
- **Content posture:** preserved — one-field decode, `messages` never touched.
- **Residual:** the proxy cannot *add* flags to a request that omits them; it can
  only refuse. For this product that is not a gap (the only client always sends
  them), and refusing is itself enforcement — but see the safer/riskier framing
  below.

### Option (b) — STAMP: the proxy sets the flags itself

The proxy decodes the body into `map[string]json.RawMessage`, overwrites (or
inserts) the `provider` object with a proxy-controlled `{"zdr":true,
"data_collection":"deny", …}`, re-marshals, and forwards the re-marshalled bytes.
Modeled exactly on `stripSSEAccountMetadata`
([main.go:883-914](proxy/main.go#L883-L914)): decode to `RawMessage` map, mutate
only the top-level `provider` key, re-marshal only when something changed, leave
every other value (including `messages`) an opaque byte slice.

- **Guarantee:** every forwarded request carries the ZDR flags regardless of what
  the caller sent — enforcement is fully independent of the client. A caller that
  sends `zdr:false` succeeds, silently corrected.
- **Byte-for-byte posture:** **broken by design** whenever a stamp is applied.
  Re-marshalling `map[string]json.RawMessage` re-serializes the top-level object
  (key order/spacing can change) even though the values stay opaque. On requests
  that already match, the "re-marshal only when changed" guard means they still go
  through byte-for-byte — so today's daemon traffic would be unaffected in
  practice, and only a non-conforming caller's body changes shape.
- **Content posture:** preserved (values stay `json.RawMessage`, never parsed) —
  but "never parsed" and "byte-for-byte" are now separate claims, and only the
  first still holds unconditionally.
- **Risk:** a client that *legitimately* set different routing — e.g. a future
  daemon that ships the D4 `order`/`only` provider allow-list, or a deliberate
  `allow_fallbacks` choice — would have that intent **clobbered** by a blind
  stamp. Stamping must therefore be written to *merge* (set `zdr`/`data_collection`
  without touching sibling routing keys), not to *replace* the whole `provider`
  object, or it collides with D4.

### Safer vs. less-disruptive, and the recommendation

- **Safer (strongest, client-independent guarantee):** **STAMP (b)** — the proxy
  guarantees the flags on the wire no matter what the caller sends.
- **Less disruptive to the current daemon:** **REJECT (a)** — the daemon always
  sends correct flags, so the gate never fires; no body is ever mutated; the
  README's byte-for-byte ZDR posture is untouched.

**Recommendation: Option (a) REJECT.** Rationale:

1. It closes F1's actual hole — *"a request without ZDR flags reaches
   OpenRouter"* — by refusing such a request at the proxy. Enforcement moves
   server-side; the guarantee no longer rests on trusting the client.
2. It preserves the byte-for-byte forwarding the proxy README
   ([proxy/README.md:10-22](proxy/README.md#L10-L22)) leans on as part of the ZDR
   story. Stamping would make that README claim conditionally false.
3. It reuses an existing, reviewed pattern (`peekModel` + 403) rather than the
   heavier re-marshal path.
4. It cannot clobber a legitimate D4 provider allow-list, because it never writes
   the body — de-risking the F1/D4 interaction entirely.

Choose STAMP only if the threat model must admit a *legitimate* client that omits
the flags (none exists in the current product), accepting the byte-for-byte break
and the merge-not-replace requirement.

---

## 4. Interaction with models.json and the byte-for-byte posture

- **The proxy has no `models.json`.** The load-bearing keys
  (`allow_non_zdr` / `allow_data_collection` / `allow_fallbacks`) live only in the
  **daemon's** config ([daemon/config.go:66-78](daemon/config.go#L66-L78)) and are
  read by `resolvedProviderRouting` into the per-request `provider` object. The
  hard constraint "never touch `daemon/models.json`" is unaffected by either
  option: **no enforcement design reads or edits `models.json`.**
- **Enforcement reads intent from the request body, not the config.** The daemon
  has *already* compiled those keys into the `provider` object on the wire
  (§2). The proxy reads that object — the daemon's expressed intent — with a
  one-field / `RawMessage` decode. It never needs to see `models.json` to know
  what ZDR posture the request claims.
- **Byte-for-byte:** Option (a) keeps it intact (read-only peek). Option (b)
  trades it away on the mutate path (re-marshal), which is why (a) is
  recommended. Either way, the "message content is never parsed" property — the
  stronger of the two ZDR properties the proxy advertises — is preserved by the
  single-field / opaque-`RawMessage` decode, exactly as `peekModel` and
  `stripSSEAccountMetadata` already do.

**Scope boundary (important, must not be oversold):** enforcing `zdr:true` at the
proxy makes the proxy the authority that *the flag is present and correct on the
wire*. It does **not** resolve the open D3/D4 edge — whether OpenRouter honors
`zdr:true` when `allow_fallbacks:true` and no ZDR endpoint is available
(BACKLOG.md:1681-1701). That residual lives on OpenRouter's side and is
untouched by F1. **Closing F1 ≠ closing the ZDR guarantee**; F1 removes the
"enforcement is client-side" caveat, D4 + the OpenRouter escalation address the
fallback-provider caveat. The two should be tracked as distinct closures.

---

## 5. Test plan

All in `proxy/main_test.go` (existing table-driven harness).

| # | Test | Asserts |
|---|---|---|
| T1 | **Normal request passes unchanged** | A body with `{"zdr":true,"data_collection":"deny","allow_fallbacks":true}` is forwarded; for (a), assert the upstream-received body is **byte-identical** to the input. |
| T2 | **Flags stripped → enforced** | A body with the `provider` object absent → (a) 403 with JSON error and **upstream never called**; (b) upstream receives a body whose `provider.zdr==true`. |
| T3 | **Wrong flags → enforced** | `zdr:false` and `data_collection:"allow"` variants → same outcome as T2. Assert upstream **never** sees `zdr:false`. |
| T4 | **Content preserved** | A body with a distinctive `messages[].content` string passes through; assert the content bytes are unchanged (for (b), survives the re-marshal). Guards the never-parse-content property. |
| T5 | **D4 co-existence** (only if (b) chosen) | A body carrying an `order`/`only` sibling key is stamped; assert the sibling key **survives** (merge-not-replace). For (a) this is subsumed by T1. |
| T6 | **Fail-when-neutered** | Comment out / bypass the enforcement gate and re-run T2+T3; assert they now **FAIL** (a `zdr:false` request reaches upstream). This proves the tests exercise the gate rather than passing vacuously. |
| T7 | **Regression** | Full existing `proxy` suite + `go test -race` stays green; `docker build ./proxy` (the CI job, `.github/workflows/build.yml`) still builds. |

The **fail-when-neutered** check (T6) is the load-bearing one: without it, T2/T3
could pass because some *other* layer rejected the request, not the new gate.

---

## OPEN QUESTIONS FOR FOUNDER

Answers needed before implementation:

1. **Reject or Stamp?** Recommendation is **(a) Reject** (§3). Confirm, or choose
   Stamp and accept the byte-for-byte break + merge-not-replace requirement.
2. **Does enforcement gate `allow_fallbacks` too, or only `zdr`+`data_collection`?**
   Rejecting `allow_fallbacks:true` at the proxy would re-break the 2026-07-10
   congestion fix (DeepInfra 429s). Recommendation: enforce only `zdr:true` +
   `data_collection:"deny"` here, and handle the fallback-provider risk via D4's
   provider allow-list — **not** by refusing fallback at the proxy. Confirm.
3. **Failure contract.** 403 with `{"error":"zdr_required"}` (mirroring the
   existing `model_not_allowed` shape at [main.go:489-495](proxy/main.go#L489-L495))?
   Or a different status/body?
4. **On by default, with an env override?** The model allow-list uses
   `ALLOWED_MODELS` (default-on, opt-out, loudly warned when disabled,
   [main.go:267-273](proxy/main.go#L267-L273)). Mirror that with e.g.
   `ENFORCE_ZDR` default-on? Or hard-code with no override so it can never be
   silently disabled?
5. **Launch-gate semantics.** Does closing F1 this way (proxy becomes the
   authority on the three flags) satisfy the launch gate *on its own*, given the
   D4/D3 fallback-provider edge remains open on OpenRouter's side (§4 scope
   boundary)? I.e. is F1 closure independent of the OpenRouter escalation
   outcome, or gated on it?

Until these are answered, no enforcement code will be written.
