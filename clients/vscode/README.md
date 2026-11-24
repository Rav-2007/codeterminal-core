# Mochiii

**Beta.** An AI coding agent for VS Code that works in your project. Ask about
your code, fix bugs, write tests, and review the changes Mochiii makes.

[Website](https://www.mochiii.in) · [Docs](https://docs.mochiii.in) ·
[MCP Marketplace](https://www.mochiii.in/mcp) · [Support](https://www.mochiii.in/support)

## What it does

- **Edits you can review.** A new chat starts in Auto: each edit is checked,
  applied, and can be undone. Switch to Manual and every edit waits for your
  Apply, or to Plan for a plan with no edits.
- **An agent, with your say.** With a provider's model that supports tools,
  Mochiii reads and searches the project, makes edits, and runs commands and
  tests. In Auto it does that without asking, after a warning the first time.
  It still asks when it recognises something that reaches outside the project,
  sends something out or wipes data. Commands are not sandboxed, and that check
  can miss some.
  Web search is off as shipped.
- **Build from a spec.** `/spec` writes down what you want, `/spec build` builds
  it tests-first, and `/spec check` grades the result.
- **MCP servers.** Add them from the built-in Marketplace, or your own by
  command, URL or pasted JSON: open **Customize → MCP → Add server**.
- **Your files as context.** Attach PDFs (scanned too), Word, Excel, PowerPoint
  and images. They are read on your machine and turned into text.

## Code search

Code search finds the code that matters for each question and follows your
saves. It needs an index of the folder. Mochiii offers to build one the first
time you open a folder; later, run **Mochiii: Index Folder for Code Search**.
The first index also downloads a search model, once for your computer.

Without an index the agent can still read and search files with its tools, and
a model that gets no tools sees only what you attach and the file you have open.

## Any model

Type `/connect` to use your own key from one of 17 providers, among them
OpenRouter, Anthropic, Google Gemini and Groq.

For a model server on your computer (Ollama, LM Studio, llama.cpp, vLLM), set
`mochiii.apiBase` to its address and `mochiii.model` to the model's name in VS
Code's Settings, then run **Mochiii: Restart Daemon**. A model on your computer
is offered the same tools as any other model; if its server says the model cannot
use tools, Mochiii answers without them.

## Where your code goes

Your code is indexed and searched on your machine. A question, and the parts of
your code that go with it, are sent to the model provider you chose; with a
model on your computer they stay there. A tool you allow, such as a remote MCP
server, receives what that call sends it. Keys are stored on your machine, and
text that looks like an API key is masked before a prompt is sent.

## Get started

1. Install Mochiii and open a project folder you trust.
2. Click the lotus in the Activity Bar, or run **Mochiii: Open Chat**.
3. Connect a model with `/connect`.
4. Ask a question.

## Requirements

- Linux x64 or Windows x64. No macOS build yet. Code search on Linux needs
  glibc 2.27 or newer.
- A trusted workspace on this computer. Remote SSH, WSL and Dev Containers are
  not supported yet.
- Using the terminal app too? VS Code extension 1.0.1 works with terminal app
  1.6.1 or later. Update the terminal app first.
- Installed a Mochiii `.vsix` numbered 1.1.0 to 1.6.1 by hand? Uninstall it
  first: VS Code will not replace it with 1.0.1.

## Beta

Mochiii works and is used daily, but some features and docs are still being
finished. Expect changes between versions. Report bugs to
[supportmochiii@gmail.com](mailto:supportmochiii@gmail.com). Report security
problems only to [mochiiijuly@gmail.com](mailto:mochiiijuly@gmail.com).

## Licence

Proprietary. See the licence file included with the extension.
