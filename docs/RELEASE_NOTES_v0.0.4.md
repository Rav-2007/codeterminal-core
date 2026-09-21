# Release notes — v0.0.3

The body to publish on the GitHub Release, kept here so it has a source of
truth: the release body is an editable field on GitHub and this file is not. If
you change one, change the other.

---

**v0.0.3 supersedes v0.0.2, which was never published.** Two defects made the
v0.0.2 artifacts unusable by anyone who downloaded them; both are fixed here.
**Linux and Windows, x64.**

A VS Code extension with a local daemon: repository-aware retrieval, an agent
loop, and edits you can undo. The `.vsix` carries its own daemon and embedder
helper — you do not install them separately, and you do not need to export
anything for the extension to start its daemon.

## What changed since v0.0.2

**You can now set an API key.** There was no way to. The daemon reads
`CODETERMINAL_API_KEY` from its environment and nothing put it there: the
extension bridged only the API *base* from settings, contributed no key setting,
and a VS Code launched from a desktop icon inherits no shell. So a packaged
install could never authenticate and every answer failed with "the configured
API credentials were rejected". Run **`Mochiii: Set API Key`** from the command
palette; the extension also offers it on first activation. The key is stored in
VS Code's SecretStorage — not `settings.json`, which Settings Sync replicates
and a commit can leak.

**The daemon and embedder helper are now published.** v0.0.2 shipped a
standalone terminal client with nothing to talk to: the client looks for a
running daemon and exits with *"daemon not found … start it with:
codeterminal-daemon"*, and no daemon was published anywhere — it existed only
inside the `.vsix`. All three binaries now ship per platform. The checksum
manifest for them is `SHA256SUMS-bin` (it was `SHA256SUMS-tui`).

## Install

Download the `.vsix` for your platform, then:

```
code --install-extension codeterminal-vscode-linux-x64-0.0.3.vsix
```
```
code --install-extension codeterminal-vscode-win32-x64-0.0.3.vsix
```

Then run **`Mochiii: Set API Key`**. Without it nothing can be answered.

The key is for whatever endpoint the daemon talks to — by default
`https://openrouter.ai/api/v1`. Change it with the **Mochiii: API Base**
setting.

**Standalone binaries** (optional, for the two-process terminal workflow):
`codeterminal-tui`, `codeterminal-daemon` and `codeterminal-embedder-helper`,
per platform. On Linux, `chmod +x` them. Start the daemon first, pointed at a
workspace, then the client.

## Windows: you will see "Windows protected your PC"

The binaries are **not Authenticode-signed**. SmartScreen will warn on first run
— choose **More info → Run anyway**.

This is a stated decision, not an oversight. `scripts/release-targets.txt`
declares `signing-required: none`, and the signing guard carries a vacuity
tripwire that *fails the release* if the set of targets needing a signature is
empty without that declaration — so nothing is being skipped silently. If you
want the integrity check a signature would have given you, verify against the
checksums below.

## macOS is not in this release

`darwin-arm64` is deliberately absent. It was built but never shippable: with no
Apple signing secrets every macOS binary was marked unsigned and the signing
guard failed the whole release, so a tag published nothing on *any* platform.
Removing the target was chosen over relaxing the guard — a tag now declares two
platforms and ships two. Returning it needs Apple Developer enrolment and five
secrets. Intel Macs are a separate, open gap: upstream onnxruntime ships no
binary for `darwin/amd64` at all.

## Verify your download

```
sha256sum -c SHA256SUMS      # the .vsix packages
sha256sum -c SHA256SUMS-bin  # the standalone binaries
```

On Windows: `certutil -hashfile <file> SHA256` and compare by eye.

## Worth knowing

- **Retrieval needs a one-time model download** (~41 MB on Linux, 104 MB on
  Windows). The extension offers it on first activation. Declining is fine —
  you get a working extension that answers without reading your code, and the
  offer returns next session.
- **Agent mode is off by default.** Tool use, third-party MCP servers and
  `/team` require `mcp.enabled` in `models.json`; with it unset the outbound
  request is byte-identical to one without the feature.
- **Not on any marketplace.** The `publish` job is unimplemented and disabled —
  a deliberate gap, not a failed step. Install from the `.vsix`.
- **Not open source.** The licence is proprietary — `LICENSE`, all rights
  reserved. This repository is readable, not open.

## Provenance

Built by `release.yml` from the `v0.0.3` tag. Binaries are built natively on the
OS they target — not cross-compiled — and stamped with the tag, so the daemon
reports `0.0.3`. Every build is `-trimpath`ed: a plain build embedded 678
absolute paths including the builder's home directory, and a supply-chain gate
fails any binary carrying one.
