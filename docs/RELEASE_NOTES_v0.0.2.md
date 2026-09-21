# Release notes — v0.0.2

**v0.0.2 WAS NEVER PUBLISHED, and its GitHub Release was deleted on 2026-09-21.**
This sentence used to read *"the body published on the GitHub Release"*, which
was never true: the release was created `draft: true`, `publishedAt` was always
`null`, and it was removed once v0.0.3 superseded it. The `v0.0.2` **tag**
survives at `52bd128` as a true record of a green build, so
[the URL that sentence linked](https://github.com/Rav-2007/codeterminal-core/releases/tag/v0.0.2)
still returns 200 — it resolves to the *tag* page, there being no release. That
is exactly how a false claim survives a link check.

Kept as a record of what that draft said. Two defects made its artifacts
unusable by anyone who had downloaded them — no way to supply an API key, and a
terminal client with no daemon published — and both are fixed in v0.0.4.

---

The first release of Mochiii that attaches anything. **Linux and Windows, x64.**

A VS Code extension with a local daemon: repository-aware retrieval, an agent loop, and edits you can undo. The `.vsix` carries its own daemon and embedder helper — you do not install them separately, and you do not need to export anything for the extension to start its daemon.

## Install

**VS Code extension** — download the `.vsix` for your platform, then:

```
code --install-extension mochiii-vscode-linux-x64-0.0.2.vsix
```
```
code --install-extension mochiii-vscode-win32-x64-0.0.2.vsix
```

**Terminal client** (optional, standalone) — `mochiii-tui-linux-x64` or `mochiii-tui-win32-x64.exe`. On Linux, `chmod +x` it first.

## Windows: you will see "Windows protected your PC"

The binaries are **not Authenticode-signed**. SmartScreen will warn on first run — choose **More info → Run anyway**.

This is a stated decision, not an oversight. `scripts/release-targets.txt` declares `signing-required: none`, and the signing guard carries a vacuity tripwire that *fails the release* if the set of targets needing a signature is empty without that declaration — so nothing is being skipped silently. If you want the integrity check a signature would have given you, verify against `SHA256SUMS` below.

## macOS is not in this release

`darwin-arm64` is deliberately absent. It was built but never shippable: with no Apple signing secrets, every macOS binary was marked unsigned and the signing guard failed the whole release, so a tag published nothing on *any* platform. Removing the target was chosen over relaxing the guard — a tag now declares two platforms and ships two. Returning it needs Apple Developer enrolment and five secrets.

## Verify your download

```
sha256sum -c SHA256SUMS        # the .vsix packages
sha256sum -c SHA256SUMS-tui    # the terminal clients
```

On Windows: `certutil -hashfile <file> SHA256` and compare by eye.

## What this is not

- **Not on any marketplace.** The `publish` job is unimplemented and disabled (`if: false`) — a deliberate gap, not a failed step. Install from the `.vsix`.
- **Not signed** on either platform.
- **Not open source.** The licence is proprietary — `LICENSE`, all rights reserved. This repository is readable, not open.

## Provenance

Built by [`release.yml`](https://github.com/Rav-2007/codeterminal-core/blob/v0.0.2/.github/workflows/release.yml) from tag `v0.0.2` at commit `52bd128`. Binaries are built natively on the OS they target — not cross-compiled — and stamped with the tag, so the daemon reports `0.0.2`. Every build is `-trimpath`ed: a plain build embedded 678 absolute paths including the builder's home directory, and a supply-chain gate fails any binary carrying one.

The release path was rehearsed green on this repository before the tag was created (run `35515730188`), and reconciled against a prediction written before the rehearsal ran.
