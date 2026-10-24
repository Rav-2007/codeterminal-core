<p align="center">
  <img src="clients/vscode/media/icon.png" alt="Mochiii" width="128">
</p>

<h1 align="center">Mochiii</h1>

<p align="center">
  An AI coding agent that knows your codebase, in VS Code and in your terminal.
</p>

<p align="center">
  <a href="https://marketplace.visualstudio.com/items?itemName=MochiiiAIAgent.mochiii-vscode"><img src="https://img.shields.io/visual-studio-marketplace/v/MochiiiAIAgent.mochiii-vscode?label=VS%20Code%20Marketplace&color=e86a92" alt="VS Code Marketplace version"></a>
  <a href="https://marketplace.visualstudio.com/items?itemName=MochiiiAIAgent.mochiii-vscode"><img src="https://img.shields.io/visual-studio-marketplace/i/MochiiiAIAgent.mochiii-vscode?color=e86a92" alt="Installs"></a>
  <img src="https://img.shields.io/badge/platforms-Linux%20%7C%20Windows-555" alt="Platforms: Linux and Windows">
</p>

<p align="center">
  <a href="https://www.mochiii.in/">Website</a> ·
  <a href="https://marketplace.visualstudio.com/items?itemName=MochiiiAIAgent.mochiii-vscode">Install for VS Code</a> ·
  <a href="docs/REFERENCE.md">Docs</a> ·
  <a href="https://github.com/Rav-2007/codeterminal-core/issues">Report a bug</a>
</p>

---

Mochiii reads your project, answers questions about it, writes and fixes code,
runs your tests, and works through long tasks. It shows you every change before
it lands. Your code is indexed on your own machine, and you choose the model
provider that answers.

## Install

One command installs Mochiii's terminal app. On **macOS or Linux**:

```bash
curl -fsSL https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.sh | sh
```

On **Windows**, in PowerShell:

```powershell
powershell -ExecutionPolicy ByPass -c "irm https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1 | iex"
```

Or, with [Node.js](https://nodejs.org) 18 or later, the same command on every
system:

```bash
npm install -g mochiii
```

Then, inside a project folder:

```bash
mochiii
```

The first run asks which AI provider to use, for its API key and for a model,
and offers to index the folder so Mochiii can search your code.

The installer downloads the build for your computer from the
[latest release](https://github.com/Rav-2007/codeterminal-core/releases/latest),
checks it against the release's checksums, puts `mochiii` on your `PATH` and
downloads the local code-search model, once per computer. It needs no
administrator rights. The
[reference](docs/REFERENCE.md#install-update-and-uninstall) lists every option.

### VS Code

Install **Mochiii** from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=MochiiiAIAgent.mochiii-vscode),
or from a terminal:

```bash
code --install-extension MochiiiAIAgent.mochiii-vscode
```

Click the lotus icon in the Activity Bar, paste an API key when asked, and
start asking.

### Update and uninstall

To update, run the install command again (with npm:
`npm install -g mochiii@latest`). To remove Mochiii on macOS or Linux:

```bash
curl -fsSL https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.sh | sh -s -- --uninstall
```

On Windows, in PowerShell:

```powershell
$env:MOCHIII_UNINSTALL="1"; irm https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1 | iex
```

Your keys, models and saved chats stay in `~/.mochiii` unless you also pass
`--purge` (on Windows, set `$env:MOCHIII_PURGE="1"` too). Installed with npm,
run `mochiii --stop` in each project folder you used, then
`npm uninstall -g mochiii`.

## Features

### Knows your codebase

Mochiii indexes your project on your machine and finds the code that matters for
each question. The index updates as you save, so answers match the code you have
now.

### You review every edit

Changes show up as a diff. Apply or skip each one. Nothing is written until you
say so, and an applied change can be undone in one step.

### Agent mode

Mochiii can read files, search, run commands and tests, and look things up on the
web. Anything that changes a file or runs a command waits for your approval, and
an answer that used the web lists the pages it read.

### Long tasks

`/debug`, `/fix` and `/refactor` handle work that is too big for one reply.
Mochiii reproduces the problem, fixes it at the root, and finishes only when the
build or tests pass. `/hunt` looks for real bugs and proves each one with a
failing run. Every task has a time and spend budget, and you can pause it and
pick it up later.

### Build from a spec

`/spec` writes down what you want built. `/spec build` builds it tests-first, and
`/spec check` grades the result against the spec.

### Rules that stick

`/remember tests here run with make check` saves a rule for the project, and
every later chat follows it.

### GitHub

Read issues and pull requests, and see why CI failed, without leaving the chat:
`/issue 104`, `/pr 31 log`.

### MCP servers

Add any MCP server with one command. Its tools are ready on your next question.

```text
/mcp-server add files npx -y @modelcontextprotocol/server-filesystem ~/notes
```

### Spending limits

`/usage` shows tokens and cost from your provider's own bill. When a session or a
day reaches its spending limit, Mochiii stops and asks before going on.

### Files in VS Code

Attach PDFs (scanned ones too), Word, Excel, PowerPoint and images. They are read
on your machine.

## Works with any model

Pick one of these providers and paste its key. For most of them, Mochiii can tell
the provider from the key itself.

| | | |
|---|---|---|
| OpenRouter | OpenAI | Anthropic |
| Google Gemini | NVIDIA | Groq |
| DeepSeek | xAI | Mistral |
| Together | Fireworks | Cerebras |
| Moonshot | Hugging Face | SambaNova |
| DashScope | Z.ai | |

Or keep everything on your computer with a local model server: Ollama,
LM Studio, llama.cpp or vLLM. Start one (for Ollama: `ollama serve`, then
`ollama pull qwen2.5-coder:7b`), and in Mochiii pick **On this computer** from
`/provider`, or run `/connect local`. No key is needed.

A model on your computer gets the code that matters with each question, and no
tools, until you type `/agent on`. Mochiii then checks what that model can do,
with a few small test questions on your computer, and uses it the way it can:

- **Full agent:** tools to read, search, run and edit.
- **Compact agent:** five tools and short instructions, for a model with a small
  context window, or one that only uses tools when its instructions are short.
- **Chat:** for a model that cannot call tools. It answers with your code, and
  you review any edit it proposes.

`/web on` lets the tools reach the internet; it stays off until you turn it on.
Mochiii also fits the code it sends into the model's context window, and says
how to make that window bigger.

The environment variables work too (`MOCHIII_API_BASE=http://localhost:11434/v1`,
`MOCHIII_MODEL=qwen2.5-coder:7b`); in VS Code, use the `mochiii.apiBase` and
`mochiii.model` settings.
Switch provider or model at any time with `/provider` and `/model`.

## Commands

Type `/` in the chat to see them all.

| Command | What it does |
|---|---|
| `/provider` · `/model` | Choose a provider, key and model |
| `/agent` · `/web` | Tools, and internet tools, for a model on your computer (off until you turn them on) |
| `/account` | What you are connected to, and what you have spent |
| `/spec` | Write a spec, then `/spec build` and `/spec check` |
| `/debug` · `/fix` · `/refactor` · `/hunt` | Start a long task |
| `/task` | See, resume or review the current long task |
| `/team` | A researcher and a coder work on your question together |
| `/review` · `/test` · `/explain` · `/doc` | Quick one-off jobs |
| `/remember` · `/rules` · `/forget` | Rules for this project |
| `/github` · `/issue` · `/pr` | Your project's GitHub repository |
| `/mcp-server` | List, add or remove MCP servers |
| `/save` · `/resume` · `/history` | Keep and reopen chats |
| `/usage` · `/budget` | Tokens, cost and spending limits |
| `/compact` · `/clear` | Shrink the chat, or start a new one |

Some of these are in the terminal app only for now.
The [reference](docs/REFERENCE.md#slash-commands) covers every command in detail.

## Privacy

- Your code is indexed and searched on your own machine. Nothing is uploaded to build the index.
- Your question, and the parts of your code that answer it, go only to the model provider you chose. Text that looks like an API key is masked before it leaves.
- Keys are stored on your machine, readable only by you, and sent only to the provider that issued them.
- Nothing on your machine listens on the network.

The [security model](SECURITY_MODEL.md) explains what Mochiii protects and what it does not.

## Platform support

| | Terminal app | VS Code extension |
|---|---|---|
| Linux x64 | ✓ | ✓ |
| Linux ARM64 | ✓ | — |
| macOS, Apple Silicon | ✓ | not yet |
| Windows x64 | ✓ | ✓ |
| Windows on ARM | ✓ (the x64 build, under Windows' emulation) | — |

- On Linux, code search needs glibc 2.27 or newer (Ubuntu 18.04+, Debian 10+,
  RHEL 8+, Fedora 28+). On older or musl systems everything else works and the
  installer says so.
- On Windows, code search needs the Microsoft Visual C++ runtime, which most PCs
  already have; the installer tells you if yours does not.
- **Intel Macs are not supported:** the engine that runs the code-search model,
  onnxruntime, has no Intel Mac build.

## Documentation

| Read | For |
|---|---|
| [Reference](docs/REFERENCE.md) | Every command, setting and environment variable, and how each part works |
| [Product overview](PRODUCT_OVERVIEW.md) | What Mochiii is, in more depth |
| [Spec workflow](docs/SPEC_WORKFLOW.md) | Building to a spec with `/spec` |
| [Security model](SECURITY_MODEL.md) | The threat model |
| [VS Code extension](clients/vscode/README.md) | The extension's own guide |

## Development

Start with the [developer handoff](docs/HANDOFF.md). Install the git hooks once
per clone:

```bash
make hooks
```

Run the same checks CI runs:

```bash
make check
```

To run the terminal app from a checkout instead of an install, you need
[Go 1.26.10 or newer](https://go.dev/dl/); no C compiler is needed:

```bash
./run-tui.sh
```

## License

Proprietary. See [LICENSE](LICENSE). The source is public to read, but Mochiii
is not open source. Third-party licences are listed in
[THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt).
