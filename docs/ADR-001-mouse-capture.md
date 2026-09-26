# ADR-001 — Mouse capture in the terminal client

**Status:** accepted (toggle shipped); **default decided 2026-09-27: capture OFF** (see "Default decided" below).
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

## What would change this decision

- Usage data showing wheel-scroll is the dominant navigation gesture: keep ON.
- A single support report of "I can't select text": flip to OFF and stop asking.
- Bubble Tea v2, which changes mouse handling substantially: revisit both.

## Verified by

`clients/tui/mousetoggle_test.go` — the toggle changes the terminal mode rather
than only the flag (the returned `Cmd` is what reaches the terminal, so that is
what is asserted), it says what it did in plain terms, and it is discoverable
from both the idle hint and `/help`.
