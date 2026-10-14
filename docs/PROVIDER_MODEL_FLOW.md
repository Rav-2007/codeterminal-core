# Provider, key and model: how a client connects Mochiii to a model

Both clients ask the user the same three questions, in the same order:

1. **Which provider?** Pick from a list, or choose "Any key".
2. **Its API key.** You can keep the key already in use instead.
3. **Which model?** Pick one of that provider's models.

The daemon does all the real work: it checks the key, stores it, lists the models and remembers the choice. A client only collects answers and shows the results. This document maps the pieces so a change in one client can be matched in the other.

## Architecture

```
 Terminal client (Go, clients/tui)          VS Code client (TS, clients/vscode)
 ───────────────────────────────────        ──────────────────────────────────────────
 setup.go    the three screens              media/connect.js   the popup above the composer
 picker.go   list + search (no paste)          ⇅ postMessage (webview ↔ extension host)
 connect.go  the masked key input           src/chatPanel.ts   openConnect(): the flow
 modelchoice.go  choose_model               src/connectBridge.ts → src/extension.ts connectKey()
                                            src/connectFlow.ts providerChoices(), modelChoices()
        │                                                 │
        └──────────── local socket, peer-authenticated ───┘
                      ConnectRequest / StatusRequest
                                  │
 Daemon (Go, daemon/)             ▼
 connect_handler.go  connectResult()
   show ................ what is in force: address, masked key, where the key comes from
   api_key [+ api_base]  resolveConnectBase → setUpProvider → saveCredential → setProvider
   choose_model / clear_model ... chooseModel(): remembered beside the key in force
   forget .............. remove the stored key
 status.go           available_tiers: { name, slug, active, default, quiet }
 credentials.go      ~/.mochiii/credentials.json (0600)

 Shared table: protocol/providers.go (17 providers, aliases, key prefixes),
 mirrored in clients/vscode/src/connectFlow.ts; connectFlow.test.ts and
 providerShape.test.ts fail if the two disagree.
```

## The workflow

### Step 1: provider

- The list starts with **Any key**, then the 17 providers in `protocol.Providers()` order. Typing filters the list; every typed word must match.
- The provider in use is marked **current** (a green dot), and the list opens with it selected. "In use" comes from the daemon's `show` answer (`active_api_base`), counted only when a key is in force.
- **The search box refuses a paste.** Someone looking at this list probably has their key on the clipboard, and a search box would display it in clear text.

### Step 2: key

- The input is masked. The key goes to the daemon once and is then cleared. It never reaches the transcript, a log, a setting or webview state.
- If the chosen provider is the one already in use, **Keep current key** (or Enter on an empty field) skips to step 3 without sending anything.
- **A key goes only to the company that issued it.** If a key's prefix clearly names a different provider than the one picked (an `sk-ant-` key with Groq chosen):
  - VS Code refuses before sending: `keyMismatch()`, "Nothing was sent".
  - The terminal re-routes the key to its issuer and says so.
  - The daemon applies the same rule regardless of client (dba3714).
- How the daemon answers:
  - `accepted` / `unverified`: the key is stored and used **live, with no restart**. Go to step 3.
  - `rejected`: stay on step 2 with the provider's reason. Nothing is stored.
  - `needs_provider`: the key starts with a bare `sk-`, which several providers issue. It was sent nowhere. The provider list is cut down to the `candidates`; in VS Code the key stays in its field.

### Step 3: model

- The rows come from `available_tiers` for the connected provider, built the same way in both clients (`modelRows` in setup.go, `modelChoices` in connectFlow.ts):
  - Only active models are listed, in the daemon's order.
  - The model in force (this client's choice, else the daemon's default) is marked and is not moved.
  - Models that didn't answer when the key was checked (`quiet`) are dimmed and listed last.
  - The provider's model id is shown only when it differs from the name.
- Choosing a model sends `choose_model`. The daemon saves it beside the key in force, so a restart keeps it. It isn't saved when the key comes from the environment, and the answer says so. The client then uses the model from the next question.
- After a connect, a list with fewer than two models is skipped: there's nothing to choose. Opened to change the model, the list is always shown.
- Leaving step 3 (Esc, or **Keep model**) after a key was connected isn't a cancel. The key is already in use, and the result says so.

## Where it opens

| Trigger | Terminal | VS Code |
|---|---|---|
| Start, no key in force | opens on step 1 (first-run heading) | the chat opens with the popup on step 1 (once per panel) |
| First question with no key | key prompt | the popup; the question is sent after it succeeds |
| `/connect` | step 1 | step 1 |
| `/connect <provider or address>` | step 2 for that provider | step 2 for that provider |
| `/provider` | step 1 | — (use `/connect`) |
| Account → "Switch provider or key" | — | step 1 |
| `/model` (bare) | step 3 only | the model menu by the composer |
| Command palette "Mochiii: Connect" | — | VS Code input boxes: provider, then key (no model step) |

## VS Code: webview ↔ host messages

| Direction | Message | Carries |
|---|---|---|
| host → webview | `connectOpen` | `providers` ("Any key" first), `base` (skip to step 2), `currentBase`, `maskedKey`, `keyInForce`, `firstRun` |
| webview → host | `connectSubmit` | `apiBase` (`''` = Any key), `key`. **The only message that carries a key.** |
| host → webview | `connectNeedsProvider` | `candidates` (API addresses), `text` |
| webview → host | `connectKeep` | none: keep the key in force, go to models |
| host → webview | `connectModelsLoading`, `connectModels` | `rows` (`modelChoices`), `inForce` |
| webview → host | `connectModel` | `tier` |
| host → webview | `connectError` | `text`; the popup stays open |
| webview → host / host → webview | `connectCancel` / `connectClose` | none |

The host accepts `connectSubmit` only for an address the popup offered: "Any key", a listed provider, or the address `/connect <address>` opened it with. It passes the key through `connectBridge.ts`, a function rather than a command, so no other extension can call it with a key.

## Files

- Terminal: `clients/tui/setup.go`, `picker.go`, `connect.go`, `modelchoice.go`, `account.go`
- VS Code: `clients/vscode/media/connect.js`, `src/chatPanel.ts` (`openConnect` and the markup), `src/connectBridge.ts`, `src/extension.ts` (`connectKey`, `connectThroughDaemon`), `src/connectFlow.ts`, `src/chatPages/pagesHost.ts` (Account button)
- Daemon: `daemon/connect_handler.go`, `providerconnect.go`, `credentials.go`, `status.go`; `protocol/providers.go`, `protocol/inforce.go`
- Parity: `clients/tui/slash_clientparity_test.go`. `/connect` now has the same summary in both clients.
