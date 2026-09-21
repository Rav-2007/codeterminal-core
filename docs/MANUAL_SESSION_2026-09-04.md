# Manual session — terminal client

**What this is.** A request for one person to sit down with the terminal client
for about forty minutes and tell us what they noticed.

**What it is not.** A checklist. Every step below already has a passing automated
test behind it. If the tests were the answer, this document would not exist.

---

## Why you, and not the test suite

The suite is large — 327 tests on Linux, including a real binary driven through a
real pseudo-terminal — and it is green. It has been green through defects that a
person would have found in ninety seconds. Here is the one that makes the point.

**`refreshViewport` used to end in an unconditional `GotoBottom()`.** It runs on
every streamed token. So scrolling back through an answer while it was still
arriving was not awkward, it was **impossible**: eight wheel-ups would reach
`YOffset` 187, the next token would arrive a few milliseconds later, and the view
would snap to the bottom. The text you were reading was gone before you finished
the line.

No test caught it. Every scroll test scrolled a *finished* transcript, and every
streaming test checked what the model contained rather than where the viewport
was. **No test scrolled during a stream, because nobody had thought to.** A
person hits it the first time they try to re-read something the model said thirty
seconds ago.

That is the class of thing being asked for. Not "does the feature work" — the
tests answer that — but "what happens when a person uses it like a person."

**"It worked" is a less useful report than "this felt off."** If something
flickered, jumped, felt slow, drew twice, or simply made you hesitate, write it
down even if you cannot say why. Especially then. *"I don't know, the screen did
something"* is a real report and we can chase it from there; a green checklist
tells us nothing we did not already have.

---

## Setup

From the repository root:

```bash
(cd helper      && go build -o mochiii-embedder-helper .)
(cd daemon      && go build -o mochiii-daemon .)
(cd clients/tui && go build -o mochiii-tui .)

./daemon/mochiii-daemon download-model     # once per machine
./daemon/mochiii-daemon index .            # once per repo
```

**You need an inference key before any of this answers anything.** Two paths,
and either is fine — ask whoever sent you this document for whichever they have:

```bash
# Direct mode: your own provider key, straight from this machine.
export MOCHIII_API_BASE="https://api.together.xyz/v1"
export MOCHIII_API_KEY="sk-..."

./daemon/mochiii-daemon --workspace .          # terminal 1
./clients/tui/mochiii-tui                      # terminal 2
```

```bash
# Managed proxy mode: one Mochiii key, provider key held server-side.
cp .env.example .env && $EDITOR .env    # set MOCHIII_PROXY_KEY=mochi_...

./run-proxy.sh --workspace .                        # terminal 1
./clients/tui/mochiii-tui                      # terminal 2
```

Which one you use does not matter for anything in this document — every step
below is about the screen, not the model.

**If the daemon exits immediately**, it is almost always a missing key or a
missing `models.json`; the daemon prints the reason and `--log-file <path>`
captures it.

**Make the ceiling reachable in minutes rather than hours.** The transcript
bound defaults to 500 turns / 2 MiB, which is a long afternoon. Start the client
with a small one instead:

```bash
MOCHIII_MAX_TURNS=30 ./clients/tui/mochiii-tui
```

Both `MOCHIII_MAX_TURNS` and `MOCHIII_MAX_TRANSCRIPT_BYTES` have
floors (8 turns, 64 KB), so a typo cannot switch the bound off. **Do step 5 with
the default ceiling too if you have the patience** — a 2 MiB transcript is where
the repaint is slowest, and that is a step 5 question.

---

## The session

Work through these in order. Keep a scratch file open and write as you go, not
afterwards — the things worth reporting are the ones you stop noticing after
five minutes.

### 1. A long session, past the ceiling

With `MOCHIII_MAX_TURNS=30`, ask about twenty-five questions. Anything real
— ask it about this codebase. Let each answer finish.

Somewhere past turn fifteen a line appears at the top of the transcript saying
how many earlier turns were dropped and how many bytes went.

- Did you notice it appear, or only find it later when you scrolled up?
- Read it as a user, not as a tester. Does it read like *the product bounding
  itself*, or like *the product losing your conversation*?
- Scroll to the very top. Is it obvious that something is above the marker and
  gone, versus the conversation simply having started there?

### 2. Scroll back mid-stream, then keep streaming

Ask something that produces a long answer. **While the tokens are still
arriving**, scroll up — mouse wheel, `PgUp`, arrow keys, whatever you would
reach for.

- Does the text you scrolled to stay still, or drift?
- Now scroll back to the bottom. Does it start following the stream again?
- Scroll up and stay there until the answer finishes. What happens at the end?
- Try scrolling up *while typing your next question*.

This is the GotoBottom area. It is fixed and it is tested. Look anyway — the fix
made following conditional on being at the bottom, and "at the bottom" is a
judgement the code makes on your behalf several times a second.

### 3. A large paste into a deep transcript

Still in the long session from step 1 — this matters, do not restart. Paste
something very large into the prompt box: a whole source file, ideally
approaching a megabyte.

```bash
# a ~1 MB blob to paste, if you need one
yes "pasted source line with unicode -> abcd" | head -26000 > /tmp/big.txt
```

The prompt box holds 4,000 characters. Everything past that is dropped, and a
line of chrome tells you how many characters arrived, were kept and were
dropped.

- **Did you see that line?** It is one line of status text, and it appears at the
  moment your attention is on the prompt box. This is the specific thing we
  cannot test: whether a notice is *noticed*.
- Did the client stutter, freeze, or drop a frame when you pasted?
- Try pasting **while an answer is streaming.** Note what you observe. There is
  a known behaviour here and we deliberately are not telling you what it is — we
  want to know whether it surprises you.

### 4. An approval

Ask for something that makes the model use a tool — "read `daemon/scrub.go` and
summarise it", or ask it to edit a file. An approval panel appears.

- Read it the way you would read it at 6pm on a Friday. Do you know what you are
  approving?
- If it says a tool is **unconfined**, does that read as a warning at the size
  and colour it actually renders? Or does it read as ordinary chrome?
- Which key does your hand reach for first?
- Answer it both ways across two attempts — approve one, deny one — and watch
  what the screen does after each.

### 5. The ceiling, felt rather than measured

Get the transcript to the **default** ceiling if you can (restart without
`MOCHIII_MAX_TURNS`, ~250 exchanges), or use a large
`MOCHIII_MAX_TRANSCRIPT_BYTES` and long answers.

At that size one repaint costs about 22 ms, which is over our own budget and
under the threshold most people consciously notice.

- While an answer streams into a full transcript, does typing feel laggy?
- Does the screen tear, flicker, or repaint visibly?
- Does the fan spin up?

If it feels fine, say so — that is a real data point here, because the number
says it should be marginal.

### 6. Quit, four ways

Do each of these from a **fresh session with an answer in flight**, and after
each one look hard at the shell you land back in.

| # | How | Command |
|---|---|---|
| a | Ctrl-C | press it twice, note what the first press does |
| b | SIGTERM | `pkill -TERM mochiii-tui` from another terminal |
| c | SIGHUP | `pkill -HUP mochiii-tui` |
| d | Close the terminal window | just close it, then open a new one |

After each, in the shell you get back:

- Is the cursor visible? Type something — can you see it?
- Is the text colour normal, or is everything now bold/blue/inverted?
- Does the prompt wrap correctly at the right edge?
- Run `reset`. If that visibly changed anything, the client did not clean up.
- For (d): is the process actually gone? `pgrep -a mochiii-tui`.

**(d) is a known gap and we want your observation anyway.** Closing the terminal
window in a way that destroys the pseudo-terminal outright delivers no signal,
so the client keeps running with nothing attached — about 8 MB, cleaned up with
`pkill mochiii-tui`. Tell us if you hit it, how you closed the window when
you did, and whether anything else looked wrong; the failure mode is recorded
but its edges are not.

---

## Known, deliberate, and not worth your time

Three things will look like bugs and are not. Skip past them so your attention
goes to the unknown.

1. **You need Shift to select text with the mouse.** Mouse capture is on so the
   wheel scrolls the transcript; the terminal only gets the mouse when Shift is
   held. Deliberate, and recorded in `docs/ADR-001-mouse-capture.md`. You can
   turn capture off in-session with `/mouse` — and **if you do, tell us whether
   you would have found that command on your own.** That part is not known.
2. **The transcript drops old turns at the ceiling and says so.** That is the
   bound working. What we want is your reaction to *how* it says it (step 1), not
   a report that it happened.
3. **`mochiii-tui --prompt "..."` strips control sequences from its
   stdout.** Piping it no longer preserves colour. Deliberate — an escape written
   to a file executes when someone later `cat`s it.

---

## Platforms

In priority order:

| Platform | Why | State |
|---|---|---|
| **macOS** | **Least verified, by a wide margin.** | The suite runs there, but the five pseudo-terminal test files are Linux-only, so **terminal restore is not tested on macOS at all** — step 6 is the single most valuable thing in this document if you are on a Mac. Signal *delivery* is tested there; the *restore* is not. |
| **Linux** | Best covered | Everything below is tested here, which is exactly why a human finding something here is interesting. |
| **Windows** | Different by design | No SIGHUP and no POSIX SIGTERM — the exit-signal code is a deliberate no-op. Steps 6b and 6c do not apply; 6a and 6d do. |

One person on macOS is worth more than three on Linux. If you can only do one
platform, do that one.

---

## Reporting

Anything at all, in any form. A paragraph, a list, a screen recording, a
sentence that begins "this is probably nothing, but".

Useful:

- *"Step 2, when I scrolled back the line I was reading jumped by one or two
  rows and then settled."*
- *"I pasted the file and there was a beat before anything happened."*
- *"I did not see the paste notice at all until you mentioned it."*
- *"After Ctrl-C my prompt was fine but my scrollback was full of the old
  screen."*

Less useful:

- *"All steps passed."*

If nothing felt wrong, say that too — but say it as *"I did steps 1–6 on macOS
and noticed nothing"*, so we know which platform the silence came from.

**Send it to whoever handed you this document.** There is no form and no
template. Forty minutes and a paragraph is the whole ask.

---

## Where the rest of the detail lives, if you want it

You do not need any of this to do the session. It is here so the document does
not depend on asking anyone a question.

- `docs/TUI_PRODUCTION_READINESS_2026-09-04.md` — what was measured, what was
  not, and the per-platform table behind the *Platforms* section above.
- `docs/RESIDUAL_RISKS.md` — the known-and-accepted list, including R1.1 (the
  destroyed-terminal orphan in step 6d) and R1.12 (the 22 ms repaint in step 5).
- `docs/ADR-001-mouse-capture.md` — why Shift is needed to select text.
