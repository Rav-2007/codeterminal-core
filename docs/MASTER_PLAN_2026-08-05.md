# Master Plan — debug the project, then outperform

**2026-08-05.** Roles: CEO · CTO · Security Patcher · Security Improver · Tester.
Supersedes the launch plan's sequencing where they disagree; that plan's Windows
work is folded in as Track C.

---

## The finding that sets the agenda

A prior session ran an Enterprise QA campaign and recorded **PASS** on SHA
`8979530`, including "native Bubblewrap/Docker subprocess sandbox isolation" as
**CONFIRMED**. Two hours of execution-based review found:

| | |
|---|---|
| **The bwrap sandbox had never run.** It passed `--nosuid`, which is a `mount(2)` option and not a bwrap flag. Real bwrap answers `Unknown option --nosuid` and refuses the whole invocation. | Every sandboxed command failed 100% of the time |
| **All six sandbox tests stub `lookPath` and assert on the argument *list*.** None executes bwrap. | The suite reports `ok` against a totally broken sandbox — verified by restoring the flag |
| **`sandbox_exec` was named for a sandbox it never used.** It called `exec.CommandContext` directly with `cmd.Env` nil, inheriting `OPENROUTER_API_KEY` and `CODETERMINAL_MOCHIII_KEY`. | One approved call against a repo with a hostile `Makefile` exfiltrates the user's inference credentials |

None of this is a criticism of ambition — the sandbox design is genuinely good.
It is a criticism of **evidence**. The campaign measured argument lists and
called it isolation.

**So the first principle of this plan is not a feature. It is:
every security control must be proven by executing it, or it is not a control.**

---

## Standing rules (non-negotiable)

1. **Test through the door the user comes in.** PART 0 rule 4 has now been
   learned three times here: Tier 2's Fix 7, the launch-plan chunk-key bug, and
   the sandbox. A control tested one layer below where it runs is untested.
2. **Never claim confinement you have not executed.** Lane B's honesty rule
   ("consent and audit, never sandboxed") now applies to Lane A too.
3. **A tool's description is a security surface.** It is what the approving
   human reads. `sandbox_exec` said "restricted sandbox" while running on the
   host with full credentials.
4. **Credentials never enter a subprocess.** `mcp.ServerEnv` exists; every
   `exec.Cmd` in this repo uses it or explains why not.
5. **Ratchets only tighten.** Floors up, errcheck ceilings in both directions.
6. **Nothing is CLOSED by engineering.** Implemented-and-verified is the stop.

---

## Track A — Security, and the QA that can actually see it (highest priority)

**A1 — DONE this session.** `sandbox_exec` routed through the real sandbox,
environment scrubbed with `ServerEnv`, description rewritten to state what is
and is not confined. Five regression tests drive the real handler with a real
hostile Makefile.

**A2 — DONE this session.** `--nosuid` removed; four tests added that *execute*
bwrap and assert the properties that matter: arguments accepted, command runs,
**a file outside the workspace is unreadable**, `--unshare-net` really removes
the network. Neuter-verified: restoring the flag fails the new tests while the
old suite still says `ok`.

**A3 — the TCP transport.** `protocol` gained a TCP backend that activates
whenever `HOST` is set in the environment. The daemon's own package comment
still reads *"It never listens on a network port."* Measured: `SO_PEERCRED` on a
TCP socket returns uid `4294967295` (UID_INVALID), so `authorizePeer` refuses —
but that is **defence by coincidence**, it is untested on macOS, and it is a
silent, environment-triggered change of transport. Decide: delete it, or gate it
behind an explicit flag with a documented auth story. Do not leave it implicit.

**A4 — sweep every `exec.Cmd` in the repo** for a nil `Env`, the same defect
class as A1. `helperproc.go` and `stdioclient.go` are known-good; the new
`lsp_bridge.go` and `watcher.go` have not been checked.

**A5 — re-audit the new tool surface.** `mcp_ast_edit.go`, `mcp_lsp.go`,
`lsp_bridge.go` all reached `main` without a security pass. They accept
model-controlled input and touch the filesystem.

**A6 — a QA rule change, not a task.** Any test that stubs a `lookPath`,
`exec.Command`, or filesystem seam must be paired with one that does not, or the
control it covers is recorded as **NOT RUN**.

---

## Track B — Correctness debt the campaign missed

**B1 — DONE.** Two TUI tests were failing on the working tree.
Root cause: the `enter` key handler was a byte-for-byte copy of `tab`, so Enter
could never submit a slash command — every no-argument command (`/help`,
`/clear`, `/git`, `/init`, `/context`, `/exit`) needed two presses. Enter now
submits; Tab completes; Enter accepts a completion only after arrow-key
navigation.

**B2 — DONE.** `gofmt` was failing on five files, two of which (`daemon/main.go`,
`daemon/provider.go`) were clean at HEAD. The uncommitted work did not pass the
project's own first gate.

**B3 — remove `patch.py` / `patch2.py`.** Regex scripts that rewrote `chat.go`;
they are how B1 and B2 got in. Not a build tool, not tested, and dangerous to
re-run.

**B4 — the index still has no freshness signal.** Unchanged from the launch
plan, and now more urgent because `watcher.go` exists but is unwired. Finish it
or surface staleness through the existing `Degradation` framework.

---

## Track C — Windows and packaging (from the launch plan, still live)

C1 Windows compiles, binds a named pipe, and authenticates a peer — landed in
`df0d663`, `1f5a1aa`, `8979530`. **Never run on hardware.**
C2 Confinement under Windows path semantics — `pathhazard.go` landed and is
verified on Linux; `realPath`/`GetFinalPathNameByHandleW` still outstanding.
C3 CI matrix: `windows-latest` + `macos-latest`. **This is what converts every
"NOT RUN on hardware" label into evidence, and it is the highest-leverage
remaining item in the whole plan.**
C4 Platform-specific `.vsix`, bundled daemon, first-run model download.

---

## Track D — Outperforming, once the base is trustworthy

Deliberately last. A faster assistant that leaks credentials is worse than a
slow one.

**D1 Retrieval quality** is the product. The embedding ceiling (0.0147–0.0164
raw similarity clustering) is the known limit; a better model will not fix it.
Chunking and query expansion will.
**D2 Agent loop**: the tool menu is flat between 5 and 12 tools (measured), so
spend the budget on *better tools*, not more.
**D3 The new tools** — AST edit, LSP definitions/references — are the real
differentiator against grep-based competitors. Finish and secure them.
**D4 Index freshness** (B4) gates all of the above: a fast wrong answer from a
stale index is the worst outcome available.

---

## Enterprise QA — the gate that would have caught this

Phases run in order. **A phase may not be skipped because a later one is more
interesting.**

| Phase | Question | Evidence that counts |
|---|---|---|
| 0 Baseline | Does it build and pass its own gate? | `make check` exit 0 on the **working tree**, not on a SHA behind it |
| 1 Execution | Does every security control actually run? | The control executed against the real OS mechanism; stubs disqualify |
| 2 Confinement | Can it reach what it must not? | A file outside the workspace, the network, the daemon's credentials — each attempted and refused |
| 3 Credentials | What does a subprocess see? | `env` dumped from inside a real child; no key present |
| 4 Consent | Does the prompt tell the truth? | Tool description read against handler behaviour, word by word |
| 5 Platform | Does it work where it claims? | Executed on that OS, or labelled **NOT RUN on hardware** |
| 6 Regression | Does the fix fail when neutered? | Neutered, observed failing, restored |

**Ship rule.** Any Phase 1–4 failure is a stop. A Phase 5 gap is shippable only
if the claim is withdrawn from the README at the same time.

---

## Sequence

1. **Now:** A3 decision (TCP), A4 sweep, B3 delete the patch scripts.
2. **This week:** A5 audit of the new tool surface; C3 CI matrix.
3. **Next:** C2 finish, C4 packaging, B4 freshness.
4. **After the base is trustworthy:** Track D.
