# C0 — Ground truth, delivery, and the doc corpus

**Written 2026-09-14.** No product code touched. **Zero commits made** — both commits C0
anticipated turn out to be unmakeable or unnecessary, for different reasons. §2 and §3 say why.

**Audience:** the repo owner, who holds the one input this chunk is blocked on.

---

## 1. Ground truth

```
BRANCH:            audit/adversarial-pass
HEAD:              1013e1b690d24e128173daab307699f4a7386cec
                   "test(daemon/mcp): the item 2 wiring test was green because it won a race"
TREE:              dirty — 1 untracked file, docs/NEXT_ACTIONS_2026-09-14.md
                   (the prior recon deliverable; no tracked file is modified)
LOCAL vs UPSTREAM: upstream/audit/adversarial-pass  EQUAL — 0 ahead, 0 behind
                   origin/audit/adversarial-pass    AHEAD 16 — the fork has not received them
                   git's configured tracking branch is origin, the fork. CI runs on upstream.
LATEST build:      run 34739694099 @ 1013e1b — success  on Rav-2007/codeterminal-core
LATEST gates:      run 34739694106 @ 1013e1b — success  on Rav-2007/codeterminal-core
BUILD AT HEAD:     YES — 34739694099, 26 real jobs all success, one job
                   "retrieval eval (scheduled)" skipped by design. Skipped is not passed.
```

All `(M)`, measured this session. HEAD has not moved since the recon pass. **C1 must still run
before this green is treated as evidence** — see §6.

---

## 2. Step 2 — the undelivered document: BLOCKED, and the block is the finding

### 2.1 The three facts

| # | Fact as stated | Verdict | Evidence |
|---|---|---|---|
| 1 | "`1e48e3c` is cited in it as the commit for three decision-table rows, and exists here" | **PARTIAL** | `git cat-file -t 1e48e3c` → `commit`; it exists and is *"docs: five verdicts recorded, and the role that was never assigned"*. But `git grep -n '1e48e3c' -- '*.md'` returns **zero hits**: nothing in this repository cites it at all. `(M)` |
| 2 | "12 of 12 of its code anchors resolved at HEAD" | **MATCH** | Established in the recon pass; all twelve land on role-consistent code, one off-by-one (`server.go:707` is blank, statement at `:708`). `(M)` |
| 3 | "`9fae051` modified the vanished-floor sweep" | **MATCH** | `9fae051` = *"ci: gate the two gate lists against each other, and make the sweep work in CI"*, touches `scripts/coverage-ratchet.sh` (+39/−8), and the diff adds `belongs_to_module()` plus both unconditional `FAIL … was never measured` / `… was never seen` paths. `(M)` |

### 2.2 Fact 1's second clause is circular, and that matters

The clause "is cited **in it**" can only be checked by reading the document. The document is absent.
So the clause supplies no independent evidence — its only witness is the thing in question. Facts 2
and 3 are genuinely independent and both hold.

### 2.3 What the three facts do and do not establish

They establish, firmly, that **the work described landed in this repository**. `1e48e3c` and
`9fae051` exist and do what is claimed of them, and the anchor map resolves.

They do **not** establish that a document describing that work exists, and they cannot. "The work
landed" and "a record of the work was written" are two states, and C0's inference collapses them.
The recon pass's finding was narrower and survives: the *file* `PROJECT_CHECKPOINT_2026-09-13.md`
has never existed in this repository, on any ref, in any commit. Re-verified this session — the
searches are in `docs/NEXT_ACTIONS_2026-09-14.md` §2.1.

A likelier account, which fits every fact without needing a lost file: whoever wrote the tasking
prompt had accurate knowledge of that work — because they or their session had done it — and wrote
the claims directly into the tasking. Accurate claims prove accurate knowledge. They do not prove
an artefact.

### 2.4 Why I did not commit anything, and what I need

**`git add docs/PROJECT_CHECKPOINT_2026-09-13.md` cannot run: there is no such file, and none was
provided to this session.** C0 anticipates this — *"the file will be provided to you, or is at the
path the user names"* — and neither happened.

The only way to produce that path is to write its contents. I will not do that, and the reason is
this project's own subject matter. Reconstructing a checkpoint from the recon pass's findings and
committing it under the date 2026-09-13, as a contemporaneous record by whoever did the 09-13 work,
would be manufacturing a historical record. In a repository whose entire documented history is
about claims outrunning the evidence for them, a fabricated dated audit record is the single worst
artefact I could add — and unlike every other defect here, it would be undetectable by design,
because it would be internally consistent with everything around it.

**What unblocks this, in order of preference:**

1. **Paste or place the file.** If it exists in a prior session's transcript, a scratch directory,
   or outside the repo, name the path and I will read it, verify its claims against HEAD, and commit
   it with the message C0 specifies. This is the outcome C0 is written for.
2. **Say it does not exist.** Then the honest deliverable is not a checkpoint — it is a statement
   that the 09-13 work is recorded only in commit messages and the four documents dated 2026-09-13
   (`RESIDUAL_RISKS.md`, `DECISION_MEMO_2026-09-04.md`, `BRANCH_STATUS_2026-09-05.md`,
   `ADVERSARIAL_PASS_2026-09-09.md`, all with retrofitted "RESOLVED 2026-09-13" headers). Those are
   real, committed, and already carry the verdicts. On that reading **nothing is undelivered** and
   C0's premise dissolves; the addendum C6 wants can cite them directly.
3. **Authorise a clearly-labelled reconstruction.** If you want one, it must be dated 2026-09-14,
   titled as a reconstruction, and state in its first line that it was assembled from repository
   evidence and not written on 09-13. I will write that on request. I will not write it unlabelled.

Note for whichever path you pick: a markdown-only commit runs `gates` and **no** `build`
(`build.yml:43`, `paths-ignore: ["**.md"]`). That is not a pass, and I would say so in the commit
report rather than let it be inferred.

---

## 3. Step 3 — the doc corpus: no conflict, and nothing to fix

### 3.1 The premise was wrong: both registers are real

C0 frames `RESIDUAL_RISKS.md` and `OPEN_ITEMS` as competing names for one register. They are two
different documents and both exist at HEAD. `(M)`

| Name cited | Exists at HEAD? | Lines | What it is |
|---|---|---|---|
| `docs/RESIDUAL_RISKS.md` | **YES** | 1771 | the risk register, rows R1.1–R1.28 |
| `docs/OPEN_ITEMS.md` | **YES** | 475 | a separate defect register, numbered items; 8 marked open |
| `docs/DECISION_MEMO_2026-09-04.md` | **YES** | 561 | the five items, all answered 2026-09-13 |
| `docs/HANDBACK_2026-09-12.md` | **YES** | 380 | handback, 10 not-verified rows |
| `docs/BRANCH_STATUS_2026-09-05.md` | **YES** | 1055 | branch status, gate table |
| `docs/MANUAL_SESSION_2026-09-04.md` | **YES** | 281 | the scripted manual session, still unassigned |

The recon pass reported on both registers separately and did not conflate them. There is no
renaming to undo and no `REAL NAME` column to fill: every cited name is its own real name.

### 3.2 Dangling document citations: zero

`git log --diff-filter=D --name-only -- docs/` shows exactly **one** deletion ever:
`docs/DECISION_MEMO_REDACTION_2026-09-04.md`, removed at `ec21d8c`. `git grep -n
'DECISION_MEMO_REDACTION'` over `*.md`, `*.go`, `*.sh` → **zero hits**. Nothing cites it. `(M)`
`git log --diff-filter=R -- docs/` → **no renames ever**. `(M)`

I extracted all 139 distinct `*.md` paths cited anywhere in tracked `.md`/`.go`/`.sh`/`.yml` and
resolved each against the tree. Every apparent dangler is benign, and I checked each individually
rather than by category:

- **Test fixtures and prose examples, not citations** — `CHANGELOG.md`
  (`editapply/editblock_separator_test.go:167`), `conference/talk.md`
  (`editapply/pathhazard_test.go:110`, a reserved-name case), `docs/notes.md` and `notes.md`
  (`daemon/apply_cmd.go:23-24`, a comment illustrating a Windows path bug;
  `daemon/create_end_to_end_test.go:50`), `NOTES.md` (`daemon/repomap_test.go:592`),
  `implementation_plan.md` (`daemon/planmode.go:182`, inside a prompt string).
- **A template placeholder, deliberately not a file** — `docs/ENTERPRISE_QA_REPORT_YYYY-MM-DD.md`
  (`docs/ENTERPRISE_QA_MASTER.md:93`, `.cursor/skills/.../SKILL.md:29`).
- **Correct pointers to files outside the repo** — `MEMORY.md`, `p3-security-review.md`,
  `~/.claude/plans/*.md` (`docs/ARCHIVE/BACKLOG_2026-07.md:785-786,1539`,
  `docs/HANDOFF.md:276,288`). `HANDOFF.md:288` is explicitly self-aware about being an external
  pointer.
- The `tmp/*.md` paths are `docs-claims.sh --self-test` scaffolding; the `-docs/…` entries were my
  extractor mis-splitting shell flags. Both are artefacts of my method, not of the repo.

**So Step 3 produces no commit.** Recorded here with equal weight to a finding, per the standing
rules: a clean corpus is a result, and the next agent should not re-derive it.

This also closes an open `(U)` from the recon pass (§5.4 there): the five `.md` basenames cited from
Go with no tracked counterpart are all fixtures or prose. `(M)`

### 3.3 Gate coverage on the files I would have corrected

Moot, since there is nothing to correct — but worth recording because it is the mechanism behind a
queued row: `scripts/docs-coderefs.sh:46` globs `docs/*.md docs/**/*.md *.md` with `globstar`
**unset** (only `nullglob` at `:45`), so `**` collapses to one level. `docs/ARCHIVE/` is reached;
`daemon/*.md` and `proxy/*.md` are **not reachable by any doc gate**. It is also opt-in by marker
(`:51`, `<!-- coderefs: enforced -->`), so 6 docs are enforced and 15 in-glob docs with 162 code
references are scanned-but-unchecked. `(M)`

---

## 4. Step 4 — the boundary count: three taxonomies, not two

I did not pick a number. The relationship resolves further than C0 expected, and worse.

**Taxonomy A — crossings.** `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md:56-73`, an ASCII diagram.
Four items: 1 unix socket / SO_PEERCRED · 2 editapply five-gate pipeline · 3 bwrap/docker/NONE ·
4 managed proxy. These are **places data crosses a privilege line**.

**Taxonomy B — transitions.** *The same document*, `:91-104`, headed "Trust boundaries — where
untrusted becomes privileged". Four items: 1 model output → tool dispatch · 2 untrusted file
content → model context · 3 Lane B server → model context · 4 human consent decision. These are
**semantic trust transitions**, each with a verdict.

A and B are both labelled "TRUST BOUNDARY 1–4" and their contents do not correspond — B's #1 is not
A's #1. **One document, two incompatible four-item numberings under one phrase.** That is the M5
error inside the document that would otherwise be the authority, and it is enough on its own to
explain how different counts circulate without anyone being careless: different people counted
different things and all said "trust boundary".

That document is also dated **2026-08-26** against **`efc611d` on `main`** — which is exactly
`upstream/main`, 213 commits behind HEAD. So neither A nor B is a live enumeration of this branch.

**Taxonomy C — the working one, and the one the tasking uses.** Four current documents number
boundaries past four and identify them: `docs/HANDBACK_2026-09-12.md:91` — *"Boundaries 6 and 7
(MCP and LSP)"*; also `docs/ADVERSARIAL_PASS_2026-09-09.md:148,220,372,452,637`,
`docs/RESIDUAL_RISKS.md:1241`, `docs/DECISION_MEMO_2026-09-04.md:498`. So a numbering reaching at
least **7** is in active use, with **6 = MCP** and **7 = LSP**, consistent across four documents.

**But taxonomy C is never enumerated.** `git grep -n -iE '^\|? *\**boundary (1|…|13)\**' -- '*.md'`
returns **zero** — no document lists its members. It is used by number and defined nowhere. `(M)`

**Verdict.** The in-repo "four" is **a different taxonomy, twice over** — not a subset of thirteen,
not stale-and-superseded, but an older audit of a different commit using two different partitions.
The tasking's "thirteen" is the tail of taxonomy C, which is real and in daily use but exists only
as numbers in prose.

**`(U)` — what the thirteen enumerate.** What would settle it: the author of
`docs/ADVERSARIAL_PASS_2026-09-09.md`'s boundary-6/7 reconnaissance had a full numbering in hand
when they wrote "6 and 7". Ask them for the list, or accept that taxonomy C needs enumerating once
and gate it thereafter. **Do not count them fresh** — a fourth incompatible partition is worse than
an unenumerated third one. Size: 30 minutes to write down, given the person; half a day without.

The known "eleven" is a third figure the tasking already flags as a conflation. I found no in-repo
instance of "eleven boundaries" to propagate or correct.

---

## 5. What I did not verify

Longer than the findings above, deliberately.

1. **I did not read `PROJECT_CHECKPOINT_2026-09-13.md`.** It does not exist. Every claim about its
   contents in this document is a claim about the *tasking prompt's* restatement of them, tagged
   `(C)`.
2. **Fact 2 is carried, not re-measured.** The 12/12 anchor resolution is from the recon pass. I
   re-verified none of the twelve this session.
3. **I did not establish who wrote the 09-13 work, or whether they kept notes outside the repo.**
   That is the one question that decides between §2.4's options 1 and 2, and only a person can
   answer it.
4. **I did not read the four 2026-09-13 documents in full** to confirm they jointly cover what a
   checkpoint would. I read their status lines and the recon pass's extraction of them. Before
   option 2 is chosen, someone should confirm the coverage is actually complete — I am asserting
   only that they exist and carry verdicts.
5. **My 139-citation sweep matched `[A-Za-z0-9_/.-]+\.md` in tracked `.md`/`.go`/`.sh`/`.yml`.** It
   would miss a document referenced by title rather than filename, by a `.ts`/`.sql`/JSON source, or
   with a space in its name. The "zero dangling citations" result is bounded by that.
6. **I did not check whether the six registers' *contents* agree with each other** — only that
   their names resolve. The recon pass found one stale-OPEN row (`OPEN_ITEMS` item 33) and one
   stale pin identifier (`RESIDUAL_RISKS.md:1513`), so they demonstrably do not fully agree. Those
   are queued, not fixed here.
7. **I ran no tests, no gates, and no CI.** `gate-parity.sh`'s clean result is carried from the
   recon pass `(C)`. No `gh run rerun`, no `workflow_dispatch`.
8. **I did not determine whether taxonomies A and B were intended as one list or two.** They may be
   a deliberate two-view presentation whose shared numbering is an editing slip, or two independent
   attempts. Reading that document's revision history would tell; I did not.
9. **Chains traced to the end:** the three-fact verification (each to a commit object and its diff);
   the citation sweep (every apparent dangler to its citing line). **Chains not traced:** what
   `renderToolResult` at `daemon/agentloop.go:838` actually does — cited by
   `ADVERSARIAL_PASS_2026-09-09.md:151` as the single choke point for boundaries 6 and 7, which
   makes it load-bearing for taxonomy C's shape, and I did not read it.

### New M5 pairs from this chunk

- `the work landed` ≠ `a record of the work was written` — §2.3, the collapse C0 makes
- `written` ≠ `committed` ≠ `delivered` — three states, and this project has now hit all three
- `cited by number` ≠ `defined anywhere` — taxonomy C
- `a different taxonomy` ≠ `a stale enumeration` — §4; the first needs reconciling, the second
  needs deleting, and treating one as the other loses information either way

### Self-corrections

1. **I began §3 expecting to find and fix stale document names**, because C0 frames the corpus as
   having a naming conflict. Wrong evidence: the recon pass mentioned `OPEN_ITEMS` prominently
   alongside `RESIDUAL_RISKS.md`, which reads like a substitution if you have not seen both files.
   Both exist and are different documents. **No conflict, no commit.** Withdrawn before it reached
   a commit, but it shaped my first two searches.
2. **My first citation sweep reported 32 dangling references.** Wrong evidence: my extractor split
   shell flags (`--docs/README.md` → `-docs/README.md`) and counted `docs-claims.sh`'s own
   `tmp/*.md` self-test fixtures as repository citations. The real count of dangling
   *document* citations is **zero**. The 32 was an artefact of my method, and I nearly filed it.

---

## 6. Sequencing consequence for C1–C6

- **C1 is unblocked and should run next.** Nothing here depends on it, and everything after it does:
  until C1 explains the two unexplained FAIL→PASS transitions, the green at §1 is not evidence.
- **C2, C3, C4 are unblocked** and independent of §2's blocker. C4 row 2 can drop its
  re-verification of the backups cap — the recon pass measured it (`backupSessionsToKeep = 5`,
  `pruneBackupSessions` on the write path at `editapply/backup.go:59`) — but should still run the
  two ratchet invocations C4 row 3 asks for, which no session has yet executed.
- **C5 and C6 both depend on §2 being resolved.** C6 Step 3 is written as "append an addendum to
  the 2026-09-13 checkpoint". If option 2 in §2.4 is chosen, there is no checkpoint to append to
  and C6 Step 3 needs rewriting to cite the four dated documents instead. **Do not let C6 invent the
  parent document it expects to find.**
