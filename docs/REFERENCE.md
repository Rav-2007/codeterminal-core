# Mochiii reference

The full manual: every command, flag, setting and environment variable, and how
each part of Mochiii works. The short version — what Mochiii is, how to install
it, and the commands most people use — is the [front page](../README.md).

## Contents

| | |
|---|---|
| **Run it** | [Install, update and uninstall](#install-update-and-uninstall) · [Quick start — from source](#quick-start--from-source) · [Commands](#commands) · [Configuration](#configuration) · [Slash commands](#slash-commands) |
| **How it works** | [How it works](#how-it-works) · [Security posture](#security-posture) |
| **Work on it** | [Development](#development) · [VS Code extension internals](#vs-code-extension-internals) |
| **Limits** | [Known platform gaps](#known-platform-gaps) · [Out of scope, on purpose](#out-of-scope-on-purpose) |

---

## Install, update and uninstall

macOS and Linux:

```bash
curl -fsSL https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.sh | sh
```

Windows (PowerShell):

```powershell
powershell -ExecutionPolicy ByPass -c "irm https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1 | iex"
```

The installers live in [`install/`](../install/) and are attached to every
release. In order, they:

1. pick this machine's build;
2. download it and refuse to go on unless it matches the release's
   `SHA256SUMS-archives`;
3. unpack it beside the current install, check that it runs, and swap it in, so a
   failure part-way leaves the previous version working;
4. put it on your `PATH`;
5. download the code-search model;
6. install the VS Code extension when VS Code is present and the platform has one.

They never use `sudo` or administrator rights, and they refuse to run as root or
elevated unless told to. Before any release is drafted, its CI runs the real
installer on Linux x64 and ARM64, macOS on Apple Silicon, Windows x64 and Windows
on ARM.

| Option (`install.sh`) | Environment variable (both installers) | Effect |
|---|---|---|
| `--version 1.1.0` | `MOCHIII_VERSION=1.1.0` | Install that release instead of the latest |
| `--no-model` | `MOCHIII_NO_MODEL=1` | Skip the code-search model download |
| `--no-vscode` | `MOCHIII_NO_VSCODE=1` | Do not install the VS Code extension |
| `--no-modify-path` | `MOCHIII_NO_MODIFY_PATH=1` | Leave your `PATH` alone |
| `--uninstall` | `MOCHIII_UNINSTALL=1` | Remove Mochiii; keep `~/.mochiii` |
| `--purge` | `MOCHIII_PURGE=1` | With uninstall, remove `~/.mochiii` too |
| `--allow-root` | `MOCHIII_ALLOW_ROOT=1`; on Windows `MOCHIII_ALLOW_ADMIN=1` | Install even as root, or from an elevated PowerShell |

With `curl | sh`, pass options after `sh -s --`, for example
`curl -fsSL …/install.sh | sh -s -- --no-vscode`. `irm | iex` cannot pass
parameters, so on Windows set the environment variable first. A variable counts
only when it is exactly `1`, so `MOCHIII_PURGE=0` purges nothing. A saved
`install.ps1` also takes them as parameters (`-NoModel`, `-AllowAdmin`, …).

**Why Windows refuses an elevated PowerShell.** Installing elevated is harmless
by itself, but running `mochiii` from that same window starts its background
service elevated. The service's socket directory then belongs to Administrators,
and `mochiii` in every normal window afterwards is refused it, with a message
saying to delete the directory or run at the same elevation. After
`MOCHIII_ALLOW_ADMIN=1`, open a normal PowerShell to run `mochiii`.

**Where things go:**

| | macOS / Linux | Windows |
|---|---|---|
| Programs | `~/.mochiii/bin/` (+ link `~/.local/bin/mochiii`) | `%LOCALAPPDATA%\Programs\Mochiii\bin` |
| Keys, models, MCP servers | `~/.mochiii/` | `%USERPROFILE%\.mochiii\` |
| Chats, rules, tasks, logs | `$XDG_STATE_HOME/mochiii` or `~/.local/state/mochiii` | `%LOCALAPPDATA%\mochiii` |
| A project's index | `<project>/.mochiii/index` | the same |

**`mochiii` in a project folder** starts Mochiii's background service for that
folder if none is running. Only the service installed beside `mochiii` is
started, never one found on `PATH` or in the project. The service keeps running
after the chat closes, so the next start is instant. The first run offers to
download the code-search model and to index the folder; answer `never` to stop
being asked for that folder. Other options:

- `mochiii --index` (re)builds the folder's index;
- `mochiii --no-index` (or `MOCHIII_NO_INDEX=1`) skips the offer;
- `mochiii --stop` stops the folder's background service;
- `mochiii --version` prints the version.

**Troubleshooting:**

- `mochiii: command not found` right after installing: open a new terminal. The
  installer added `~/.local/bin` to your shell's startup file.
- Code search is off on Linux: it needs glibc 2.27+ (`ldd --version`). Chat and
  agent tools work regardless.
- Code search is off on Windows: install the Visual C++ runtime from
  <https://aka.ms/vs/17/release/vc_redist.x64.exe>.
- A Windows upgrade or uninstall says "Close mochiii (process N)": a `mochiii`
  is still open in another window. Close it and run the command again; nothing
  was changed.
- A Windows upgrade or uninstall says it is "waiting for … to let go of the
  files": Windows is still holding programs that were just stopped, as Windows
  Defender scans them or, on Windows on ARM, the x64 emulator caches them. That
  usually takes under a second, and the installer waits up to 30 seconds before
  giving up with the name of whatever holds the files.
- An Intel Mac is refused: onnxruntime, which runs the code-search model, has no
  Intel Mac build.
- No warnings at install time: SmartScreen and Gatekeeper act on files a browser
  downloads, and the installers download with `curl` and PowerShell, which leave
  no such mark. The release's checksums are what make the download trustworthy.

## Quick start — from source

For working on Mochiii. Everyone else: [install it](#install-update-and-uninstall).

**Requirements:** Go 1.26.9+, and nothing else: every program is pure Go and
builds with `CGO_ENABLED=0`, so no C compiler is needed. An older Go (1.21 or
later) downloads that toolchain by itself on the first build, unless
`GOTOOLCHAIN=local` is set.

### 1. Build

The helper is **not** optional — the daemon spawns it to compute embeddings
locally, and retrieval is disabled without it.

```bash
(cd helper      && go build -o mochiii-embedder-helper .)
(cd daemon      && go build -o mochiii-daemon .)
(cd clients/tui && go build -o mochiii-tui .)
```

### 2. Fetch the model — once per machine

Downloads the pinned BGE model and the onnxruntime shared library into
`~/.mochiii/models/`, verifying every file's size and SHA-256. A second run
with everything present makes no network requests.

```bash
./daemon/mochiii-daemon download-model
```

### 3. Index — once per repo

```bash
./daemon/mochiii-daemon index .
```

Skipping this is not fatal: the daemon starts and answers **ungrounded**, using
no code from your repo.

While the daemon runs it watches the workspace and re-indexes about a second
after you save, create, delete, or rename a file, so ordinary editing needs no
action. Re-run `index` after changes the watcher could not see — anything done
while the daemon was stopped, such as a branch switch or a pull.

### 4. Run

Pick one of the two inference paths.

<details open>
<summary><b>Direct mode</b> — your provider key goes from this machine to the provider</summary>

```bash
# Hand the daemon your key once -- from any major provider. It works out whose
# key it is, checks it with that provider before storing it, and writes it 0600
# to ~/.mochiii/credentials.json:
./daemon/mochiii-daemon connect
# Paste your provider API key (it will not be shown): ...
#   Checking the key with https://openrouter.ai/api/v1 ...
#   Verified: the provider accepted it: key "laptop", credit limit 12.50
```

**Any major provider's key works, and there is no address to type.** The provider
is recognised from the key's own prefix — `sk-or-` OpenRouter, `sk-ant-`
Anthropic, `sk-proj-` OpenAI, `AIza` Google Gemini, `nvapi-` NVIDIA, `gsk_` Groq,
`xai-` xAI, and so on — and the key is sent to that provider and to no other.
The full list, by the name each is known by:

`openrouter` · `openai` · `anthropic` · `gemini` · `nvidia` · `groq` · `deepseek` ·
`xai` · `mistral` · `together` · `fireworks` · `cerebras` · `moonshot` ·
`huggingface` · `sambanova` · `dashscope` · `zai`

- **A key whose prefix names nobody is never guessed at.** Several providers
  issue keys that start with a bare `sk-`; such a key is sent nowhere until you
  say whose it is: `connect --provider deepseek` (or `/connect deepseek` in the
  TUI). `--api-base <url>` names any other OpenAI-compatible address.
- **For any provider but OpenRouter, `connect` also finds a model.** It asks the
  provider what it serves, sends small test requests — best candidate first —
  until one answers, and stores that list beside the key, so `/model` offers the
  provider's own models in place of `models.json`'s tiers, which name
  OpenRouter's. The answer is also what proves the key: some providers offer no
  other way to check one. It is one request, a few tokens, when the provider's
  list is what it serves.
- **A provider's list is not always what it serves.** Measured on NVIDIA with a
  real key (2026-10-05): of 59 chat models listed, most answered "not found" for
  the account or accepted the request and never replied, and a handful answered
  in seconds. So when the best candidate does not answer, up to 40 are tried,
  several at a time. Models the provider says are not there are left out of
  `/model`; ones that sent nothing back stay, marked as such.
- **Zero-data-retention routing is something OpenRouter does.** With any other
  provider, prompts go straight to it under its own data policy, and the client
  says so.

`connect` refuses a key the provider rejects rather than storing it, and says
plainly when it could **not** verify one instead of implying it did. Use
`connect --show` to see which key is in force (never the key itself),
`connect --forget` to remove it, and `--no-verify` on a machine with no network.
A key is deliberately **not** accepted as a command-line argument: in `argv` it is
visible to other processes through `ps` and is written to your shell history.

Environment variables still work, and a key in the environment still takes
precedence over a stored one. A program that starts the daemon can instead hand
it a key **on stdin**: `--credentials-from-stdin`, then one JSON line —
`{"api_key": "…", "api_base": "…", "provider": "…", "proxy_key": "…"}`, every
field optional, `{}` for none. The VS Code extension starts the daemon exactly
this way, so its key is never in the daemon's environment. A stdin key takes the
stored credential's place: `MOCHIII_API_KEY` in the environment still wins over
it, and while one is supplied the file `connect` wrote is not consulted. Highest
first: **`MOCHIII_API_KEY` → a key on stdin → the key `connect` stored.** A stdin
key that names no `api_base` (or `provider`) goes with whatever base is in force,
as `MOCHIII_API_KEY` does; one that names a base is sent only there. A malformed
line, or none within five seconds, stops the daemon with the reason rather than
letting it start keyless. The one case the stored credential wins:
`MOCHIII_API_BASE` naming one of the providers above with **no**
`MOCHIII_API_KEY` beside it. That can only fail — every one of them needs a key —
so a key connected for a different provider is used, with its own address, and
the daemon logs that it did. A custom address (a local server, a gateway) is
always kept, and a stored key is never sent to it.

```bash
export MOCHIII_API_BASE="https://api.together.xyz/v1"
export MOCHIII_API_KEY="sk-..."

# ...or keep them in .env (gitignored, never commit it):
cp .env.example .env && $EDITOR .env
set -a && source .env && set +a
```

```bash
./daemon/mochiii-daemon --workspace .    # terminal 1
./clients/tui/mochiii-tui                # terminal 2
```

**With no key yet, the terminal client opens on the setup** rather than the
chat: choose a provider, paste its key, choose a model — the same three steps
`/provider` runs at any time ([details](#slash-commands)). Esc skips to the chat;
a key is then asked for when you send your first question. With a key already
in force it opens on the chat, and the line under the prompt says which model
and provider that is.

**Those two commands start with agent mode off:** the model is given no tools,
so it cannot read a file it was not handed, propose an edit through a tool, run
a test, or search the web. To get the tools, start with the agent config
instead — either run [`./run-tui.sh`](../run-tui.sh), which builds and starts both
halves for you, or add `--config models.agent.json` to the daemon line. Two of
those tools reach the internet; read
[what that config allows](#modelsagentjson--agent-mode-and-the-one-setting-that-reaches-the-internet)
first.

</details>

<details>
<summary><b>Managed proxy mode</b> — the pilot path; this machine holds one Mochiii key</summary>

Inference runs through the [managed proxy](#managed-proxy), which holds the
provider key server-side, meters usage per user, and enforces zero-data-retention
routing before anything reaches the provider.

[`run-proxy.sh`](../run-proxy.sh) is the whole path: it sources `.env`, points
`MOCHIII_API_BASE` at the production proxy, sets `MOCHIII_USE_PROXY=true`,
**unsets `MOCHIII_API_KEY`** so a provider key can never reach the proxy by
accident, refuses to start without a Mochiii key, and then `exec`s the daemon — so
every daemon flag passes straight through.

```bash
cp .env.example .env && $EDITOR .env    # set MOCHIII_PROXY_KEY=mochi_...

./run-proxy.sh --workspace ~/some/repo               # terminal 1
./clients/tui/mochiii-tui --workspace ~/some/repo   # terminal 2
```

Startup logs `proxy mode: forwarding inference through …` — that line is how you
confirm the pilot path is in use. Point at a different proxy with
`MOCHIII_PROXY_BASE=http://localhost:8080/v1`; `MOCHIII_API_BASE`
deliberately cannot do this, because the script sets it authoritatively so that
`source .env` stays safe to combine with proxy mode.

</details>

**VS Code users skip terminal 1.** The extension manages the daemon itself —
it probes the per-workspace lockfile, adopts a daemon that answers a handshake,
and starts one only when nothing does.

Stop the daemon with Ctrl-C; it removes its socket and lockfile on the way out.
If it is ever killed outright, the next start detects that nothing is listening
on the leftover socket, removes it, and binds a fresh one.

---

## Commands

All commands are subcommands of the `mochiii-daemon` binary.

| Command | Purpose |
|---|---|
| *(none)* | Run the daemon in the foreground |
| `index <path>` | Build the vector + lexical index for a workspace |
| `retrieve --workspace <p> --k N <query>` | Query the index and print top-k hits |
| `edits apply [--workspace p] [file\|-]` | Apply SEARCH/REPLACE blocks, or a unified diff, from a response |
| `edits undo [--workspace p] [--session ts] [--force]` | Restore a backup session |
| `mcp list [--config p]` | Show what agent mode would advertise, and its policy |
| `mcp add <name> [--env NAME] [--in-project] -- <command> [args…]` · `mcp remove <name>` | Add or remove an MCP server of your own ([agent mode](#agent-mode)) |
| `skills list [--limit N] [--json]` · `skills delete <id>` | Local skills store |
| `download-model` | Fetch the pinned BGE model + onnxruntime library |
| `helper-smoketest <text>` | Spawn the helper, embed one string, shut down |

**Daemon flags**

| Flag | Effect |
|---|---|
| `--workspace <path>` | Which repo grounds answers (default: cwd) |
| `--config <path>` | Path to `models.json` (default: beside the binary, then `./models.json`) |
| `--model <slug>` | Override the resolved slug (testing only) |
| `--system-prompt <path>` | Use this file instead of the prompt compiled into the binary |
| `--log-file <path>` | Tee logs to a size-rotating file as well as stderr |
| `--no-context` | Disable retrieval for the daemon's lifetime |
| `--debug-context` | Log the full content of every retrieved chunk |
| `--no-rerank` | Bypass fusion and class re-ranking; raw similarity order |
| `--credentials-from-stdin` | Read one JSON line of credentials from stdin at startup — `{"api_key", "api_base", "provider", "proxy_key"}`, all optional — instead of a key in the environment, which stays readable in `/proc/<pid>/environ` for the daemon's whole life. The VS Code extension starts the daemon this way. `MOCHIII_API_KEY` in the environment still wins. A malformed line, or none within 5 s, is fatal; stdin is never read again. |
| `--no-scrub` | Disable the HEURISTIC secret scrubber (prefixed key shapes). It does **not** disable literal redaction of this daemon's OWN credentials — the key it holds, and its encodings, are always removed from tool results, chunks, prompts and logs, and an outbound web call carrying one is always refused. That is exact-value matching with no false positives, so there is nothing to silence. |

---

## Configuration

Credentials and model selection live in two different places, deliberately.

### Environment — credentials only

The daemon reads plain environment variables and never parses `.env` itself.

| Variable | Meaning |
|---|---|
| `MOCHIII_API_BASE` | Base URL of an OpenAI-compatible API. **Optional** — unset, the daemon defaults to `https://openrouter.ai/api/v1` and logs that it did (`daemon/main.go:142`). A malformed value is still fatal. *Required in proxy mode*, where the address cannot be guessed. *(This row read "**Required** — the daemon refuses to start without it" until 2026-09-21. That stopped being true on 2026-09-15, when `22b3021` gave the daemon a default — the fix for the item-41 first-run outage.)* |
| `MOCHIII_API_KEY` | Sent as `Authorization: Bearer`. May be unset for local servers that need no key, or when a key has been stored by `mochiii-daemon connect` — this variable **takes precedence** over the stored one, and over a key handed over on stdin (`--credentials-from-stdin`). A variable the daemon starts with is readable in `/proc/<pid>/environ` for its whole life; a launcher that can should use stdin, as VS Code does |
| `MOCHIII_MODEL` | One model, named the way the server at `MOCHIII_API_BASE` names it (e.g. `qwen2.5-coder:7b` for Ollama). **Replaces every tier in `models.json`** — for a local server, which knows none of those names. VS Code: the `mochiii.model` setting |
| `MOCHIII_USE_PROXY` | `true` selects proxy mode |
| `MOCHIII_PROXY_KEY` | Per-user Mochiii key; required in proxy mode — or `proxy_key` on stdin, which this variable outranks |

**A model server on your own machine** (Ollama, LM Studio, llama.cpp, vLLM) needs
no key: `MOCHIII_API_BASE=http://localhost:11434/v1` and
`MOCHIII_MODEL=qwen2.5-coder:7b`. A local server also gets five minutes, not
sixty seconds, before its first token — a CPU model reading a long prompt is
silent, not stalled.

A stored credential (`mochiii-daemon connect`) fills in only what the
environment did not say, **and only for the address it was saved for** — a
stored OpenRouter key is never sent to a local server. It is deliberately
**ignored in proxy mode**: what
`connect` stores is a *provider* key, and proxy mode authenticates with a
*Mochiii* key, so filling one in from the other would send a credential to a host
it was not issued for.

`MOCHIII_USE_PROXY` is load-bearing, not cosmetic: it is what makes the
daemon authenticate with the Mochiii key instead of the provider key, and startup
fails if the Mochiii key is empty. **In proxy mode, leave `MOCHIII_API_KEY`
unset** — with both set the daemon warns, because it would otherwise send a
provider key to a proxy that neither wants nor can use it.

### `models.json` — model tiers

The model slug lives in [`models.json`](../models.json) rather than an environment
variable, since it is not a secret and is useful under version control.

```json
{
  "config_version": 1,
  "default_tier": "deepseek_v4_pro",
  "tiers": {
    "primary":         { "slug": "deepseek/deepseek-v4-flash", "active": true },
    "minimax_m3":      { "slug": "minimax/minimax-m3",         "active": true },
    "deepseek_v4_pro": { "slug": "deepseek/deepseek-v4-pro",   "active": true }
  }
}
```

Nine tiers ship active, defaulting to `deepseek_v4_pro` (DeepSeek V4 Pro). It was
chosen by measurement, not by price: on the 14-task eval it passed 50 of 56 trials at a
median of 51 seconds a task. DeepSeek V4 Flash (`/model primary`) costs about a fifth as
much per task, but could reach at most 38 of 56 and took 4–5 times as long. See
[`docs/AGENT_WORKFLOW_EVAL.md`](AGENT_WORKFLOW_EVAL.md).
`ghost_text` and `reasoning` ship inactive. Startup fails fast, with a specific
error, if the config is missing or malformed, or if any tier marked `active` has
a missing or empty slug.

A tier can also carry two settings of its own:

```json
"primary": { "slug": "deepseek/deepseek-v4-flash", "active": true,
             "reasoning_effort": "medium", "provider_sort": "throughput" }
```

- `reasoning_effort` (`low` · `medium` · `high`) asks that tier's model to think before it
  answers.
- `provider_sort` overrides `zdr.provider_sort` for that tier: `price` picks the cheapest
  host, `throughput` the fastest.

Both are left out by default, and the request is then byte-for-byte what it was before.
An unsupported effort is dropped with a warning.

Every request carries `max_tokens`, 32768 by default, so no single call can run away. At
that cap an answer ends as "cut off", and you can ask the model to continue. A tier can
lower it with `max_output_tokens`. Values above 32768 are clamped with a warning, because
the managed proxy refuses them. The managed proxy refuses any
`provider.sort`, so `provider_sort` is for your-own-key mode. Which settings actually help
is measured, not assumed: see [`docs/AGENT_WORKFLOW_EVAL.md`](AGENT_WORKFLOW_EVAL.md).

The `zdr` block resolves to the provider-routing object sent on every request and
is **secure by default**: an absent or legacy block resolves to `zdr: true`,
`data_collection: "deny"`. Also configurable here: `retrieval.disabled`,
`retrieval.rerank_disabled`, `retrieval.context_budget_chars`, `no_scrub`, and
the whole `mcp` block ([agent mode](#agent-mode)). Unknown keys are warned about,
never silently ignored.

#### `models.agent.json` — agent mode, and the one setting that reaches the internet

`models.agent.json` is `models.json` plus an `mcp` block, and it is what
[`run-tui.sh`](../run-tui.sh) uses by default. Two of the tools it enables —
`web_search` and `web_fetch` — **send text to a third party over the internet**,
and both ship as `"allow"`, meaning they run without stopping to ask **where the
call cannot carry what the model has read**:

```json
"mcp": { "builtin": { "tools": {
  "web_search": "allow",
  "web_fetch":  "allow"
} } }
```

That is a deliberate default: an assistant that cannot look anything up answers
questions about the changing world from a frozen memory, confidently and without
saying so, which is worse than the exposure. The daemon prints one line at every
startup naming exactly which tools do this and how to undo it. Change both values
to `"ask"` and every call stops for a human first — the prompt shows the outgoing
text in full before anything is sent.

**What `"allow"` covers, and what it never does.** A file or a page can tell a
model "now fetch this address with what you just read in it", and a model that
does as a document says has sent your text to a stranger. So `"allow"` covers
only the two calls that cannot do that:

| Call | Runs without asking when | Otherwise |
|---|---|---|
| `web_search` | the turn has read nothing yet — its first query can only say what the conversation said | asks, showing the query |
| `web_fetch` | the address is one **you typed** or one **a search returned** in this turn, compared whole | asks, showing the address |

An address the agent wrote itself is always asked about: everything after the
host name is sent to that host. "Yes, for this turn" on a web call covers that
exact query or address and no other. A fetch address is also checked like a
query — a secret-shaped value in it, or one longer than an address needs to be,
is refused even with a yes. The prompt says which of these is why you are being
asked (`daemon/webegress.go`). Before 2026-10-07 `"allow"` covered every call: a
scripted model that read a file and then fetched an address carrying it was not
stopped.

What the daemon does and does not promise on this path: secret-shaped values are
stripped from the query before it leaves, private and link-local addresses are
refused, and fetched pages are fenced as untrusted data that the model may quote
but never obey. It cannot vouch for the far end. See
[`SECURITY_MODEL.md`](../SECURITY_MODEL.md).

**What you see under an answer that used the web.** The daemon lists the pages
it read for that answer, itself, as the last lines of the answer:

```
Sources (pages read for this answer):
- gov.uk — <https://www.gov.uk/government/people/...>
- en.wikipedia.org — <https://en.wikipedia.org/wiki/...>
```

It says *read*, not *used*: the daemon knows which pages it handed the model,
not which sentence came from where. It shows addresses and never page titles (a
title is a stranger's sentence, and the block is saved with the answer), at most
five, with anything a terminal or a renderer would act on percent-encoded
(`daemon/websources.go`). The block is part of the saved answer, so a chat opened
later is as checkable as it was on the day.

The model is asked to name its sources in words and to write no reference marks.
Models trained with a browsing tool write them anyway — `【0†L1-L4】`, an address
or a site's name inside those brackets — and no client here can open one. So the
daemon takes them out of the answer as it streams (`daemon/citemarks.go`): a
pointer is removed, and anything written in words inside the brackets — an
address, a name — is kept, in ordinary parentheses. `【 】` are everyday
punctuation in Chinese, Japanese and Korean, so a conversation with any of those
in it keeps its brackets, apart from pointers. Code blocks, inline code and edit
blocks are never touched.

Measured on 2026-10-07 with one model (`nvidia/nemotron-3-super-120b-a12b`),
twenty questions, three runs before and five after (one of those the fact
questions only). Before, 16 of 24 answers about current facts held such
brackets; over all the runs the model wrote them in five shapes. The new wording
alone did not stop them — the model went on writing them in about half its
answers — so the filter is what removes them: the last run had none on screen,
and the 128 answers recorded before it, replayed through the filter, have none.
Every answer that used the web ended with the block (41 of 41), and a scripted
run against the real web confirmed the block lists exactly the pages the model
was handed. Answers got shorter: the median fact answer went from 363 to 232
characters, the median answer to a coding question from 1,452 to 504. The cost
is 242 more input tokens on every model call (4,139 to 4,381 for the opening).

### Slash commands

Type `/` in the TUI or the VS Code chat for the menu. Local commands run in the
client; steered commands send a task preamble to the model, and the long-task
commands send a mode the daemon enforces ([Long tasks](#long-tasks)).

```text
/model                 # TUI: choose the model from a list (VS Code: prints the tiers)
/model list            # TUI: print them instead
/model minimax_m3      # select one by name; the TUI's choice is remembered across restarts
/model clear           # back to the default

/provider              # TUI: choose a provider, paste its key, choose a model
/provider nvidia       # skip the list (TUI); or /provider <its API address>
/account               # TUI: the provider, key and model in use, and what was spent
/connect               # the same screens as /provider, under their older name
/connect show          # which key is in force (masked — never the key itself)
/connect forget        # remove the stored key

/mcp-server            # the tools the running daemon offers, and its MCP servers
/mcp-server add files npx -y @modelcontextprotocol/server-filesystem ~/notes
/mcp-server remove files   # (TUI) add or remove an MCP server of your own

/spec add a --verbose flag   # write a spec (specs/<name>.md) and review it
/spec build            # build the active spec: tests first, live task list
/spec check            # grade the project against every success criterion
/spec use <name>       # /spec off, /spec show -- choose the active spec

/team <question>       # researcher -> coder specialists (/team:planner,coder …)

/debug <issue>         # long task: reproduce, localize, fix at the root, verify
/fix <bug>             # long task: reproduce, fix, verify
/refactor <change>     # long task: green baseline, small steps, each built and tested
/hunt <area>           # long task: real bugs only, each proved by a failing run
/task <goal>           # any long task
/task                  # where it stands · /task resume [note] · /task review · /task discard
/task budget 45m $1 200  # this session's budget: minutes, dollars, model calls

/usage                 # tokens and cost this chat/session, and how full the context is
/budget                # this session's and today's spend against their limits
/budget more           # allow another allotment of a limit that is near or reached
/save [name]           # keep this chat; saving again later updates it
/resume my-name        # continue a saved chat, by its name or its number (/resume 2)
/resume                # which chats there are to continue
/history               # the same list; ⚠ marks work left half done
/history 2             # read saved chat 2
/history delete 2      # delete it (its number, or its whole name)

/remember tests here are run with make check   # (TUI) keep a rule for this project
/rules                 # (TUI) the saved rules, numbered, and the file they are kept in
/forget 2              # (TUI) remove rule 2
/github                # (TUI) Mochiii's GitHub account and this project's repository; /github connect
/issue 104             # (TUI) an issue and its comments, attached to your next question
/pr 31 log             # (TUI) why a pull request's CI failed, attached to your next question

/help /mcp-server /explain /test /doc /security
/review /plan /run /clear /compact /context /git /init /search /exit
```

**Usage.** `/usage` answers from the provider's own bill, not an estimate. It shows
tokens in (and how many were cached), tokens out, and dollars, for this chat and for
the whole session. It also shows the size of the last context sent against the model's
window, taken from `context_window` in `models.json`. It answers locally, so it costs
no model call. A bare `/word` that is not a command is answered locally too, instead
of being sent to the model.

**Budget.** The daemon adds up what every model call bills — tokens always, dollars
where the provider reports them — against two limits: this **session's** (one run of
the client) and the **day's** (every project on this machine). The defaults are
3,000,000 tokens or $1 a session and 15,000,000 tokens or $5 a day, set under
`mcp.budget.spend`. At 80% you are told once. At a limit Mochiii **stops and asks**:
the turn in flight ends with a summary of what it did, and the next question is
refused before it reaches the model, until you type `/budget more` to allow another
allotment. A long task stops the same way and is saved for `/task resume`. `/budget`
shows where both stand; like `/usage`, it costs no model call. The VS Code client
sends no session yet, so only the day's limit holds it, and it is raised there in
the configuration.

**Chat history.** A chat is kept only if you save it with `/save` (`/save my-name` to
name it), so disk use stays your choice. `/clear` and ctrl+n both start a new chat and
discard the current one: it leaves the screen, it is no longer sent to the model, and a
later `/save` will not write it. On a chat that is not saved, either one asks first, in
the prompt's box — **"This chat is not saved. Save or clear?"** — and one key answers:
`s` saves the chat and then starts the new one, `c` clears without saving, Esc keeps the
chat. If the save fails, nothing is cleared. Relaunching continues where you left off, and
says so in one line: when the chat began, how many turns it holds, how many of them are
on screen, and whether it is saved. A chat goes on until you end it, however long ago
it began. Everything stays on this machine:

- **Where:** saved chats live in `~/.local/state/mochiii/history/`, one gzipped file per
  chat, never inside the project.
- **Which chat:** `/resume`, `/history` and `/history delete` take the chat's number
  in the list or the name it was saved under (`/save test1`, later `/resume test1`).
  Case does not count, and the start of a name is enough to read or resume when it
  fits only one chat; deleting needs the whole name. If two chats share a name, both
  are shown and the number decides. `/history save` and `/history resume` are the
  same two commands under their older names.
- **Saving again:** after more work, `/save` updates the chat's saved copy
  instead of adding a new one, and an update only ever adds: the copy keeps every
  turn it already held. `/resume` keeps the saved copy, and asks first
  if the chat on screen is not saved.
- **How much is kept:** at most 50 chats per project and 20 MiB for the whole folder.
  If a save pushes out the oldest, it tells you. One saved chat holds at most 8 MiB of
  text. The chat in progress keeps its newest 1000 turns and is never trimmed by age.
- **Half-done work:** `/history` flags it with ⚠: a last answer that did not finish, or a
  spec that still has open criteria.
- **Deleting:** `/history delete <n>` removes one chat. To remove all of them, delete the
  folder.

The `/spec` family is the spec-driven workflow — decide what to build, build to it,
check the result against it: [`docs/SPEC_WORKFLOW.md`](SPEC_WORKFLOW.md).

**Which to use**, measured on the task-success eval with hidden tests
([`docs/AGENT_WORKFLOW_EVAL.md`](AGENT_WORKFLOW_EVAL.md)):

- **With a spec active, just ask.** An ordinary turn passed 12/12 spec tasks, and
  `/spec build` passed 10/12.
- **`/team` when one more solved task is worth the price.** It passed 24/28 against a
  single agent's 23/28: one trial ahead, at 1.5× the tokens and 2.3× the time.

`/connect` is the in-client half of [`mochiii-daemon connect`](#4-run). It
takes the key at a masked prompt rather than as an argument, because an argument
would be left in the transcript and sent on with your next prompt. In both
clients the daemon verifies the key, stores it and starts using it **without a
restart**. A key from whatever started the daemon — `MOCHIII_API_KEY` in its
environment, or a key VS Code handed it on stdin from SecretStorage — still wins
over a connected one; VS Code then drops its own key and restarts the daemon so
the connected one takes over, and `/connect show` says when an environment key
is what is in force.

**Provider, key, model — three screens (TUI).** `/provider` (or `/connect`)
opens a list of the providers; type to search, Enter to pick. The next screen
takes that provider's key at a masked prompt and says where it is about to be
sent. The daemon checks it — for any provider but OpenRouter by finding one of
its models that answers, see [direct mode](#4-run) — and the last screen lists
the provider's models. Enter on another makes it the model; Esc keeps the one in
use. The same three screens are what the client **opens on** when no key is in
force; Esc there skips to the chat.

- **Each screen marks where you are.** The provider in use and the model in use
  are marked `● (current)`, and each list opens on it.
- **`/model` opens the model list by itself**, with no key to paste: it is the
  third screen, for the provider already in use. `/model list` prints the
  models as text instead.
- **Changing the model does not ask for the key again.** Pick the provider you
  are already on and press Enter on the empty key field: the key in use is
  kept, and the model list follows.

**`/account` (TUI)** shows what the client is connected to: the provider and its
address, the key in force (masked) and where it comes from — stored on this
machine, the daemon's environment, or the proxy — the model, whether a restart
will remember it, and this session's and today's spend against their limits. It
asks the daemon only; nothing is sent to the provider.

**The prompt is drawn in a box (TUI)**, the width of the terminal, so where to
type is set apart from the conversation above it. Its border is blue while the
client is waiting for you — a prompt to type, a tool approval to answer — and
grey while a turn is running. A terminal under 12 rows keeps the bare prompt
and gives the rows to the answer.

**The line under the prompt (TUI)** names the model and the provider all the
time, and adds the session's tokens and cost once there are any:
`z-ai/glm-5.3 · NVIDIA · 3.8K tokens · $0.0027`. It reads the daemon's own
answer about what it is sending to, so with a key in the environment it names
that key's provider, not the stored one's.

At the right end of the same row is who is running the client and on which
machine, as a shell prompt writes it: `ravi@laptop`. The name is read from the
operating system when the client starts and is only drawn on that row — it is
not sent to the daemon or to any provider. The line comes first: in a narrow
terminal the name is cut, or left out, before the line is shortened.

- **Paste a key from any major provider and it is ready** is still one Enter
  away: the list's first row is "Any key", which asks for the key and nothing
  else, and the daemon recognises the provider from it.
- **A key is only ever sent to the provider that issued it.** Pick Groq and
  paste a key that starts `nvapi-`, and it is checked with NVIDIA; the screen
  says so. A key with a bare `sk-` prefix, pasted under "Any key", is sent
  nowhere: the list comes back cut down to the providers it could be from.
- **A refused key is refused by name** ("refused by NVIDIA"), changes nothing,
  and leaves you on that provider's key screen to try again.
- **The model you pick is remembered.** It is stored beside the key, so it is
  still the model after a restart, and `/model <name>` is remembered the same
  way. `/model clear` goes back to the model the daemon found. With a key from
  the environment there is no stored key to keep it beside, so the choice
  lasts until the daemon stops — and the client says which you got.
- **Nothing pasted into the provider list is shown.** A paste there is as
  likely as not the key, so it is not taken.

`/provider <provider>` or `/provider <address>` skips the list. VS Code's
`/connect` takes the key only, and has none of these screens, `/provider` or
`/account` yet.

**An answer is drawn as markdown, in blue and grey (TUI).** A model writes
markdown, and the TUI draws it instead of printing its punctuation:

| In the answer | On screen |
|---|---|
| headings, `**strong**`, a table's header row, list markers | blue, bold |
| `*emphasis*`, `` `code` ``, links | steel blue |
| the answer's own words | grey |
| table borders, rules, quote bars, code fences | dim grey |

A table is boxed to fit the terminal; when it cannot fit without breaking its
words it is listed row by row instead, with nothing dropped. A link shows its
address after its text. **Code blocks and edit blocks are shown character for
character** -- what you copy out of the terminal is what the model wrote. Text
that only looks like markdown is left alone: `5*3 = 15`, `snake_case`,
`__init__`, `**kwargs`. `NO_COLOR=1` turns the colour off and keeps the layout.
The output of local commands (`/help`, `/git`, …) is not markdown and is shown
as written. None of it is part of the conversation: the model is not shown
`/help` as something it said. What `/git` and `/search` print is the exception
with a purpose -- it is sent to the model **once**, with your next question,
marked as command output rather than as your words or its own, so "write a
commit message for this" works without the file names in a repository being
replayed as the model's voice on every turn after.

---

## How it works

### The shape

```
protocol/        wire types + version handshake + socket discovery
editapply/       the SEARCH/REPLACE apply engine (parse, confine, verify, back up)
daemon/          the long-running process: index, retrieval, memory, agent loop
helper/          embedder subprocess — CGO is confined entirely to here
clients/tui/     the chat TUI, and the one-shot CLI path
clients/vscode/  the VS Code extension (TypeScript)
proxy/           the managed-tier proxy in front of OpenRouter
```

`go.work` ties the first five Go modules together. **`proxy/` is a separate
module with zero third-party dependencies, deliberately** — it is the only
internet-facing component. `mcp-servers/` is still a stub; the built-in tools
live in the daemon.

### A prompt's journey

```
  TUI / VS Code / CLI
        │  ① handshake → PersistedHistory
        │  ② PromptRequest
        ▼
  ┌──────────────────────────────────────────────┐
  │ DAEMON  (your machine)                       │
  │  ③ hybrid retrieve: vectors + FTS5, RRF-fused│
  │  ④ scrub secrets from prompt and chunks      │
  │  ⑤ wrap in <retrieved_context> delimiters    │
  │  ⑥ stream GroundingInfo to the client        │
  └──────────────────────────────────────────────┘
        │  ⑦ one inference turn (agent mode: one per step)
        ▼
  ┌──────────────────────────────────────────────┐
  │ PROXY (managed mode only)                    │
  │  auth · ZDR gate · model allow-list · quota  │
  └──────────────────────────────────────────────┘
        ▼
   Model provider ──⑧ SSE tokens, streamed back unbuffered──▶ client
```

Everything above the inference call stays local: indexing, embeddings, retrieval,
memory, and backups never leave the machine.

### Transport

**No TCP and no network listener** — a local socket is the only surface.

The daemon binds a Unix domain socket at `0600` inside a `0700` per-user runtime
directory, or a named pipe on Windows, and writes a JSON lockfile beside it
recording the address and PID. Clients read that lockfile; there is no other
discovery mechanism and nothing is guessed. Connections are peer-authenticated
against kernel-supplied credentials — `SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on
macOS — and a peer whose UID is not yours is refused.

Messages are newline-delimited JSON: `HandshakeRequest` → `HandshakeResponse`
(carrying `PersistedHistory`; the connection closes here on a protocol-version
mismatch) → `PromptRequest` → `GroundingInfo`, then `TokenResponse` messages
until one carries `done`. Exact structs: [`protocol/protocol.go`](../protocol/protocol.go).

### Retrieval

`index` walks the workspace **confined to its root**, never following symlinks,
and chunks eligible files into overlapping ~40-line windows. Each chunk is
embedded locally and written to **two** stores under
`<workspace>/.mochiii/index/`: a [chromem-go](https://github.com/philippgille/chromem-go)
vector collection and a SQLite FTS5 table.

**Retrieval is hybrid, in three stages** ([`daemon/rerank.go`](../daemon/rerank.go)):

1. **Two candidate pools** — semantic nearest-neighbours, and BM25 lexical
   matches for the same query. Both over-fetch well past `k`, because reweighting
   only the top `k` could never recover a chunk ranked just outside it.
2. **RRF fusion** — reciprocal rank fusion (`rrfK = 60`) merges the two rankings.
   Vectors find "the thing that means this"; FTS5 finds the exact identifier you
   typed. Fusion is what stops either one from being the only chance.
3. **Class re-ranking** — every chunk is tagged `code` · `other` · `config` ·
   `doc` at index time and weighted `1.15` · `1.00` · `0.90` · `0.75`. This is a
   **tilt, not a ban**: a doc with high enough raw similarity still wins.

The lexical tier is **optional at runtime** — if the FTS5 store fails to open or
a search errors, that call degrades to semantic-only rather than failing.

**What is never indexed:** anything that could carry a secret (`.env*`, `*.pem`,
`*.key`, `id_rsa*`, `.aws/`, `.ssh/`, any name containing "secret" or
"credential"), plus `.git/`, `node_modules/`, `vendor/`, build output, binaries,
files over 1 MB, and anything matched by a `.gitignore` at any directory level.
Lockfiles and `.gitignore` itself are excluded as pure noise. Every exclusion
runs through a single gate called immediately before a file is read, so no
walk-time prune can drift from what actually gets read.

**Guards worth knowing about:**

- **Stale-index guard** — both embedders report 384 dimensions, so dimension
  alone cannot distinguish a placeholder-built index from a real one. Every build
  stamps the embedder's ID and schema version; `retrieve` refuses with
  "re-index required" on any mismatch, including a stamp missing entirely.
- **No accidental network calls** — the vector collection is created with an
  embedding function that refuses to run, so a future bug that left a chunk
  unembedded fails loudly instead of silently calling a hosted embedder.
- **Retrieval never blocks a request.** No index, a stale index, a dead embedder,
  or a store error all degrade to answering from the bare prompt, with one line
  logged and the reason sent to the client as `GroundingInfo`.

Retrieved chunks are wrapped in `<retrieved_context>` delimiters in the **user**
message, never the system role, and capped by a character budget (default 8000).
Chunk text is untrusted, so tag-like text inside it is neutralised before
rendering — a file cannot forge an early close and inject instructions.

**When code is attached before the model starts** depends on the mode:

- **Plain mode (no tools)** is searched up front, except for greetings and
  questions about Mochiii itself ("what is your name"). A short follow-up
  ("continue", "ok fix it") is searched together with the question it follows.
- **Agent mode** gets no code searched on its behalf. The agent has `search_code`,
  which runs this same retrieval with a query it chooses, and calls it when it
  needs code. Searching up front on the user's words attached about 8K tokens to
  every turn at full price, whether or not the turn needed it. Only exact
  `file:line` references in the prompt (`foo.go:142`) are attached up front.

### Editing

The model is instructed to propose changes as SEARCH/REPLACE blocks:

```
path: relative/path/to/file.go
<<<<<<< SEARCH
(exact existing lines to find)
=======
(replacement lines)
>>>>>>> REPLACE
```

**The daemon's chat path only parses and logs — it never writes to disk.**
Applying goes through [`editapply/`](../editapply/), one implementation shared by the
CLI and by in-chat review, so no surface has its own weaker copy. Five gates, in
order:

| # | Gate | Refuses |
|---|---|---|
| 1 | **Path safety** | Anything resolving, through symlinks, outside the workspace root; absolute paths; `..` escapes; files the indexer would secret-skip |
| 2 | **Exact match** | `SEARCH` text not found, or found more than once — ambiguous is a refusal, never a guess |
| 3 | **Syntax** | A `.go` edit that **breaks a file which parsed before it**. Judged on the delta, not the result — an edit to an *already*-broken file is allowed, and the note says the file did not parse beforehand either, so a syntax error stays fixable through edit blocks. Creating a file is judged absolutely, because there is no “before”. **Go is the only language with a parser in this binary** — every other file type is written unchecked, and the note beside the diff says so verbatim. On a non-Go repository this gate does not fire, so the list below is four gates, not five |
| 4 | **Confirm** | Everything except a literal `y` at the diff prompt |
| 5 | **Backup** | Nothing — it records pre- and post-edit content under `.mochiii/backups/<ts>/{before,after}/` before the real write |

**Nothing is applied without a human decision.** In the CLI and the TUI that is an
explicit `y` per edit. The VS Code panel additionally offers a session-scoped
auto-apply which the user turns on deliberately; it drives the *same* per-block
path a manual click would, so all five gates still run on every block
([`chatPanel.ts`](../clients/vscode/src/chatPanel.ts)). What no surface has is an
edit reaching disk that the user did not ask for.

**Creating a file** is the same mechanism, not a second one: a block with an
**empty `SEARCH`** means "this file's content is, or should be, nothing", so
`REPLACE` is written as the whole file ([`editapply/create.go`](../editapply/create.go)).
It goes through the identical gates — confinement, the syntax check, your
confirmation. Two behaviours are worth knowing:

- **It will not clobber.** An empty `SEARCH` against a file that already exists
  and is non-empty is **refused**, with an error telling the model to send a real
  `SEARCH` section instead. An existing but *empty* file is filled, because
  that is the same intent.
- **The syntax gate applies to creation too**, deliberately. Without that, a
  model whose edit was refused for not parsing could land the identical bytes by
  resending them as a creation. It is *absolute* here where the edit path judges
  the delta, and that asymmetry is the same decision rather than an exception: an
  empty `SEARCH` means the file's content is, or should be, nothing, and nothing
  has no prior brokenness to inherit. (An empty `.go` file does not parse, so a
  delta rule on this path would read every creation as a repair and excuse
  anything.)

`edits undo` restores a session, comparing current on-disk content against the
`after/` snapshot first. A file modified since is **never silently clobbered**:
it is listed as guarded and restored only with `--force` or an explicit
confirmation naming it.

### Agent mode

**Off by default.** With `mcp.enabled` unset, the outbound request body is
byte-identical to what it was before this existed. With it on, the daemon runs a
bounded loop — model → tool → model — until the model stops asking or a ceiling
stops it.

Two trust lanes, and **which map a server sits in *is* its lane** — there is no
`lane:` field to mistype:

- **`mcp.builtin`** — tools compiled into the daemon, confined by the same
  resolver that gates model-proposed edits. **None of them writes:**
  `propose_edit` files a change into the normal five-gate review.
- **`mcp.servers`** — MCP servers you configure, spawned as stdio subprocesses.
  Ordinary programs with your full access. **Not sandboxed**, and each needs
  `acknowledged_unconfined: true` before it resolves to anything runnable.

**Adding an MCP server of your own.** In the TUI, with any stdio MCP server:

```text
/mcp-server add files npx -y @modelcontextprotocol/server-filesystem ~/notes
/mcp-server add github --env GITHUB_TOKEN npx -y @modelcontextprotocol/server-github
/mcp-server                 # what the daemon offers now, yours included
/mcp-server remove files
```

(`mochiii-daemon mcp add <name> [--env NAME] [--in-project] -- <command> [args…]` is the same
thing with no daemon running.)

- **It asks first, and you type the server's name.** The daemon answers an add
  with what the server will be able to do — run as you, unconfined — and
  stores nothing until the name is typed back. That is the same gate as
  `acknowledged_unconfined`, at the moment it applies.
- **Then it is tried.** The server is started once and asked for its tools, so
  the answer is either "it started and offers N tools" or "saved, but it did
  not start", with the reason. It is live from your next question: no restart.
- **It starts in a folder of its own, not in your project.** A launcher such
  as `npx`, `uvx` or `python -m` decides what to run partly from the folder it
  starts in — a project's `.npmrc` can name another registry — so a server you
  added once would otherwise be resolved by whichever project you opened next.
  Give a server the project's path as an argument, or add it with
  `--in-project` (`"cwd": "workspace"` in a config block) when it has to start
  there; the add says which you are agreeing to.
- **Its tools ask before every call**, like any tool with no policy listed.
- **The model is shown at most 24 tools at a time**
  (`mcp.budget.max_advertised_tools`), and the daemon's own come first. With
  `models.agent.json` as shipped those take 11, so the servers you add share
  13 places (until 2026-10-12 the limit was 12, which left them one). The add
  tells you how many of a server's tools will actually reach the model, and
  `/mcp-server` lists the ones left out. Each tool on the menu costs tokens on
  every request, and a wider menu measured slightly worse on a borderline one.
- **`--env` takes a variable's NAME**, passed through from the daemon's
  environment — export it, or put it in `.env`, before starting Mochiii. A
  `NAME=value` on the line is refused and kept out of the transcript; the
  command and its arguments are stored and shown, so no secret belongs there.
- **The command is a program's name or an absolute path.** A relative path
  would mean a different program depending on where the daemon was started.
- **Where it is kept:** `~/.mochiii/mcp-servers.json`, mode 0600, beside the
  stored key — never in the project. A repository cannot ship a server for
  Mochiii to run, and the agent cannot add one: it is refused that directory,
  and a sandboxed command can neither write there nor reach the daemon.
- **Agent mode must be on** for any tool to run (`./run-tui.sh` starts it on).
  With it off, an added server is stored and says it is waiting.

`/mcp-server` reports on **the daemon that is running**. It used to start a
second copy of the daemon to ask, which read whatever `models.json` it found
for itself — so a daemon started with `models.agent.json` was described as
"Agent mode is OFF" (fixed 2026-10-11). VS Code's `/mcp-server` still runs
`mcp list` from its packaged daemon, and has no `add`.

**The working copy.** A turn that edits or runs something does it in a private
copy of the project: its edits land there, `read_file` shows them and
`sandbox_exec` tests them, so the agent can fix what fails before you see
anything. When the turn ends, the copy's net difference becomes ordinary edit
proposals — your files change only when you accept them — and the client shows
whether the change was ever built or tested. **A change a command made is named
as the command's**: a test or a build script runs in the copy too, and if it
alters one of your files that alteration is offered beside the agent's own edits,
marked "changed by a command the agent ran, NOT written by the agent". A project
too large to copy (or `mcp.no_working_copy`) has no copy to run in; a command
there would run in the project itself, so it always asks first, whatever the
configuration says, and the prompt says "RUNS IN YOUR REAL PROJECT". See
[`docs/SPEC_WORKFLOW.md`](SPEC_WORKFLOW.md#the-working-copy--why-the-agent-can-test-its-own-work).

**What the agent can reach on your machine.** Not only the project:

| | Where | What you are asked |
|---|---|---|
| **Read** files, list folders | anywhere on the machine | yes/no for each path outside the project |
| **Create and edit** files | the project, and anywhere in your home folder (`~/Desktop`, `~/Documents`, …) | every change is shown as a diff; nothing is written until you accept it |
| **Delete, move, rename** | nowhere | there is no tool for it; the agent says so and gives you the command |
| **Run** commands | a private copy of the project | build and test programs (`go`, `npm`, `make`, `cargo`), not a shell |

Refused even if you say yes: reading private keys, credential stores and shell
histories; writing hidden files and folders, `~/bin`, `.desktop` launchers, or
anything outside your home folder. The agent is told exactly this list with every
turn (`daemon/machinereach.go`), built from the tools that turn really has: asked
"can you work all over my machine?", it used to answer "only inside the project".

A tool you do not list resolves to `ask` — the default is a question. `ask`
suspends the turn and shows you the tool, the **complete** arguments, its lane,
and which step this is. A timeout, a garbled answer, an answer to a different
call, and a closed client are all denials. An "allow for this task" grant covers
one tool for one turn and is never written to disk — and for a command or a web
call it covers exactly the command, query or address you were shown, not the
tool.

Per-turn budgets, all configurable, shown here at their defaults:

```jsonc
"budget": {
  "max_iterations": 8,             // model calls per turn, the closing answer included
  "max_turn_tokens": 600000,       // billed tokens per turn, prompt + completion
  "turn_timeout_seconds": 600,     // MACHINE time; your thinking time is added back
  "max_tool_result_bytes": 32768,  // per result, after scrubbing
  "max_total_tool_bytes": 131072,  // per turn, after scrubbing (models.agent.json ships 524288)
  "max_advertised_tools": 24,      // cap on the menu the model sees
  "spend": {                       // across turns: see /budget
    "session_tokens": 3000000, "session_usd": 1.00,
    "day_tokens": 15000000,    "day_usd": 5.00
  }
}
```

A turn that reaches a ceiling ends with one last call that answers from what was
read, and that call is counted inside the ceiling: `max_iterations: 8` is eight
model calls, not nine. Two things end a turn before any ceiling, because a model
that is getting nowhere should not be paid to reach one:

- **The same call three times with the same result**, and
- **five look-ups or edits in a row that brought nothing new** — files that are not
  there, searches with no match, edits that did not apply, content already read
  under another name. The third of them carries a note saying so.

And two things keep a turn from wandering in the first place. A `read_file` or
`list_directory` of a path that does not exist answers for the **whole project** —
the same name elsewhere, the closest names, or "no file of that name exists (N
files checked)" — so the model need not try the next path to find out. And
`grep`, the exact-text search, is offered in every turn that has room for it,
where it used to be a long-task tool: "where is X defined" no longer becomes
reading files one by one. Search by meaning (`search_code`) is offered only when
the project has an index for it to search.

A message that is only a greeting, a thank-you or a goodbye is answered without
tools, history or the long system prompt: a request under 1.5 KB where it was
17.5 KB.

Every decision — including `allow` calls you were never prompted about — is
appended to `.mochiii/logs/toolcalls.jsonl` (local, `0600`, rotated at
5 MiB). It records the **SHA-256 of the arguments and their length, never the
arguments themselves**: those are unscrubbed model output, and the digest is the
one your approval was bound to.

Full design and threat model: [`docs/MCP_MASTER.md`](MCP_MASTER.md) and
[`docs/MCP_LANE_B_THREAT_MODEL.md`](MCP_LANE_B_THREAT_MODEL.md).

### Long tasks

For work that does not fit in one turn: a long debugging session, a bug hunt, a
refactor across many files. `/debug`, `/fix`, `/refactor`, `/hunt` and `/task` run
in the terminal as **long tasks**. VS Code still sends its one-turn form of the
first three.

- **One request, many segments.** A segment is an ordinary agent loop of up to
  20 model calls. Each starts from a small fresh context: the same system message
  and tools every time, so the provider's cache carries over, plus your request
  and the task's **ledger**. The ledger holds the plan, the findings with their
  evidence, the files changed, the last check and a handoff from the previous
  segment. Files are simply read again when needed, so each call stays small
  however long the task runs.
- **One working copy for the whole task.** Later segments build on earlier ones'
  edits. You review every change once, when the task ends.
- **Finishing is a gate.** `finish_task` is accepted only after a passing build or
  test run with no edit since. `/hunt` is the exception: its proof is failing
  runs, recorded as findings. A finding without verifiable evidence (a command the
  task ran, or a `file:line` that exists) is refused. Once a finish is accepted,
  nothing else runs, not even a later call in the same reply. The summary the
  model gave `finish_task` is the reply you read, with no extra model call.
- **It stops on its own:**
  - when it finishes, or needs you and says what;
  - when it reaches its budget: 30 minutes, $0.50 and 150 model calls by default,
    changed per session with `/task budget` or in `mcp.budget.task`;
  - when two segments in a row change nothing (it is stuck);
  - when you press esc.
- **Saved as it goes.** The ledger and the diff are saved after every segment,
  under `~/.local/state/mochiii/tasks/`, never inside the project.
  - `/task resume [note]` carries on from the last saved point. Anything you add
    goes into the ledger.
  - `/task review` offers the saved changes without calling the model.
- **Narrower reach.** The web tools are withheld: an unattended run works on the
  local project, and a web page is text anyone could have written. "Yes for this
  turn" at an approval lasts the whole task.
- **Tools for the job,** in the long-task modes only, so an ordinary turn's tools
  are unchanged:
  - `grep`: exact or regex search, over only the files `read_file` could open;
  - `git_history` (log, show, diff, blame): read-only and hardened against a
    repository's own config. It never compares the working tree, and it withholds
    files `read_file` would refuse;
  - `read_file` line ranges, for large files;
  - commands that run up to 5 minutes (`exec_seconds`);
  - `checkpoint`: save the working copy's state under a name, and restore it to
    undo a hypothesis or a step that went wrong;
  - `rename_symbol`: a rename through the language server (gopls, …), across
    every file, all or nothing. It works from the project's own text, so rename
    before editing those files;
  - `investigate`: a short read-only sub-search that hands back only its answer
    (15 lines, with `file:line`), not everything it read.

  To make room under the menu cap of 16, long tasks leave out
  `query_compiler_definition` and `propose_ast_edit`; `grep` and `propose_edit`
  cover their work.
- **Long command output keeps its failures.** Output past the result cap used to
  be cut to its first bytes. It is now digested in every mode: its start, every
  line that reports a failure, and its end.
- **More test runners, only where confined.** List them in
  `mcp.builtin.extra_programs`, for example `["pytest", "node"]`. They run only
  on a host whose commands are sandboxed. Shells and the like are refused.

```jsonc
"budget": {
  "task": { "max_minutes": 30, "max_usd": 0.5, "max_calls": 150, "segment_calls": 20, "exec_seconds": 300 }
}
```

**Not yet shown to beat continuing by hand.** Round 4 in
[`docs/AGENT_WORKFLOW_EVAL.md`](AGENT_WORKFLOW_EVAL.md) compared long tasks
with the ordinary agent plus a user who types "continue", under rules written
before it ran. On 2026-10-04, at one trial per task, long tasks solved 3 of 6 and
the ordinary agent 4 of 6, so no advantage is claimed. Two of the three long-task
losses ended after two model calls with no tool call at all, on a provider host
that returned text without its tool calls; that defect is open. `/debug`, `/fix`
and `/refactor` are long tasks by design, whatever Round 4 finds: they are the
commands for a complete job. For a quick one-line change, ask without a command:
an ordinary turn costs less (3 model calls against 4 for the smallest fix,
measured).

### Conversation memory

The daemon keeps cross-session memory per workspace and delivers recent turns to
the client in the `HandshakeResponse`. Because the protocol is one prompt per
connection, the daemon cannot tell "fresh session" from "next prompt", so it
populates this on **every** handshake — acting on it is the client's job, and
only a startup connection should hydrate a transcript from it.

History sent to the model is re-validated and capped server-side: `user` and
`assistant` roles only, oldest first, inserted between the system message and the
final prompt. Retrieved context always lands in the final user message.

**The chat is shared by every client of the workspace.** One daemon serves the
terminal client and the VS Code panel alike, so both write to the same
conversation, and each keeps up with what the other added:

- The handshake carries a `chat_revision` alongside the hydrated turns. It is
  opaque to clients, which compare it and send it back.
- `{"chats":true,"action":"current","since":<revision>}` returns what the chat
  gained since that revision (`append: true`), or the whole chat after a new
  chat, a resume or a compact.
- The terminal asks when it gets focus and before it sends a question. The
  panel asks when it gets focus, or becomes visible again.
- A prompt carries the revision its history was built from. If the chat has
  moved on since, the daemon answers from the stored chat rather than from what
  the client sent, so the model never continues a conversation it was shown half
  of.
- A turn's final message says where the chat now stands, and whether another
  client wrote to it while the turn ran.

### Saved rules

A correction made in one chat used to end with that chat. A **rule** is one line
you choose to keep — a fact about the project, or how you want work done in it —
and every later chat in that project starts with it:

```text
/remember tests here are run with make check
/remember never edit the files under api/gen
/rules        # lists them, numbered
/forget 2     # removes one
```

- **Only what you save.** A rule is written when you type `/remember`, and at no
  other time. The model has no tool that writes one, and where its commands are
  confined they cannot reach the file.
- **The daemon adds them, every turn**, to the system message, inside a
  `<user_rules>` fence, and again right after your question: one model tested
  ignored a rule that was only in the system message and followed it beside the
  question. Because the daemon adds them, a rule saved in the terminal applies
  to the VS Code panel's turns too; the three commands themselves are in the
  terminal only for now. With no rules saved, what is sent is byte for byte what
  it was.
- **Kept outside the project**, in your own state folder beside the saved chats
  (`/rules` prints the path), in a file only you can read. A repository you
  clone cannot bring rules with it. You can edit the file by hand.
- **Bounded**, because every rule is sent twice with every question: at most 50
  rules, 300 bytes each, 4 KB together.
- **Not for secrets.** A rule goes to the model's provider with every question,
  so one that looks like a key or a password is refused, and whatever is in the
  file is scrubbed on the way out like any other text.

A rule is an instruction about the work. It does not change which tools the
model has or what needs your approval.

### GitHub

Mochiii reads the project's GitHub repository: issues, pull requests, and why
CI failed. Opening pull requests, answering review comments and merging are
planned and not built yet.

```text
/github connect   # paste a token into a hidden box
/github           # who Mochiii is on GitHub, and this project's repository
/issue 104        # an issue and its comments
/issues bug       # the open issues, optionally with a label
/pr               # the open pull requests
/pr 31            # one: its checks, reviews, and whether it can merge
/pr 31 log        # the lines that say why its CI failed, not the whole log
```

`/issue`, `/pr 31` and `/pr 31 log` attach what they show to your next
question, once, like `/git`. In agent mode the model can read the same things
itself with one read-only tool, `github`.

- **Give Mochiii its own GitHub account.** GitHub never lets the author of a
  pull request approve it, so a token for your own account would leave you
  unable to approve Mochiii's work; connect warns when that is what it was
  given. Create an account, invite it to the repository, sign in as it, and
  make a classic token with the scope `repo` only (Settings > Developer
  settings > Personal access tokens). An account invited to one repository
  reaches only that one, whatever the token's scopes say. Leave out
  `workflow`.
- **The token is the daemon's.** It is checked with GitHub before it is kept,
  stored in `~/.mochiii/github.json` (readable only by you), and never shown
  to the model, put in a command's environment, or logged. The secret
  scrubber knows both GitHub token shapes.
- **One repository per project**, read from the `origin` remote and pinned at
  connect. If `origin` changes later, `/github` says so and Mochiii keeps the
  pinned one until you run `/github use`.
- **Text from GitHub is not an instruction.** Anyone can write on a public
  repository, so what Mochiii reads there reaches the model fenced, scrubbed,
  and with each author's relationship to the repository beside their name.
  The tool takes only a number, a kind and a label, so nothing the model has
  read can be sent anywhere through it.

### Embedding helper

`BAAI/bge-small-en-v1.5`, int8-quantised ONNX, 384 dimensions, run through
onnxruntime in a separate binary the daemon spawns and talks to over its own
local socket, so the model's memory lives and dies with that process. No CGO:
`helper/internal/ort` loads onnxruntime at run time and calls its C API through
the function table, and `grep -r 'import "C"'` finds nothing anywhere.

BGE is asymmetric, and the daemon applies that asymmetry at the boundary:
documents embed unmodified, queries get BAAI's instruction prefix. The helper
itself is a dumb text-to-vector service with no notion of the distinction.

**One text per inference.** A request may hold many texts; the helper runs the
model over them one at a time, and runs one inference at a time whoever asks. Its
memory is therefore the size of one 512-token text — about 190 MB resident — and
not the size of the request. It used to pack a request into one tensor, and a
single save of a 3,000-line file (109 chunks, re-embedded together) cost 4.3 GB
that was never handed back; batching bought no speed on a CPU, so one at a time
is also about 18% faster. It makes a vector a function of its text, too: the
model is int8, its quantised layers take their ranges from the whole tensor, and
the same chunk embedded beside different neighbours used to come out as far
apart as cosine 0.994.

The daemon blocks on a real health RPC rather than a log line, respawns a dead
helper a bounded number of times, and on shutdown escalates `SIGTERM` → `SIGKILL`
and does not return until the process is reaped. Smoke-test the whole path with
`helper-smoketest`.

### Managed proxy

[`proxy/`](../proxy/) exists so the provider key never ships to end users. **It never
reads message content** — every gate inspects only the request envelope, and
`messages` stays an opaque byte slice forwarded byte-for-byte.

| Concern | Control |
|---|---|
| Identity | Per-user Mochiii key validated against Supabase |
| Privacy | Rejects any request whose ZDR routing flags are missing or weakened |
| Cost | Model allow-list; refuses `models`, `plugins`, `transforms`, provider cost-steering |
| Spend | Atomic quota reservation + a per-request token ceiling enforced mid-stream |
| Abuse | Request-rate, token-rate and in-flight caps, pre- and post-auth |

All fail closed. Error codes and configuration: [`proxy/README.md`](../proxy/README.md).

---

## Security posture

The full threat model — what each gate promises, what it does not, and the
accepted residuals — is [`SECURITY_MODEL.md`](../SECURITY_MODEL.md). The summary:

**What holds.** No network listener on your machine. Peer-authenticated local
socket at `0600`. Confinement to the workspace root through symlinks, with
atomic symlink-refusing writes on both the forward and undo paths. Five edit
gates with no auto-apply. ZDR routing enforced server-side by the proxy, which
returns `403 zdr_required` rather than trusting the client to ask nicely.

**What is explicitly *not* claimed:**

- **Agent mode's Lane B is unconfined.** A configured MCP server is an ordinary
  program running with your full privileges. The protection is that you approve
  every call and everything is audited — **not** that it is sandboxed. This is
  why `acknowledged_unconfined: true` must be typed by hand.
- **Secret scrubbing is heuristic and client-side only.** It matches a fixed set
  of *prefixed* patterns, so novel, obfuscated or unprefixed secrets are missed;
  it can be disabled; and the proxy cannot scrub, because it never reads content.
  Defence in depth, not a guarantee.
- **This daemon's OWN credentials are scrubbed literally, and that cannot be
  disabled.** Separately from the heuristic, the daemon knows the exact key it
  holds (started with, stored by `connect`, or set live) and removes that value —
  and its base64/hex/url-escaped/reversed encodings, and any 16-byte fragment of
  it — from every tool result, retrieved chunk, outgoing prompt and `--debug-context`
  log, and refuses any outbound `web_search`/`web_fetch` that carries it. This is
  exact-value matching: no false positives, so `--no-scrub` does not touch it. It
  still cannot catch a key transformed by arbitrary code (XOR, a cipher, gzip) —
  the defences for that are the read-tool denylist and the child-env allowlist,
  which keep the key out of reach rather than recognising it after the fact.
  The fixed set is prefixed key shapes: the keys of every provider `/connect` accepts
  (OpenRouter, OpenAI, Anthropic, Google, NVIDIA, Groq, xAI, Together, Fireworks,
  Cerebras, Hugging Face), AWS, GitHub, Slack, Stripe, Supabase, and private-key blocks.
  A generic `api_key = "…"`, a JWT, a password inside a URL and a short password are
  **not** recognised, on purpose: matching those corrupts ordinary code far more often
  than it catches a secret.
- **A stale index still reports `grounded ✓`.** Nothing records when the index
  was built, so the model can confidently describe the old shape of a file it
  "retrieved". The watcher keeps a running daemon current, but offline changes
  are invisible until you re-run `index`. This is the top known honesty gap.
- **Proxy rate limits are per-instance.** They are in-memory, so across N
  replicas the effective ceiling is N× the configured value. Quota itself is
  atomic in Postgres and *is* correct across replicas.

---

## Development

There is **no root module** — only `go.work` — so `go test ./...` from the repo
root fails. Name the six modules explicitly; these are the exact commands CI runs.

```bash
for m in daemon editapply protocol clients/tui helper proxy; do
  (cd "$m" && go build ./... && gofmt -l . && go vet ./... && go test -race ./...)
done

# VS Code extension (real Extension Development Host; xvfb-run if headless)
(cd clients/vscode && npm ci && npm run compile && npm test)

# Retrieval quality — needs the real model and CGO, so it is behind a build tag
# and an untagged `go vet` does NOT compile it.
(cd daemon && go test -tags eval -run TestEvalRetrievalQuality -v ./...)
```

### Make targets

`make check` runs the same gates as CI, so green locally means green there.

```bash
make hooks    # install tracked git hooks — once per clone
make check    # fmt, vet, race, lint, coverage ratchet, errcheck ceiling
make lint     # staticcheck + ineffassign + bodyclose
make ratchet  # per-package coverage floors
make fuzz     # 30s per target; FUZZTIME=5m to search harder
make drill    # mid-stream SIGTERM drill against the real proxy binary
```

**Ratchets only tighten.** [`scripts/coverage-floors.txt`](../scripts/coverage-floors.txt)
is raised to numbers measured on `main`, never lowered to make a red build green;
`scripts/errcheck-ceilings.txt` fails if the count moves in *either* direction, so
a new unchecked error is fixed rather than grandfathered.

**Stated plainly: `git push --no-verify` bypasses the pre-push hook, and nothing
in it can prevent that.** It guards against forgetting, not against deciding. It
exists as a compensating control because no branch protection is configured, so
CI reports but does not block. *(Until 2026-09-20 this said branch protection
was "unavailable on a private free-plan repo". The repository is now public and
the protection endpoint answers 404 "no rule configured" rather than 403
"upgrade" -- so it is available and unconfigured, which is a different thing and
an owner decision.)* The retrieval eval runs
only on `schedule`, on dispatch, or when a retrieval-path file changes, because
it is the expensive one — roughly 26 minutes. *(This paragraph also named "the
macOS matrix" until 2026-09-20. There is no macOS matrix: `cross` is
`os: [windows-latest]`, a literal one-element list.)*

### Retrieval eval set

[`testdata/evalset/`](../testdata/evalset) is a fixed 8-file sample codebase, with 15
natural-language queries in [`testdata/queries.json`](../testdata/queries.json)
phrased as *intent* rather than keyword copies of the code, each mapped to the
file that should be the top hit. The test prints a full per-query table so quality
is readable rather than pass/fail, and asserts top-3 recall against one named
constant (`0.80`) — tune it there, never by hand-picking queries.

> `queries.json` lives **outside** the indexed root deliberately. Indexing the
> query file makes a query's own text an unbeatable match against itself — a real
> contamination bug, caught and fixed by moving the file out.

---

## Limits

### Known platform gaps

**Intel Mac (`darwin/amd64`) is not supported.** Upstream onnxruntime v1.26.0
ships no prebuilt binary for it at all. This is a deliberate, open gap:
`download-model` and the helper both refuse with an explicit "Intel Mac is not
supported" error rather than a generic failure. Closing it most likely means
building onnxruntime from source. `linux/arm64` has been pinned since 1.1.0,
beside `linux/amd64`, `darwin/arm64` and `windows/amd64`
([`daemon/onnxruntimefetch.go`](../daemon/onnxruntimefetch.go)); Windows on ARM
runs the `windows/amd64` build under emulation.

**The VS Code extension has no macOS or ARM package yet.** Those platforms get
the terminal app. A macOS `.vsix` waits on Apple signing, because a package
VS Code downloads would be quarantined unsigned
([`scripts/release-targets.txt`](../scripts/release-targets.txt)).

### Out of scope, on purpose

| Area | Not done, deliberately |
|---|---|
| **Retrieval** | No *full* index build on start or per request — the running daemon's watcher does keep it current per file; no query rewriting; no multi-hop. Re-running `index` rebuilds chunk-by-chunk keyed by a deterministic ID rather than diffing |
| **Editing** | No *scored or approximate* `SEARCH` matching — the matcher normalizes (line endings, trailing space, indentation, unicode) and then matches exactly, and **ambiguous is always a refusal**, never a guess ([`match.go`](../editapply/match.go)). The replacement is then written back in the file's own line-ending convention, so an LF patch never leaves a CRLF file mixed ([`lineendings.go`](../editapply/lineendings.go)). No mid-stream parsing: edits are read from the completed response. A unified diff **is** read — each hunk becomes one SEARCH/REPLACE block and goes through the same gates — but it is never what the model is instructed to produce. File *creation* **is** supported — see [Editing](#editing) |
| **Agent mode** | No OS sandboxing of third-party servers — consent and audit are the protection, and the docs say so; stdio only, so no remote MCP and no new egress; tools only, no resources or prompts; no hot-reload of `models.json` (a server added with `/mcp-server add` is picked up at the next question); `/mcp-server add` in the TUI and CLI only; tool results never persist into history |
| **Skills** | **Removed.** `daemon/skills.go` and its CLI were deleted by `5453ba8` under decision D8 — the store had no callers. This row described the surviving plumbing until 2026-09-21; there is none |
| **Routing** | `ghost_text` and `reasoning` stay inactive; VS Code has no model picker UI yet |

---

## VS Code extension internals

### Architecture

- `src/daemonClient.ts` — extension-host-only module that owns the Unix
  socket: reads the daemon's lockfile, connects, frames/parses newline-
  delimited JSON, and exposes a small async interface
  (`preflightHandshake`, `streamPrompt`, `applyEdit`). This is a straight
  TypeScript reimplementation of `clients/tui/daemonconn.go` + `stream.go`
  (and, for `applyEdit`, of the daemon's `ApplyEditRequest` handler) — the
  wire format is already JSON, so no new dependency and no Go sidecar is
  needed. `streamPrompt`'s final `TokenResponse` may carry `edit_proposals`;
  `applyEdit` sends the accepted block back verbatim on its own connection
  and relays the daemon's `ApplyEditResponse` (applied + backup dir, or the
  exact gate-refusal string) — no gate is reimplemented in TypeScript.
- `src/chatPanel.ts` — creates the webview panel, keeps the conversation
  transcript (source of truth for building `PromptRequest.History`, mirrors
  `buildHistory` in `clients/tui/chat.go`), tracks the single pending edit
  proposal (cleared on apply, skip, or a new prompt so a lagging click can
  never target a stale block), and relays daemon events to the webview via
  `postMessage`. The webview cannot open a socket itself — all daemon I/O
  lives here.
- `media/main.js` — runs inside the sandboxed webview: renders the
  transcript, a grounding indicator, the edit-proposal diff (whole-block
  red/green, matching the TUI's non-LCS rendering) with Apply/Skip buttons,
  the apply result (applied + backup-dir hint, or the refusal reason), and
  forwards user input up via `postMessage`. No daemon access.

### Try it

```bash
# Terminal 1: start the daemon as usual (see repo root README)
cd /path/to/repo
set -a && source .env && set +a
./daemon/mochiii-daemon

# In this directory:
npm install
npm run compile
```

Then open this `clients/vscode` folder as a VS Code workspace and press
`F5` to launch an Extension Development Host. Run the command
**"Mochiii: Open Chat"** (Ctrl/Cmd+Shift+P). Any cross-session history
the daemon has for its workspace renders before you type anything; sending
a prompt streams the grounded answer back token-by-token.

If the model's answer includes a SEARCH/REPLACE edit block, the panel
renders it as a diff with **Apply**/**Skip** buttons right below the
answer. Apply sends the block to the daemon as-is; the daemon runs it
through `editapply.PrepareEdit` (exact-match, ambiguity-refuse, workspace
confinement, secret-file refusal, syntax gate) before writing + backing it
up, and the panel shows whatever the daemon reports — a success line with
the backup dir, or the exact refusal string, file untouched either way.
