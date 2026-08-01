# How much does a wider tool menu cost? — 2026-08-01

**Answer: for selection accuracy, between 5 and 12 tools, nothing measurable.**
The default that shipped this morning was set on the opposite assumption, and it
moves as a result.

Register item 20 / P2-2. Reproduce with:

```sh
export CODETERMINAL_API_BASE=... CODETERMINAL_API_KEY=...
go test -tags eval -run TestToolMenuSizeCurve -v -timeout 60m ./daemon
```

---

## The result

`deepseek/deepseek-v4-flash` (the `primary` tier), production ZDR routing,
7 prompts × 5 trials at each size. 105 completed trials, **zero** transport
errors and **zero** request timeouts.

| menu size | selection accuracy | n | no-call | timeouts | tool JSON per request |
|---|---|---|---|---|---|
| 5 | **88.6%** (31/35) | 35 | 0 | 0 | 1,626 B |
| 8 | **85.7%** (30/35) | 35 | 0 | 0 | 2,414 B |
| 12 | **85.7%** (30/35) | 35 | 0 | 0 | 3,445 B |

The spread across the whole range is **one trial**. At n=35 that is noise, not a
curve.

### The failures are all one prompt, and it is not a menu-size problem

Every single error at every size is the same confusion:

```
menu= 5: run_tests -> list_directory   x4
menu= 8: run_tests -> list_directory   x5
menu=12: run_tests -> list_directory   x5
```

"Please run the editapply test suite." resolves to `list_directory` almost every
time, at every menu size, including the one where only five tools are offered.

**Excluding that one prompt, accuracy is 30/30 — 100% — at 5, at 8 and at 12.**

That reframes the finding entirely. There is no menu-size effect to measure here;
there is one tool whose description does not win against `list_directory` for a
prompt containing the word "suite". That is a tool-description defect, and it
would be just as present on a two-tool menu.

---

## What this does to the default

`defaultMaxAdvertisedTools` moved 12 → 5 earlier the same day. The comment
justifying it said, accurately at the time:

> selection accuracy falling from 100% with one tool on the menu to 85.7% with
> FIVE. […] What happens at 12 is not known — no run has ever been done there.

A run has now been done there, and 12 measures the same as 5. **The stated basis
for the number no longer holds, so the number moves back to 12**, which is where
it was before a reasonable inference from incomplete data moved it.

### What still justifies having a cap at all

Not accuracy — token cost, and it is the one dimension this measurement does not
cover:

- Every advertised tool's full JSON schema is sent on **every request of every
  iteration**. 12 tools is 3,445 bytes against 5 tools' 1,626 — **2.1× the tool
  payload**, on every iteration of every agent turn, paid by the user.
- Wire bytes are not tokens, and the ratio between them is not measured here. The
  direction is not in doubt; the magnitude is.

So the cap stays, and `max_advertised_tools` stays a config knob. What changes is
that its default is no longer defended by an accuracy claim the data does not
support.

---

## Two corrections to this measurement's own first run

Recorded because a measurement whose failures are hidden is worth less than no
measurement.

**1. The first run's 71.4% at menu=4 was a harness bug, not a model result.**
`menuOfSize(4)` truncated the five real tools to four, which silently dropped
`git_log` — so the "history" prompt had no correct answer available and five of
its thirty-five trials were unwinnable by any model. Size 4 is now rejected by
construction: a menu smaller than the number of distinct tools the prompts need
is not a smaller menu, it is a different experiment.

**2. The first run's "12-tool menus time out" signal did not reproduce.** That run
lost 13 of 35 trials at menu=12 to the 90-second request timeout while losing none
at 4, 5 or 8, which looked like a latency cost of a wide menu. The clean re-run
recorded **zero** timeouts at every size, including 12. The first run's timeouts
were network conditions, not menu size, and the earlier reading of them was wrong.

The guard that refused to report the biased run is the reason both corrections
exist: it failed the run rather than publishing 90.9% from 22 surviving trials.

---

## What was NOT measured

- **Tokens, as opposed to wire bytes.** See above.
- **Menus above 12.** `max_advertised_tools` still accepts up to 64; nothing
  between 13 and 64 has been run.
- **Real MCP tools.** The distractors are plausible hand-written tools, not tools
  a real server advertises. Real ones are often more verbose and less distinct,
  which would if anything make a wide menu worse than measured here.
- **Argument quality.** Unchanged from `TOOLCALL_RELIABILITY_2026-07-31.md`;
  selection is the only thing menu size could plausibly affect.

## The follow-up this actually surfaced

`run_tests` loses to `list_directory` on "run the editapply test suite", 14 times
out of 15 across three independent menu sizes. That is the single largest
correctable error in this data and it has nothing to do with menu size. Fixing it
means rewording the tool's description; verifying the fix means re-running this
eval, which now exists.
