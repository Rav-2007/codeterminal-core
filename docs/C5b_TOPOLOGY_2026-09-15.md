# C5b — remote topology, orphan branches, and the reach allowlist

<!-- coderefs: enforced -->

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `b621c06` |
| **Tree** | clean |
| **vs canonical `main`** | **246 ahead, 0 behind** — FF-AVAILABLE |
| **Latest `build`** | `34739694099`, commit `1013e1b`, **success**, remote **`Rav-2007/codeterminal-core`** |
| **Latest `gates`** | `34739694106`, commit `1013e1b`, **success**, same remote |

> **No CI has run after `1013e1b`.** Everything **(M)** below was measured locally.

---

## The headline: there are three `main`s, and the one CI watches is the oldest

```
git remote -v
origin    git@github.com:Rav-i24/Mochiii.git             (a fork)
upstream  https://github.com/Rav-2007/codeterminal-core  (where CI runs)
```

| Ref | SHA | Date | vs canonical `main` | |
|---|---|---|---|---|
| `upstream/main` — **canonical, CI runs here** | `efc611d` | **2026-08-12** | — | **(M)** |
| `main` — local | `e881bbd` | 2026-08-30 | **40 ahead** | **(M)** |
| `origin/main` — **the fork** | `4b8c53e` | 2026-09-02 | **79 ahead, 0 behind** | **(M)** |

**The fork's `main` is 79 commits ahead of the repository CI actually builds**, and it has been for
weeks. Local `main` sits between them: 40 ahead of canonical, 39 behind the fork. **The word "main"
resolves to three different commits depending on who says it**, which is an M5 collision in the most
load-bearing noun in the project.

**Eight branches track the fork. Three have no upstream at all.** Eleven branches, and **not one
tracks the repository CI runs on** **(M)**.

## The branch table

| Branch | Tracks | Ahead of canonical | Behind | Contained in HEAD? |
|---|---|---|---|---|
| `audit/adversarial-pass` | `origin/…` (fork) | 246 | 0 | — (is HEAD) |
| `main` | `origin/main` (fork) | **40** | 0 | **yes** |
| `ci/main-lint-pin` | **none** | 1 | 0 | **NO — the only one** |
| `feat/canonical-language-table` | **none** | 28 | 0 | **yes** |
| `feat/edit-payload-ingestion` | **none** | 27 | 0 | **yes** |
| `feat/web-grounding` | `origin/…` (fork) | 24 | 0 | **yes** |
| `fix/ci-limiter-probe-and-interrupt-race` | `origin/…` (fork) | 74 | 0 | **yes** |
| `sec/untrusted-text-channels` | `origin/…` (fork) | 79 | 0 | **yes** |
| `ci/cross-go-test` | `origin/…` (fork) | 0 | 51 | **yes** |
| `docs/readme-rewrite` | `origin/…` (fork) | 0 | 48 | **yes** |
| `security/ultra-vuln-pass-2026-08-06` | `origin/…` (fork) | 0 | 101 | **yes** |

**Ten of eleven branches are ancestors of HEAD** **(M)**. The merge delivers all of them.
`ci/main-lint-pin` is the single exception, and that is `ca96966`, held by decision.

---

## Part 1b — attribution, and what the topology does NOT explain

H9 applies: check, do not infer. **The topology mechanically explains one instance directly. It is a
contributing condition for the class, and it explains none of the other six on its own.**

| # | Instance | Does the topology explain it? |
|---|---|---|
| 1 | a memo committed with nobody told | **No.** Telling a person is not a push target |
| 2 | a checkpoint written and never committed | **No.** Never entered git at all |
| 3 | `142e57d` never merged | **No.** It was pushed. *Merging* is a different act from *pushing*, and the omission was the merge |
| 4 | `ca96966` verified and unpushed | **No.** Its branch has no upstream at all — it was never pushed anywhere, not pushed to the wrong place |
| 5 | **40 commits on a stale local ref** | **YES, directly.** That ref is local `main`, 40 ahead of canonical and 39 behind the fork it tracks **(M)**. The stale ref is stale *because* "main" means three things |
| 6 | the eval fix never delivered | **No.** Same shape as 3 — a merge omission |
| 7 | `0925a3d` not on `main` | **No.** Same shape as 3 |

**The honest statement, which is weaker than the one the chunk offered and is the one the evidence
supports:** the topology is *not* the mechanism behind most of the delivery gaps. What it does is
make the word "pushed" ambiguous — and an ambiguous success condition is what lets a merge omission
*look* like a completed delivery. **It is a confusion amplifier, not a cause.**

It *is* the direct cause of one thing the chunk named: `gh` resolving to the fork, trap **T1**.

## Part 1c — the recommended remote rename, and what it costs

**Owner action. Not executed.**

```bash
git remote rename origin fork
git remote rename upstream origin       # origin now = Rav-2007/codeterminal-core
git fetch --all
git branch -u origin/main main
# and -u for each branch that should track the canonical remote
```

### What breaks — measured, not estimated

```
git grep -nE '\borigin\b' -- scripts/ .github/ Makefile
(no output)
```

**Nothing in `scripts/`, `.github/` or the `Makefile` is keyed to the remote name `origin`** **(M)**.
The single remote-name dependency in the tree was `scripts/reach.sh`'s own default, and **that has
been removed in this chunk**: it now resolves its canonical ref from the refs that exist, preferring
`upstream/main` then `origin/main`, and names the one it used. The rename is safe in both directions.

### Consequences to state before running it

1. **`gh` starts resolving to the canonical repository**, which retires trap T1.
2. **`origin` changes meaning** for every habit, note and muscle-memory command in the project. The
   fork does not disappear; it becomes `fork`.
3. **The `--repo Rav-2007/codeterminal-core` rule stays in force regardless.** Both remotes carry
   identically-named workflows, so a `gh run list` without `--repo` is still ambiguous to a reader
   even when it is no longer ambiguous to the tool. **A habit that depends on configuration is not a
   control.**
4. **The fork's 79-commit `main` does not move.** Renaming remotes changes names, not history. What
   is on the fork's `main` and not on the canonical one is a separate question — and every one of
   those 79 commits is already an ancestor of HEAD **(M)**, so the merge delivers them.

---

## Part 2 — the orphans are stale pointers, not lost work

Both were reported by the gate's first version as *"487 commit(s)"* and *"486 commit(s)"*, which
reads as half a year of undelivered work. **It is nothing of the kind, and the number was my gate's
error, not a finding.**

| Branch | Total on ref | Ahead of canonical | **Unreachable from HEAD** | Unique patches (`git cherry`) | Verdict |
|---|---|---|---|---|---|
| `feat/canonical-language-table` | 487 | 28 | **0** | **0** | **SAFE TO DISCARD** |
| `feat/edit-payload-ingestion` | 486 | 27 | **0** | **0** | **SAFE TO DISCARD** |

All **(M)**. Both are ancestors of HEAD: every commit on them is already reachable, and `git cherry`
finds no patch on either that is not already applied. They are pointers into HEAD's own history — the
same shape local `main` has, and exactly what the chunk called "a stale pointer".

**There is no eighth delivery gap.** The chunk anticipated that a 487-commit branch with unique
patches "would be an eighth delivery gap and the largest yet". It has none.

**The gate's message has been corrected** to report commits unreachable from HEAD rather than the
total on the ref, and to say plainly when the answer is zero. A gate that makes a stale pointer look
like 487 commits of lost work will get itself ignored.

---

## Part 3 — `reach.sh` is now a blocking gate

### The allowlist requires a trigger as well as a reason

Format is now `key|trigger|reason`, **both mandatory**.

> A **reason** explains why the gap is acceptable today. A **trigger** names the event that makes it
> unacceptable. Without a trigger, an exemption written for a week-long situation becomes permanent
> by default — **which is how every stale record in this repository started.**

Thirteen entries cover the ten findings. Most triggers are *"the remote rename"* or *"the merge
lands"*, so **the gate self-clears**: it goes red the moment either happens and the entry has not been
removed.

### Allowlisted is not invisible — and it was, until this chunk

The chunk's acceptance test was: *"an allowlisted entry must still be detected and reported, just not
fatal. If allowlisting silences detection rather than the exit code, that is a defect in the gate."*

**It was a defect, and the test found it.** Commits covered by an allowlisted branch were `continue`d
silently, so **53 undelivered pipeline commits and 165 undelivered document citations vanished from
the output entirely** **(M)**. The exit status was right and the report was a lie by omission.

Now, every run prints:

```
reach: COVERED 54 pipeline commit(s) and 165 document citation(s) are undelivered,
reach:         accounted for by the allowlisted branch(es): audit/adversarial-pass
reach:         They are DETECTED, not silenced. When that branch's entry retires, these become failures.
reach: ALLOWED ca96966 -- retires when: the merge lands, or the owner reverses the hold
...
reach: 13 exemption(s) above are DETECTED and not fatal. Each names the event that retires it.
```

Summarised rather than listed, for the same reason the design changed at 213 failures: **one line per
commit is noise, and zero lines per commit is amnesia.** One line per *exemption*, not per hit — a sha
cited by four documents is one decision.

### Placement, resolved

`gate-parity` refused to let this be skipped when the script was added. It was `manual` for exactly
one commit, with the tension recorded rather than hidden. It is now **`local` and blocking in
`make check`** **(M)** — `make reach` exits 0.

**Local and not CI, and the reason is not cost.** A CI runner's clone has no local branches, so checks
1 and 2 would examine nothing and **pass by being irrelevant** — the exact failure `hookcheck`'s
exclusion names. The person holding an undelivered fix is at a terminal.

### Neuter — 6/6, from a committed tree

`git status --porcelain` empty before the first arm (H8, now mechanical).

| # | Arm | Result | |
|---|---|---|---|
| — | baseline | **exit 0**, 249 items | **(M)** |
| 1 | branch ahead of upstream | **FAIL** — *"is 246 commit(s) ahead"* | **(M)** |
| 2 | branch with no upstream | **FAIL** — and correctly says its commits are reachable from HEAD | **(M)** |
| 3 | branch entry removed | **FAIL** ×48 pipeline commits, `142e57d` and `0925a3d` **by name** | **(M)** |
| 4 | document citing a commit absent from `main` | **FAIL** — *"A reader following that citation reaches nothing"* | **(M)** |
| 5 | entry with **no trigger** | **FAIL** — *"An exemption with no event that retires it becomes permanent by default"* | **(M)** |
| 6 | entry with **no reason** | **FAIL** | **(M)** |
| — | restored | **exit 0**, tree clean, no probe branches | **(M)** |

### The replay, with the allowlist active

| # | Instance | Still detected? |
|---|---|---|
| 1 | a memo, nobody told | **No** — out of scope, and the banner says so |
| 2 | a checkpoint never committed | **No** — never entered git |
| 3 | `142e57d` | **Yes** — inside `COVERED 54`; by name the moment the branch entry retires |
| 4 | `ca96966` | **Yes** — `ALLOWED ca96966`, with its trigger |
| 5 | 40 commits on a stale local ref | **Yes** — `ALLOWED audit/adversarial-pass` and `remote:main` |
| 6 | the eval fix | **Yes** — same covering entry |
| 7 | `0925a3d` | **Yes** — inside `COVERED 54`; by name when the entry retires |

**Five of seven, and the allowlist did not blind it.** The two it misses are structural and named in
the banner rather than papered over.

---

## Premises that did not hold

| Premise | Outcome |
|---|---|
| "seven branches, including `main`, tracking `origin`" | **Eight (M)**, plus three with no upstream. My own earlier count was read off a pre-fix run of the gate |
| "a 487-commit branch with unique patches would be an eighth delivery gap" | **No unique patches on either orphan.** There is no eighth gap |
| "this is plausibly the mechanism under several of the seven instances" | **It explains one directly.** It is a confusion amplifier, not a cause — see Part 1b |
| "most triggers will read *closed by the merge*" | **Most read *the remote rename*.** Eight of thirteen entries |

## Self-corrections

1. **I reported "seven branches track the fork". It is eight** — the eighth is
   `audit/adversarial-pass` itself, which my earlier reading had already separated out for a
   different reason and then lost from the total.
2. **My own gate's headline number was misleading.** *"487 commit(s) has NO upstream"* was the total
   on the ref. The number that matters is commits unreachable from elsewhere, which is **zero**.
   Fixed in the gate, not just in the prose — H2 applied to output I wrote myself.

## Verification

| Check | Result | |
|---|---|---|
| `make reach` | **exit 0** — 249 items, 13 exemptions, all with triggers | **(M)** |
| `./scripts/gate-parity.sh` | exit 0 — 23 scripts, `reach.sh` now `local` | **(M)** |
| `./scripts/docs-coderefs.sh` | exit 0 | **(M)** |
| `bash -n` on every changed script | clean | **(M)** |
| working tree | clean | **(M)** |

## For the owner

1. **Run the remote rename** (Part 1c), or say it stays as-is — eight allowlist entries name it as
   their trigger and will not retire until it happens.
2. **Delete the two orphan branches**, or repoint them. Measured safe: zero unique patches on either.
3. **`ca96966` stays held** — unchanged, and now carries an explicit retiring event.
