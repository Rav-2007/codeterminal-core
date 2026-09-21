# Release notes

User-visible changes, newest first. Internal refactors, test work and
performance changes are not listed here unless you can observe them.

---

## Unreleased

### The approval prompt says which program starts, and only when it does

**What changed.** Finding a definition or its references, or proposing an edit
by symbol, can start a language server: `gopls`, `typescript-language-server`
or `pyright-langserver`, from your PATH. That program reads your project's
configuration, and a `tsconfig.json` can load plugins. It then keeps running
until the daemon exits.

The prompt used to say *"STARTS ANOTHER PROGRAM"* every time one of these tools
was used, including every time after the server was already running. The one
prompt that actually started it looked the same as all the others.

Now the first prompt says **STARTS gopls**, naming the program, and approving it
is what starts it. Later prompts say **ASKS gopls, WHICH IS ALREADY RUNNING**,
because those calls start nothing new. An "approve for the rest of this task"
answer never covers starting it again if the server has stopped: that asks
again.

### Starting a language server asks you first, even when the tool is set to `allow`

**A behaviour change you will notice.** If your configuration sets one of these
tools to `"allow"`, it still runs without a per-call prompt, but the call that
would start its language server now asks first, once per server. Before this
the server started without anyone being asked. Allowing a lookup is not the
same as agreeing to start a program that reads your project's configuration.

### Idle language servers are stopped after ten minutes

**What changed.** A language server started for a lookup used to keep running
until the daemon exited. A single `gopls` measured 295 MB of memory, and a
project with Go, TypeScript and Python code could keep three running.

Now a server that has not been used for ten minutes is stopped. A server
answering a request is never stopped, however long the request takes, and every
lookup resets the clock, so a working session never runs into it.

**What you will notice.** After a break, the next lookup asks again:
**STARTS gopls**. That is the consent prompt doing its job. Starting a program
is asked about every time it happens, including a restart. The server then
reloads your project, so that first lookup takes a little longer.

### Old sandbox caches are cleaned up

Each project that runs a build or test command gets its own cache folder, and
those folders were never deleted. They stayed after the project was gone, and
one measured machine had 1,712 of them.

The daemon now removes, when it starts, any of these folders whose project no
longer exists or that nothing has used for 30 days. It only removes folders it
created: anything else in that directory, and anything a link points to, is
left alone. The folder for the project you have open is never removed.

### The startup warning describes each tool by what it does

When built-in tools are set to `"allow"`, the daemon lists them at startup. It
used to put everything except the two web tools under *"these are confined and
do not write to your files"*. That was false for the language-server tools, and
false for `sandbox_exec`, which runs build and test commands with your full
privileges when neither bwrap nor docker is installed. Each kind of tool now
gets its own accurate sentence. A tool name that matches no built-in, such as a
typo, is now reported as doing nothing instead of being described as a tool.

---

## v0.0.4 — 2026-09-21

**The first release intended to be published.** v0.0.1, v0.0.2 and v0.0.3 were
tagged and built and never published, so everything below ships to a user for
the first time here.

### Everything is named Mochiii

The product was already called Mochiii; the identifiers were not. Binaries are
now `mochiii-daemon`, `mochiii-tui` and `mochiii-embedder-helper`; the
environment variables are `MOCHIII_API_KEY`, `MOCHIII_API_BASE`,
`MOCHIII_USE_PROXY` and `MOCHIII_PROXY_KEY`; the setting is `mochiii.apiBase`;
the workspace state directory is `.mochiii/` and the model cache is
`~/.mochiii/models/`.

**No compatibility shims, deliberately.** Nothing was ever published, so there
is no installed base to keep working. If you built from source, update your
exports; `mv ~/.codeterminal ~/.mochiii` keeps the embedding model you already
downloaded instead of fetching it again.

### You can set an API key

There was previously no way to. The daemon reads its credential from the
environment and nothing put it there, so a packaged install could never
authenticate and every answer failed with *"the configured API credentials were
rejected"*. Run **`Mochiii: Set API Key`** from the command palette. The key is
stored in VS Code's SecretStorage — not `settings.json`, which Settings Sync
replicates and a commit can leak.

### The daemon and embedder helper are published

The standalone terminal client previously shipped with nothing to talk to: it
looks for a running daemon and exits telling you to start one, and no daemon was
published anywhere. All three binaries now ship per platform, with checksums in
`SHA256SUMS-bin`.

### Your terminal is restored when the session ends, not just when you quit

**What changed.** Closing a terminal window or dropping an ssh connection
sends the client SIGHUP. It used to die on the spot without putting the
terminal back: the alternate screen stayed up, the cursor stayed hidden, and
mouse tracking stayed on. If the shell underneath survived, you were left
typing blind into something you could not see, usually needing `reset`.

SIGHUP now goes through the same clean shutdown as `kill` and Ctrl-C, and so
does SIGQUIT. Nothing about quitting normally has changed.

**SIGQUIT still dumps.** `kill -QUIT` on the client prints every goroutine's
stack, the way it always did — the difference is that your terminal is put back
first, so you can actually read it. The exit status is 131 (128+3), as before.

**One known limitation, stated plainly.** If the terminal is destroyed outright
rather than sending a hangup — which happens in some terminal emulators and
multiplexers — the client is not notified at all and keeps running in the
background. It costs nothing but memory, and `pkill mochiii-tui` clears
it. This is not new, and it is being worked on.

### `--prompt` piped into `head` no longer dies by signal

Running one-shot into a reader that stops early (`| head`, `| grep -m1`, or
closing a pager) used to kill the client with SIGPIPE — exit status 141. It
now exits 0 quietly. A reader taking only part of the answer is a normal thing
to do, not an error.

### Terminal control sequences are stripped from untrusted text

**What changed.** Everything Mochiii displays that it did not write
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
runs (`mochiii-tui --prompt "..."`, or text piped on stdin) now filter
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

### Pasting more than fits now tells you

**What changed.** The prompt box holds 4,000 characters and always has. Anything
past that was dropped without a word — paste a 40KB file meaning to ask about
it, and the model was asked about its first 4,000 characters instead, with
nothing on screen to say so and an answer that came back confident and about the
wrong input.

It now says how many characters arrived, how many were kept and how many were
dropped. A very large paste is also no longer slow: 1MB used to take about a
frame and a half to process, and now takes a fiftieth of that.

### Long sessions trim themselves, and say when they do

**What changed.** A session that ran long used to keep every byte of every
answer it had ever received. It now drops its oldest turns once it passes 500
turns or 2 MB of text, and leaves a line at the top of the transcript saying how
many turns and how many bytes went.

`/compact` is unchanged and is still the deliberate version — it keeps the last
8 turns, immediately. The automatic trim is a safety net that keeps hundreds.
Both ceilings can be changed with `MOCHIII_MAX_TURNS` and
`MOCHIII_MAX_TRANSCRIPT_BYTES`.

### `/mouse` — you can select text with the mouse again

**What changed.** The client captures mouse events so the wheel scrolls the
transcript. The cost, which was never mentioned anywhere, is that your terminal
stops handling click-drag selection: copying anything out required holding
Shift, and nothing on screen said so.

`/mouse` turns capture off and on. With it off, select and copy exactly as you
would in any other terminal program, and use `pgup`/`pgdown` to scroll. The
default is unchanged.

### You can scroll back through an answer while it is still arriving

**What changed.** Scrolling up during a reply used to be impossible. The view
snapped back to the bottom on every streamed token — within milliseconds, every
time — so whatever you had scrolled up to read was gone before you could read
it.

The view now follows the newest text only when you are already at the bottom,
which is where you normally are. Scroll up and it stays where you put it;
scroll back down and it starts following again.

### Long sessions draw faster, and the transcript is still unbounded

**What changed.** Drawing the transcript used to cost time proportional to the
whole conversation, on *every* streamed token — so the longer a session ran, the
more the answer stuttered as it arrived. Two things changed: the rendered form
of each turn is now kept and reused instead of being rebuilt from scratch, and
the screen is redrawn at most once per frame rather than once per token.

Measured on a 240-turn conversation, the work done per token dropped by roughly
fifty times. Nothing about what is displayed has changed — the same bytes, in
the same order.

**What has not changed, stated plainly.** The transcript is still only trimmed
by `/compact`, so a long enough session still grows without limit in memory.
That is a separate piece of work and it has not been done.
