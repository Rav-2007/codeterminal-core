# Ultra Master Plan — 2026-08-08: Packaging

**Supersedes** [`MASTER_PLAN_2026-08-07.md`](MASTER_PLAN_2026-08-07.md), whose
Stages 0–2 are done and whose Stages 4–5 are carried forward unchanged in §7.
Roles: CTO · System Designer · Security Designer.

**Every fact below was re-derived against the source and the artifacts on disk
this session.** Where it contradicts the previous plan, §1 says so. Two of its
Stage 3 assumptions were wrong, and one of them is a shipping blocker it never
saw.

---

## 1. What this pass overturned

### 1.1 A `.vsix` already exists — and it is evidence, not progress

Every plan and register says "no `.vsix`". There is one:
`clients/vscode/codeterminal-vscode-0.0.1.vsix`, 11.3 MB, built 2026-08-05,
gitignored. It is **unshippable**, and cataloguing exactly how is the cheapest
requirements document available:

| Defect | Detail |
|---|---|
| **No embedder helper** | The package ships `codeterminal-daemon` and no `codeterminal-embedder-helper`. Retrieval is dead on arrival — the product's whole thesis, silently degraded to `reasonEmbedderUnavailable` |
| **Predates daemon management** | No `daemonSupervisor.ts`. It carries the pre-2026-08-07 architecture where the user starts a daemon by hand |
| **Ships its own source** | `src/`, `src/test/`, `tsconfig.json`, `.vscode/`, `.gitignore` — no `.vscodeignore` exists, so vsce packaged the directory |
| **Ships both logos** | `logo.png` (276 KB) *and* `logo.jpg` (147 KB) |
| **One platform, unlabelled** | A Linux ELF daemon in a package with no `--target`. Install it on macOS or Windows and it is a 19.7 MB inert file |
| **`"private": true` survived** | Present in the *packaged* manifest |

**Design consequence.** Packaging is not "run vsce". Six of those seven are
content-selection failures that a `.vscodeignore` and a `--target` matrix fix
structurally, and the plan below fixes them by construction rather than by
remembering.

### 1.2 The previous plan named the wrong install directory

It said binaries land in `bin/`, "which `resolveHelperBinPath` already checks
first". They do not, and it does not. The real layout is better, and it is
already agreed on by **three independent resolvers** that were written at
different times for different reasons:

| Resolver | First/relevant candidate |
|---|---|
| `activate` in [`extension.ts`](../clients/vscode/src/extension.ts) | `<extensionPath>/daemon/codeterminal-daemon[.exe]` |
| `resolveHelperBinPath` in [`daemon/helperpath.go`](../daemon/helperpath.go) | `<exedir>/codeterminal-embedder-helper` — its comment literally says *"installed side by side (release layout)"* |
| `resolveConfigPath` in [`daemon/configpath.go`](../daemon/configpath.go) | `<exedir>/models.json` |

So the shipping layout is fixed, and **requires zero Go changes**:

```
<extensionPath>/daemon/
    codeterminal-daemon[.exe]
    codeterminal-embedder-helper[.exe]
    models.json
```

This is the single most useful finding in this pass. It means Stage 3 is a
**build-and-ship problem, not a code problem** — with exactly one exception,
which is the next section.

### 1.3 THE BLOCKER: `/mcp-server` executes a binary from the opened repository

**Severity: P0. This must be fixed before a single stranger installs anything.**

`runMCPServerList` in [`chatPanel.ts`](../clients/vscode/src/chatPanel.ts)
resolves its binary like this:

```ts
const candidates = [
  path.join(workspace, 'daemon', 'codeterminal-daemon'),
  path.join(workspace, 'codeterminal-daemon'),
  'codeterminal-daemon',
];
const bin = candidates.find(/* first that exists */);
// ... execFileAsync(bin, args, { cwd: workspace })
```

`workspace` is the folder the user opened. **A repository that ships a file at
`daemon/codeterminal-daemon` gets it executed when the user types
`/mcp-server`.** Clone a repo, open it, type one slash command, run their code.

This is not a novel class here. **The TUI had the identical bug and it was fixed
in `d56e425`** — *"fix(tui,daemon): never resolve a binary against the working
directory"*. The comment left behind in [`clients/tui/slash.go`](../clients/tui/slash.go)
describes this attack in as many words. The fix resolved against the TUI's own
`exedir` plus `exec.LookPath`. **The VS Code extension was not carried along**,
and its version is strictly worse: the TUI's exposure depended on the CWD
happening to be the workspace, while the extension passes the opened folder
explicitly.

`resolveHelperBinPath`'s header calls this out too — *"Same defect class as the
TUI's `/mcp-server` binary hijack, which was CONFIRMED by execution"* — while the
extension's copy sat unreferenced. **No test covers it.**

**Why this reorders the plan.** Today the blast radius is one machine whose owner
built the extension from source. Publishing to a marketplace converts it into
"install our extension, then any repository you open can run code." Packaging
does not *cause* the bug; packaging is what makes it matter.

### 1.4 The helper's cross-build question is answered

The previous plan recorded CGO cross-compilation as unverified risk. Measured:

- `CGO_ENABLED=0 go build` on `helper` **fails** — `onnxruntime_go: build
  constraints exclude all Go files`. CGO is mandatory.
- But onnxruntime is **`dlopen`'d at runtime**, not linked: `ort.SetSharedLibraryPath`
  in [`helper/onnxembedder.go`](../helper/onnxembedder.go). So a cross-build needs
  **a C toolchain for the target and nothing else** — no onnxruntime headers, no
  import library.

**Decision: build every platform on its own runner, and cross-compile nothing.**
mingw-w64 would work for Windows, but macOS needs an SDK whose redistribution is
legally constrained, and CI already has `windows-latest` and `macos-latest`
proven green. Native builds also make the signing steps (§5) land on the only
machines that can perform them.

### 1.5 CI keeps nothing it builds

The `cross` job builds and tests on Windows and macOS and uploads **no
artifacts** — `grep upload-artifact` is empty. There is no release pipeline of
any kind. Stage 3.3 builds one.

---

## 2. Sizes, so the UX decisions are made against numbers

| Component | Size | Ships in the `.vsix`? |
|---|---|---|
| `codeterminal-daemon` | ~19.7 MB | **Yes** |
| `codeterminal-embedder-helper` | ~7.6 MB | **Yes** |
| `models.json` + extension JS + one logo | < 1 MB | **Yes** |
| BGE model + tokenizer | **34.7 MB** | **No** — first-run download |
| onnxruntime shared library | **8.6 MB** linux · **31.7 MB** darwin · **75.7 MB** windows | **No** — first-run download |

**Per-platform `.vsix` ≈ 28 MB uncompressed, ~12 MB on the wire.** First-run
download is **43 MB on Linux, 66 MB on macOS, 110 MB on Windows** — say the real
number in the prompt, and say it per platform rather than quoting the worst case
to everyone.

**Why the model is not bundled.** Tripling the package to embed an asset that is
already sha256-pinned and cached per user buys nothing, and it would put a 110 MB
download in front of every update. The product already degrades correctly without
it (`disabledRetrieval(reasonEmbedderUnavailable)`), which is what makes the
download a prompt rather than a gate.

---

## 3. Design

### 3.1 One shape, decided

**Platform-specific `.vsix` via `vsce package --target`**, one per
`linux-x64`, `darwin-arm64`, `win32-x64`. The marketplace serves the matching
one automatically. The alternative — one universal package carrying all three
platforms' binaries — is ~84 MB of which two thirds is guaranteed dead weight on
every install.

**Intel Mac (`darwin-x64`) is not a target**, because upstream onnxruntime v1.26.0
ships no binary for it. This is already an explicit, well-handled refusal in
`onnxruntimefetch.go`; publishing no `darwin-x64` package is the honest
expression of the same fact, and the marketplace will simply report the extension
as unavailable rather than installing something broken.

### 3.2 Content selection is structural, not remembered

`.vscodeignore` is an **allow-list-shaped denylist**: everything except
`out/`, `media/` (one logo), `daemon/`, `package.json`, `README.md`, `LICENSE`.
The Aug-5 package shipped source and tests because nothing prevented it, and
"remember to exclude tests" is not a control. §6 gates this with an assertion on
package *contents*, not on the packaging command's exit code.

### 3.3 The release pipeline

A new `release.yml`, tag-triggered (`v*`), separate from `build.yml` so ordinary
pushes never pay for it:

```
job build-binaries   matrix: [ubuntu-latest, macos-latest, windows-latest]
  └── go build daemon + helper (CGO on, native)
  └── sign        (macOS: codesign; Windows: signtool — §5)
  └── upload-artifact  <os>-binaries

job package         needs: build-binaries       runs-on: ubuntu-latest
  └── download all three artifacts
  └── per target: stage daemon/ + helper + models.json, then
      vsce package --target <t> -o codeterminal-<t>-<version>.vsix
  └── assert package contents (§6 Gate 3)
  └── upload .vsix files + SHA256SUMS to the GitHub Release

job publish         needs: package     MANUAL APPROVAL / separate dispatch
  └── vsce publish
```

**`publish` is deliberately not automatic.** Marketplace publication is a founder
action and irreversible in practice — a bad version cannot be unpublished
cleanly, only superseded.

### 3.4 What the extension must change

Small, and mostly deletion:

- **Fix `runMCPServerList`** (§1.3). Resolve against `context.extensionPath/daemon/`
  and `PATH` only. **Never** against `workspace`. Mirror the TUI's fix so one
  sentence describes both.
- **`package.json`**: drop `"private": true`; add `license`, `icon`,
  `repository`, real `categories`, `@vscode/vsce` as a devDependency, and a
  `contributes.configuration` block (**zero settings are contributed today**).
- **`activationEvents: ["onStartupFinished"]`** — the extension now manages a
  daemon lifecycle, not just a command.
- **First-run model prompt**, never a gate.
- **Delete the "start the daemon by hand" instructions** in `chatPanel.ts` and
  `slashCommands.ts`. They describe an architecture that no longer exists and
  would teach a new user a workflow the supervisor makes wrong.

---

## 4. Stages, with a gate on each

Ordered so the tree is shippable-or-honest at every boundary. **Estimated 6–9
working days to a `.vsix` a stranger can install**, excluding Apple developer
enrolment, which is calendar time nobody controls.

### ✅ Stage 3.0 — Close the RCE — **DONE 2026-08-08** (`b7e393d`)

> Two RCE paths, not one. The binary candidate was the known bug; `--config
> <workspace>/models.json` was found while fixing it and is worse, because
> `mcp list` STARTS the servers a config names and `acknowledged_unconfined`
> lives in that same attacker-written file. Both CONFIRMED by execution in a
> real Extension Development Host, both neuter-verified. `daemonBinary.ts` is
> now the one implementation and `extension.ts` shares its layout helpers.

Fix `runMCPServerList`. **Write the failing test first**: a fixture workspace
containing a fake `daemon/codeterminal-daemon` that writes a sentinel file when
executed; assert the sentinel never appears. Watch it fail against today's code,
then land the fix.

> *Gate:* the exploit test fails on unpatched code and passes after, demonstrated
> — not asserted. Mirrored in the TUI's suite so the pair cannot drift apart
> again, which is how this one survived `d56e425`.

### ✅ Stage 3.1 — Manifest and content selection — **DONE 2026-08-08** (`4453825`)

`.vscodeignore`, the `package.json` fields, `@vscode/vsce` pinned as a
devDependency.

> *Gate:* `vsce package` succeeds locally, and an assertion on the resulting
> archive's file list shows **no `src/`, no `test`, no `.vscode`, exactly one
> logo** — asserted against contents, because that is the failure the Aug-5
> package actually had.

### ✅ Stage 3.2 — Bundle the runtime — **DONE 2026-08-08** (`4453825`)

> Proven by execution, not by reading: the packaged daemon was run from `/` and
> found its helper (`vector_length=384`) and its `models.json` with no flags.

Stage `daemon`, `helper` and `models.json` into `<extensionPath>/daemon/`. No Go
changes; §1.2 is why.

> *Gate:* on a machine with **no Go toolchain and no repository**, install the
> `.vsix`, open a folder, and get a grounded answer. Retrieval working is the
> assertion — it is what the missing helper broke, invisibly, in the Aug-5
> package.

### ✅ Stage 3.3 — The release pipeline — **WRITTEN 2026-08-08** (`bd7f975`), NOT RUN

> `release.yml` exists and its YAML parses; **no tag has fired it**, so the
> three-target matrix is PLAUSIBLE, not CONFIRMED. The target-aware gate IS
> verified locally: the linux package checked as `win32-x64` fails on both
> missing `.exe` names.

`release.yml` per §3.3, artifact upload, three targets, checksums.

> *Gate:* a tag produces three `.vsix` files and a `SHA256SUMS`, and the
> `linux-x64` one installs and works. Windows and macOS are gated separately at
> 3.5, because that is where signing lands.

### ✅ Stage 3.4 — First-run experience — **DONE 2026-08-08** (`bd7f975`)

> `download-model --check`, prompt-not-gate, per-platform size reported BY THE
> DAEMON. Resumable download was **not** built: `downloadAsset` already writes
> `<name>.part` and renames only after verification, so a cancelled download
> leaves nothing a check would mistake for good. Resuming would save bandwidth,
> not correctness — deferred deliberately.

Model-download prompt with the real per-platform number, progress in a
notification, resumable download (`.part` + `Range` — safe *because* `verifyAsset`
already sha256s everything). Decline must leave a working, ungrounded product and
a way to change your mind.

> *Gate:* decline the download → the extension answers ungrounded and says why.
> Accept → progress is visible and a mid-download cancel leaves nothing corrupt.

### Stage 3.5 — Signing, and first contact on real hardware (2–3 days + enrolment)

macOS `codesign` + `notarytool` + stapling; Windows signing if a certificate
exists. §5 covers what happens if they do not.

> *Gate:* on a **clean VM per platform** — no toolchain, no repo — install,
> open a repo, ask a question, apply an edit, undo it. Gatekeeper and SmartScreen
> behaviour recorded verbatim, including the exact dialog text.

---

## 5. Security design

Packaging changes the threat model: the code stops running only on the machine
that built it, and starts running on machines whose repositories we do not
control.

**1. Repository-controlled code execution — the class, not just the instance.**
§1.3 is one occurrence of a pattern that has now appeared **three times**: the
TUI's `/mcp-server`, `resolveHelperBinPath`'s CWD candidate, and the extension's
`/mcp-server`. Two were fixed independently and the third survived because
nothing enforced the rule across surfaces. **Stage 3.0 adds that rule as a test
in both clients.** The invariant is one line: *an executable path is derived from
our own installation directory or `PATH`, never from workspace content.*

**2. We are now shipping executables to strangers.** The `.vsix` becomes a
supply-chain artifact. Publish `SHA256SUMS` with every release; build only in CI
from a tagged commit, never from a developer machine; keep `publish` a separate
approved step so no automatic path exists from a merge to the marketplace.

**3. Gatekeeper is a hard gate, SmartScreen is a soft one.** Unsigned Mach-O
binaries inside a `.vsix` are quarantined and killed on macOS — the daemon will
not start, and the failure surfaces as an unexplained "daemon not running". This
is the one item that can stop a platform outright. **If Apple enrolment is not
done, ship `linux-x64` and `win32-x64` and say plainly that macOS is pending
notarization.** Shipping a macOS package that cannot start is worse than shipping
none.

**4. The proprietary licence constrains marketplace framing.** `LICENSE` is "All
rights reserved". The listing must not imply open source, and the `license` field
must point at the real file.

**5. What does *not* change, and must be re-stated in the listing.** Lane B stays
**unconfined** — consent and audit, never "sandboxed". Secret scrubbing stays
heuristic and client-side. A stale index still reports `grounded ✓`. Packaging
must not quietly upgrade any of these claims for a marketing surface.

**6. Least privilege in the manifest.** Contribute only the commands and settings
actually used. Every capability declared is attack surface a reviewer must trust.

---

## 6. Verification

Unchanged discipline, and none of it relaxes for a release:

- **Every fix carries a test demonstrated to fail when neutered.** The Stage 3.0
  exploit test is the sharpest instance and is written before its fix.
- **Gates assert on artifacts, not on exit codes.** `vsce package` returning 0
  is what produced the Aug-5 package.
- **The end-to-end gate runs on a clean VM per platform**: no Go, no compiler,
  no terminal, no repository. Install, open a repo, ask, apply, undo.
- **Nothing is marked CLOSED.** Implemented-and-verified is where engineering
  stops.
- Anything not run on hardware says **NOT RUN** until it has been.

---

## 7. Carried forward from the superseded plan

Unchanged, and still after packaging:

- **Stage 4 — Index honesty.** A stale index still reports `grounded ✓`; nothing
  records when it was built. Add `BuiltAt` to `embedderStamp` (a zero value means
  *unknown* and must suppress the signal, not assert staleness) and surface it
  through the existing `Degradation` framework, on `StatusRequest` only.
- **Stage 5 — Pilot.** 10–25 users with hand-inserted keys.
- **Parked, explicitly:** register items 7, 10, 12; `ErrorClass` wiring;
  D1–D3 and D5–D8; the shared-helper refactor (200–300 MB RSS per open window,
  a v1.1 item); splitting `SECURITY_MODEL.md`; relocating the `mcp-servers/`
  eval fixture.

---

## 8. Risks, stated rather than smoothed over

1. **Apple enrolment is calendar time, not work time.** It gates macOS entirely
   and nothing in this plan shortens it. Start it on day one, in parallel.
2. **`vsce package` may still refuse something.** `@vscode/vsce` is not
   installed; the Aug-5 package was produced by an unknown version and kept
   `"private": true`, so its tolerances are not knowable from here. Stage 3.1 is
   deliberately early and cheap for this reason.
3. **The clean-VM gate needs clean VMs.** Without one per platform, Stage 3.5's
   gate degrades to "it worked on the build machine", which is the assurance
   level this project exists to reject.
4. **Windows and macOS have run tests but never run the PRODUCT.** Green CI says
   the packages compile and the units pass; it says nothing about an extension
   host spawning a signed daemon that finds its helper.
5. **The RCE may not be the only one.** §1.3 was found by reading one function.
   That is evidence about the search, not about the remainder — a focused sweep
   of every path the extension derives from workspace content is worth half a day
   before publishing.
