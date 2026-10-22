# Release notes

User-visible changes, newest first. Internal refactors, test work and
performance changes are not listed here unless you can observe them.

---

## v1.2.1 — 2026-10-22

The first published 1.2 release. Version 1.2.0 was built but not published, so
its changes, listed under it, reach you in this release, except npm: the
packages are not on npm yet, so `npm install -g mochiii` does not work.

### A short install command

**What changed.** The install command now fits on one short line. On macOS and
Linux:

```bash
curl -fsSL https://mochiii.in/install | sh
```

On Windows, in PowerShell:

```powershell
powershell -ExecutionPolicy ByPass -c "irm https://mochiii.in/install.ps1 | iex"
```

Both addresses redirect to the installers in the latest GitHub release. The full
GitHub addresses still work.

### The install commands install the terminal app only

**What changed.** The install commands used to install the VS Code extension too,
when they found VS Code. They now install only the terminal app. The extension
comes from the VS Code Marketplace. `--no-vscode` (`MOCHIII_NO_VSCODE`) is still
accepted and does nothing.

## v1.2.0 — 2026-10-21

Built but not published: these changes reached users in v1.2.1, without npm.

### `npm install -g mochiii` installs Mochiii on any OS

**What changed.** The install command is now the same in every shell on macOS,
Linux and Windows, for anyone with Node.js 18 or later:

```bash
npm install -g mochiii
```

npm installs only the build for your computer. On macOS and Linux the `mochiii`
command is then the native binary itself, so starting Mochiii does not start
Node. The curl and PowerShell lines from 1.1.0 still work, for machines without
Node.js. Update with `npm install -g mochiii@latest`; to remove Mochiii, run
`mochiii --stop` in each project folder you used it in, then
`npm uninstall -g mochiii`.

### One program instead of three

**What changed.** `mochiii` is now the terminal app, the background service and
the code-search helper in one file, with its default configuration built in. The
service's commands run as `mochiii index`, `mochiii download-model` and
`mochiii connect`. The download is 15 MB instead of 22 MB. Upgrading a 1.1.0
install stops its background service and replaces its three programs.

### Answers start sooner

**What changed.** Mochiii used to open its connection to your AI provider only
when a question was sent, so the first question of a session, and the first
after a pause, waited for DNS and a TLS handshake: 0.2-0.4 s measured to real
providers, and 5 s once on a cold DNS lookup. The background service now opens
that connection while you type, or as it starts, without your key and without
sending anything. Only your provider's address is contacted, the same one your
questions go to.

Against a stand-in provider whose connections cost 0.3 s:

- a question typed after opening Mochiii arrived in 16 ms instead of 327 ms;
- a first question in an indexed project, with no service running yet, arrived
  in 0.38 s instead of 0.79 s;
- every Mochiii process starts in 11 ms instead of 18 ms.

### After an npm upgrade, the old background service is replaced

**What changed.** npm replaces the program but never stops the background
service it left running. The next `mochiii` in a folder now notices a service of
the same install at another version, says so, and restarts it. A service from
another install, VS Code's own among them, is left running, and Mochiii names
its version.

### Alpine and other musl systems

**What changed.** On Alpine, both install commands give you a build without code
search; chat and agent mode work. Code search needs glibc, and since 1.2.0 the
regular Linux build needs glibc to start at all. `mochiii index` says so on that
build, and Mochiii never offers to index a folder there.

### Code search needs no C compiler, and an older glibc

**What changed.** The code-search helper calls ONNX Runtime directly instead of
through cgo. On Linux, code search now needs glibc 2.27 (Ubuntu 18.04, Debian 10,
RHEL 8 and later) instead of 2.34, and building Mochiii from source needs no C
compiler. Search results are unchanged: the vectors match the old helper's bit
for bit.

### A Go security fix

**What changed.** Mochiii is built with Go 1.26.10, which fixes GO-2026-6809 in
the certificate checks behind every connection to your provider.

## v1.1.0 — 2026-10-18

### One command installs Mochiii, on macOS, Linux and Windows

**What changed.** The terminal app used to be built from source, on Linux only.
Now one command installs it: `curl -fsSL …/install.sh | sh` on macOS and Linux,
or `irm …/install.ps1 | iex` in PowerShell. The full commands are on the
[front page](../README.md#install).

- It runs on Linux x64 and ARM64, Apple Silicon Macs, Windows x64, and Windows on
  ARM (the x64 build, under Windows' emulation).
- It checks every download against the release's checksums, needs no
  administrator rights, and downloads the code-search model.
- It adds the VS Code extension when VS Code is installed.
- Running it again updates; `--uninstall` removes Mochiii and keeps your keys,
  models and chats.

Before any release is drafted, CI runs the real installer on all five platforms.
Intel Macs are not supported: onnxruntime, which runs the code-search model, has
no Intel Mac build.

### `mochiii` starts its own background service, and sets a folder up the first time

**What changed.** `mochiii` used to fail with "daemon not found" unless something
else had started the daemon. Now it starts the background service for the folder
itself. It only ever starts the one installed beside it, never one found on
`PATH` or in the project.

The first time it runs in a folder, it offers to download the code-search model
and index the folder, so answers are grounded in your code. Answer `never` and it
stops asking for that folder. New options:

- `--index` re-indexes the folder;
- `--no-index` skips the offer;
- `--stop` stops the folder's service;
- `--version` prints the version.

### Code search works on Windows

**What changed.** On Windows the daemon looked for its embedder helper without
the `.exe` extension, so it never found it, and code search was silently off on
every Windows install, the VS Code extension's included. It now finds it.

### Long tasks judge every check, and remember what failed

**What changed.** `/fix`, `/debug` and `/refactor` now judge each build or test
run against the last one and the best one, and carry what failed from one segment
to the next. By default this is recorded, not acted on (`mcp.correction.mode:
shadow`). Setting `on` lets a task restore its best state, change strategy when
it is stuck, and re-run an approved check itself.

Three fixes came with it:

- A task counts every model call it makes against its budget.
- Restoring a checkpoint never brings back a file the task deleted.
- A Python `/fix` can finish on a plain `pytest`.

---

## Before v1.1.0 — shipped in the 0.1.x and 1.0.x releases

Written while unreleased, and shipped by 0.1.0 through 1.0.1. The extension's
[CHANGELOG](../clients/vscode/CHANGELOG.md) lists those releases version by
version.

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

### Build and test commands are sandboxed on Ubuntu 24.04 and later

**What changed.** `sandbox_exec` runs `go`, `npm`, `make` and `cargo` commands
the model asks for. It confines them with bubblewrap or Docker, and Ubuntu 24.04
and later block bubblewrap by default. On those machines, without Docker, every
approved command ran with your full privileges. The prompt said
**NOT SANDBOXED**, but that was the normal case, not an exception. Whether a
command was confined also depended on what started the daemon: VS Code could,
a terminal could not.

These commands are now confined there with Landlock, which is built into the
Linux kernel and needs no setup. A command can read the system and its
toolchain, and read and write only your project and its own cache folder. It
cannot read your home folder, cannot use `/tmp`, cannot open Unix sockets (so it
cannot reach your desktop session, your `ssh-agent` or the daemon), and cannot
share memory or message queues with your other programs. A process it starts in
the background is stopped when the command finishes, rather than being left
running. Machines where bubblewrap or Docker already worked are unchanged.

**What you will notice.** The approval prompt says *Confined to this workspace
by Landlock* instead of **NOT SANDBOXED**, and it states the two things
Landlock does not hide: the command can see your other running programs, and
on kernels older than 6.12 it can send them signals. A build or test that opens
a Unix socket of its own fails with a permission error. Temporary files go to a
folder of the command's own, which is removed when the command ends. If the
folder you opened *is* your home folder, the prompt says so plainly instead of
claiming your home is protected, because in that case it is not. The prompt also
notes what the sandbox cannot undo: anything a build writes in your project —
a `Makefile`, a `.git/hooks` script — runs with your full privileges the next
time you use the project, outside any sandbox.

### Giving Mochiii your API key no longer means setting an environment variable

**What changed.** Mochiii needs a key for the model provider it talks to, and
until now there was exactly one way to supply it: export `MOCHIII_API_KEY` in the
shell before starting the daemon. If you did not know the variable's name,
nothing worked and nothing told you why.

`mochiii-daemon --help` now also lists the subcommands it accepts, `connect`
among them, instead of printing only flags — the reason the command was reported
missing after it had shipped was that nothing named it.

There is now a command for it, in three places:

```
mochiii-daemon connect     # from a shell
/connect                   # in the terminal client
/connect                   # in the VS Code chat
```

Each asks for the key at a masked prompt — it is not shown as you type, and it is
not left in your terminal history or in the chat transcript. Deliberately, a key
is **not** accepted as an argument: on a command line it is readable by other
programs on the machine and is written to your shell history, and in a chat it
would be kept in the transcript and sent along with your next question.

**It asks you at the moment it needs the key, not when you open the client.**
Starting the terminal client no longer greets a new user with a credential form:
you get the prompt. The key is only actually needed when you ask something, so
that is when it asks — your question is held, the masked key prompt opens, and the
moment your provider accepts the key the question you already typed is sent. You
never type it twice. Cancel, type nothing, or give a key the provider refuses, and
your question is handed straight back to the input rather than lost or sent into a
failure.

One exception, deliberately: if your `api_base` points at a **local** server
(`localhost` or a loopback address), nothing asks for a key at all. A local
OpenAI-compatible server that wants no `Authorization` header is a supported
setup, and nagging it for a credential it does not use would be wrong.

**It checks the key before telling you it worked.** Mochiii asks your provider
whether the key is valid. If the provider refuses it, nothing is stored and
whatever key you were using before is left alone — so a typo cannot take away a
key that was working. If your provider has no way to answer the question, or the
machine is offline, the key is stored and Mochiii says plainly that it could
**not** verify it, rather than implying it did.

**In the terminal client it takes effect immediately.** No restart: the next
question you ask uses the new key — unless a key is set in the daemon's
environment, which still wins. In that one case the client says so rather than
reporting the key as in use, because a message claiming the key was in force
while the daemon kept sending a different one is worse than no message. In VS
Code the key goes to the editor's credential storage and the daemon restarts to
pick it up.

`/connect show` tells you which key is in force, showing only its last four
characters. `/connect forget` removes it, and says so only when there was one to
remove.

**If you already set `MOCHIII_API_KEY`, nothing changes for you.** A key in the
environment still takes precedence over a stored one, so existing setups keep
working exactly as before — and `/connect show` tells you when that is what is
happening, which is otherwise invisible.

### A sandboxed build can no longer reach the cloud metadata endpoint

**What changed.** A build needs the network to fetch its dependencies, so the
sandbox allows it — and that allowed every confined command to reach
`169.254.169.254`, the address cloud machines serve their instance credentials
from. A malicious dependency or a build script could read those credentials on
any cloud build machine.

Confined commands now have that blocked — **on both sandboxes, bubblewrap and
Landlock**. The sandbox watches the connections a build opens and refuses the
cloud metadata address and the other link-local ranges, while everything the
build legitimately needs — package registries, module proxies, DNS — is
untouched. Services on your own machine (`localhost`) stay reachable, because
builds and tests legitimately use them. Bubblewrap is the sandbox most machines
actually use, so this is the change that makes the protection real for most
people rather than for a minority of hosts.

**It is proven before it is claimed.** The prompt only says the endpoint is
blocked on a machine where Mochiii has just demonstrated the block working, and
each sandbox is demonstrated separately — one working proves nothing about the
other. Where it cannot be demonstrated, the prompt keeps saying the endpoint is
reachable rather than claiming a protection you do not have.

**It does not get in the way of your build.** Real builds fetch over the network
from processes they start — `make` runs a shell, the shell runs `curl`. Those are
allowed exactly as before; only the metadata and link-local addresses are
refused. That is covered by a test that would fail if a build's own network ever
started being denied.

**What it does not cover.** The confinement holds while the command runs. Docker
is unchanged and does not need this: it already runs on its own network. A build
process that detaches itself completely from Mochiii loses its network rather
than slipping past the block, and a 32-bit program run inside bubblewrap will no
longer start — both are the safe direction, and both are deliberate.

### A force-killed daemon no longer leaves a build process running

If Mochiii's daemon was force-killed (or crashed) while a sandboxed command was
running, a process that command had left in the background kept running. It was
still confined, but it kept the workspace and the network until you noticed it.

Such a process is now stopped the moment the daemon dies, rather than lingering —
the same thing bubblewrap does for its own sandboxes. As a backstop, anything
that does survive is cleaned up the next time the daemon starts. A normal
shutdown stops it immediately, as before.

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
