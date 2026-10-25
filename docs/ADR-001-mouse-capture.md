# ADR-001 — Mouse capture in the terminal client

**Status:** accepted (toggle shipped); default decided 2026-09-27 (capture ON, up/down as prompt history), **revised 2026-10-25: capture OFF from the start, up/down scroll, prompt history on ctrl+up/down** (see "Revised 2026-10-25").
**Date:** 2026-09-04.
**Scope:** `clients/tui` only. The VS Code client is a webview and has no such trade.

---

## The thing being decided

`main.go` starts Bubble Tea with `tea.WithMouseCellMotion()`. That tells the
terminal to report mouse events to the program instead of handling them itself.

It buys **one** feature: the wheel scrolls the transcript viewport
(`handleMouse`, which forwards to `viewport.Update` and nothing else).

It costs **the terminal's own click-drag text selection**. With mouse reporting
on, dragging across the transcript selects nothing; the user must hold Shift to
get the terminal's native behaviour back.

## Why it needed a decision rather than a default

The trade was made silently and is invisible until it bites. There is no error
message, no mode indicator and nothing to search for: dragging simply does
nothing. Someone who does not already know the Shift convention does not
conclude "mouse reporting is enabled". They conclude **"I cannot copy text out
of this program"**, which for a coding assistant is close to concluding the
product is broken — copying the code the model just wrote is among the most
common things anyone does with the output.

Shift-drag is a real convention, but it is not universal across terminals and
multiplexers, and it is written down nowhere the user can see.

Meanwhile the feature it buys is **already available from the keyboard**:
`pgup` and `pgdown` scroll the same viewport, and both are listed in `/help`.

## Decision

**Make it a named, discoverable, runtime toggle.** Shipped:

- `/mouse` toggles capture and reports the new state in the transcript, in terms
  of what it means for the user ("select and copy with the mouse as usual" /
  "the wheel scrolls; hold shift to select") rather than by naming a terminal mode.
- The idle hint carries `/mouse to select text` — the symptom, not the mechanism,
  because the symptom is what someone will be looking for.
- `/help` lists it, and the entry contains the words "select text".
- It is recorded as deliberately TUI-only in the client-parity gate, with the
  reason: the extension is a webview and the editor owns selection there.

**Rejected: removing it outright.** Wheel scrolling is what people expect of a
scrollable pane, and taking it away with no way back is the same class of silent
trade in the other direction.

## The open question, stated rather than settled

**The default is still ON.** My recommendation is to flip it to OFF, and the
argument is the asymmetry above: the cost is paid by every user on every copy
and is undiscoverable; the benefit is a convenience with a keyboard equivalent
that is already documented. A user who wants the wheel can find `/mouse` from
the hint on screen; a user who wants to copy currently cannot find anything.

I have not made that change, because it is a product judgment about which
gesture a majority of users reach for first, and I have no usage data. It is one
line in `main.go` plus the initial value of `mouseCaptured`, and the tests do
not depend on which way it points.

## Default decided (2026-09-27)

Capture now starts **OFF**. The deciding report was the one this ADR predicted:
a user said they "can't copy inside this interface" — not that mouse reporting
was on. With capture off the terminal keeps click-drag selection and
`ctrl+shift+c`, and the wheel still scrolls: on the alternate screen the
terminal's alternate-scroll mode sends each wheel notch as up/down keys, which
now scroll the transcript whenever the slash popup is closed. `/mouse` turns
capture back on for terminals that do not do that. The idle hint now says how
to copy and paste instead of naming `/mouse`.

Pasting had a second, separate cause: `ctrl+v` reads the clipboard through a
helper (`wl-paste`, `xclip` or `xsel`), and with none installed it did nothing.
It now says once to use the terminal's `ctrl+shift+v`, which always works.

## Revised the same day: back to ON

Within hours the user asked for shell-style prompt history on up/down. With
capture OFF that cannot coexist with wheel scrolling: the terminal sends each
wheel notch as up/down keys, indistinguishable from a real arrow press, so the
wheel would scroll through old prompts. Offered the three-way trade (history +
wheel with shift+drag select / history + plain select with no wheel / history on
ctrl+up/down), the user chose the first. Capture is ON again; up/down walk the
prompt history; the idle hint now names "shift+drag to select", which is the
discoverability gap this ADR was about in the first place.

## Revised 2026-10-25: the terminal keeps the mouse

The owner, from GNOME Terminal: "i can't do anything copy in the agent
workspace. only i can paste something in the prompt bar." The hint said
"shift+drag to select", and it was not enough.

**Tried first, and withdrawn the same day:** the chat selected text itself. A
plain drag highlighted text and copied it on release. It needed a clipboard
program to reach the system clipboard (`wl-copy`, `xclip` or `xsel`), and on
the owner's machine there was none. VTE, GNOME Terminal's engine, also ignores
OSC 52, the escape sequence that lets a program set the clipboard through the
terminal. So the app told the user to install `wl-clipboard`. The owner's
answer: "It should copy when the user presses ctrl+shift+c, no new
clipboards."

That rules out any copy the program makes itself. ctrl+shift+c copies only what
the terminal has selected, and a plain drag is a terminal selection only while
the program has not asked for mouse reports. With capture off, VTE sends each
wheel notch as up/down keys, which collides with up/down as prompt history.
These were the options offered:

1. **Chosen:** the wheel scrolls; up/down scroll too; prompt history moves to
   ctrl+up/down.
2. Keep capture on and copy with shift+drag, then ctrl+shift+c (today's keys).
3. Up/down stay as history, and the wheel stops scrolling (pgup/pgdn instead).
4. Rebuild the chat into the terminal's own scrollback, as Claude Code does.
   Drag, copy, wheel and up/down history would then all be native, and a long
   answer could be selected in one go. That is a rebuild of several days and
   was not chosen now.

**What changed:**
- `main.go` starts without `tea.WithMouseCellMotion`, and
  `chatModel.mouseCaptured` starts false to match.
  `TestTheProgramStartsWithoutMouseCapture` reads `main.go` and fails if the
  two disagree.
- up/down scroll the chat in every state, mid-turn included. The slash popup is
  the exception: there they still walk the highlighted command.
- ctrl+up/down, and alt+up/down, walk prompt history. alt is there because
  macOS gives ctrl+up/down to Mission Control. The alt keys have not been tried
  on a Mac.
- The idle hint reads `enter send · ctrl+↑↓ history · ctrl+shift+c copy ·
  ctrl+n new · ctrl+c quit`, with `cmd+c` on a Mac (`keyhints.go`).
- `/mouse` turns capture on, for a terminal whose wheel does nothing in the
  alternate screen.
- ctrl+v with no clipboard program now does nothing and adds no note. The note
  added on 2026-09-27 named clipboard programs to install. The owner's answer,
  the same day as the rest: "the mark should not be visible anywhere". `/help`
  lists the terminal's own paste key (ctrl+shift+v; cmd+v on a Mac), which
  always works because it arrives as typed text.

**What this does not do:** the alternate screen has no scrollback, so a
selection covers only what is on screen. A long answer is copied a screen at a
time. Option 4 is the fix for that.

## What would change this decision

- Usage data showing wheel-scroll is the dominant navigation gesture: keep ON.
- A single support report of "I can't select text": flip to OFF and stop asking.
- Bubble Tea v2, which changes mouse handling substantially: revisit both.

## Verified by

`clients/tui/mousetoggle_test.go` — the toggle changes the terminal mode rather
than only the flag (the returned `Cmd` is what reaches the terminal, so that is
what is asserted), it says what it did in plain terms, and it is discoverable
from both the idle hint and `/help`.
