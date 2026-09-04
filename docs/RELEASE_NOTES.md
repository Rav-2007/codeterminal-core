# Release notes

User-visible changes, newest first. Internal refactors, test work and
performance changes are not listed here unless you can observe them.

---

## Unreleased

### Terminal control sequences are stripped from untrusted text

**What changed.** Everything CodeTerminal displays that it did not write
itself — model answers and reasoning, tool output, tool-call arguments on the
approval screen, proposed edits, daemon error messages, and conversation
history restored from a previous session — is now filtered before it reaches
your terminal. Colour and text styling survive; everything else is removed.

**Why.** Those bytes are chosen by a model, or by an MCP server, or by a file
the model read. Written straight to a terminal they are not text, they are
commands. Before this change a model answer could clear your screen, rewrite
your window title, lock the scroll region, silently overwrite the line you had
just read, turn any text into a hyperlink pointing anywhere, or write to your
system clipboard. All of those were reproduced against a real terminal.

The case that mattered most was the tool-approval screen. It shows the
arguments a tool is about to run with, and it is where you decide whether to
allow it. A control sequence there could repaint the question you were
answering — not a display bug, but forged consent.

**What is kept.** Colour and styling, so syntax-highlighted code still looks
the way it did: bold, dim, italic, underline, reverse, strikethrough, the
standard and bright foreground and background colours, and both the 256-colour
and 24-bit truecolour forms. Blink and *conceal* are removed — conceal makes
text invisible on screen while leaving it in the buffer and in anything copied
out of it, which on an approval screen is the attack rather than a style.

### `--prompt` and piped stdin: what stdout guarantees

**This is a behaviour change you can observe outside the terminal.** One-shot
runs (`codeterminal-tui --prompt "..."`, or text piped on stdin) now filter
stdout the same way. That output previously promised to be byte-for-byte
whatever the model sent; it no longer is.

The filtering is **unconditional** — it does not check whether stdout is a
terminal. An escape sequence written to a file is not defused, only deferred:
it runs the moment anyone `cat`s or `less`es that file. One-shot output landing
in a log that someone greps later is exactly that path.

What stdout guarantees now:

- **The text of the answer is byte-stable.** Every printable character,
  newline and tab the model produced, in order, unchanged.
- **Terminal control sequences are removed**, except colour, which is kept.
- Tool activity and approval prompts still go to stderr, never stdout.

If you were relying on stdout to carry control sequences through to another
program, that no longer works, and it was never safe. Nothing that reads the
answer as text is affected.

### Long sessions are unaffected in what they show, but not yet in what they cost

No change here yet — recorded so it is not mistaken for one. Redrawing the
transcript still costs time proportional to the length of the conversation on
every streamed token, so a very long session gets slower to draw. The
transcript is still only trimmed by `/compact`. Both are being worked on.
