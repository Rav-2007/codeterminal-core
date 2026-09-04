# ADR-002 — Vendoring and SBOM

**Status:** accepted — do not vendor; do not generate an SBOM yet.
**Date:** 2026-09-04.
**Scope:** all six Go modules.

---

## What was asked

Task 3.8 asked for a recommendation for or against vendoring dependencies and
producing an SBOM, with reasoning rather than a preference.

## The dependency surface, measured

| Module | Direct deps | Notes |
|---|---|---|
| `clients/tui` | 8 | Charm stack (bubbletea, bubbles, lipgloss, x/ansi), go-isatty, termenv, golang.org/x/sys |
| `protocol` | 1 | golang.org/x/sys |
| `editapply` | small | pure Go |
| `daemon` | larger | includes retrieval and MCP |
| `proxy` | small | the only internet-facing component |
| `helper` | CGO | onnxruntime |

Everything is already pinned by `go.sum` with cryptographic hashes, and the Go
module proxy plus the checksum database mean a build cannot silently take
different bytes for the same version.

## Recommendation: do not vendor

**What vendoring would add.** Builds that work with no module proxy, and the
dependency source visible in the repository at review time.

**What it costs, and why it loses here.**

1. **`go.sum` already provides the integrity property.** A vendored tree is not
   more tamper-evident than a hash — it is less, because a hand-edit inside
   `vendor/` produces a diff nobody reads, while a `go.sum` mismatch stops the
   build.
2. **It makes the review surface worse, not better.** Vendoring the Charm stack
   and onnxruntime bindings adds tens of thousands of lines that every future
   diff, every code search and every AST-based guard in this repo has to learn
   to ignore. Several gates here walk `*.go` files across modules; a vendor tree
   would need exclusions in each, and an exclusion someone forgets is a gate
   silently narrowed.
3. **It hides upgrades.** `govulncheck` and Dependabot-style flows key on
   `go.mod`; a vendored tree that drifts from it is a class of bug we would be
   introducing for a benefit we do not currently need.
4. **The offline-build case is not ours.** CI has network. Releases are built in
   CI. No air-gapped build has been requested.

**What would change this:** an air-gapped or reproducible-from-source-only build
requirement, or shipping to a customer who audits the dependency tree directly.

## Recommendation: no SBOM yet, and the reason is honesty

An SBOM is a claim about what is inside a shipped artifact. It is worth
generating when someone consumes it — a customer's supply-chain review, a
compliance regime, a vulnerability service that ingests it.

None of those exist for this product today. Generating one now would produce a
file that is committed, drifts, and is never read, which is the "stale record"
failure this repository already has a history of. It also invites a claim we
cannot currently back: an SBOM covering only the Go modules would omit the
onnxruntime shared library the helper links against, which is precisely the
component an auditor would care most about.

**What is in place instead**, and it covers the same ground for the same
artifacts:

- `go.sum` pins every dependency by hash across all six modules.
- `scripts/govulncheck.sh` and the CI matrix scan all six against the Go
  vulnerability database, and **fail** on a reachable finding — verified
  empirically: `govulncheck` exits 3, measured against `golang.org/x/text
  v0.3.0`, so `run: govulncheck ./...` fails the build rather than reporting.
- `scripts/go-toolchain-pinned.sh` keeps the Go floor identical across every
  module, and distinguishes the hard `go` directive from the advisory
  `toolchain` hint.
- `scripts/supply-chain.sh` fails if `go mod tidy` would change anything, so the
  dependency graph in the file people audit matches the one the build uses.
- `go build -trimpath` on release artifacts, enforced by the same script.
- `scripts/actions-pinned.sh` pins every GitHub Action to a commit SHA.

**What would change this:** the first external party who asks for one. At that
point generate it at release time from the build, covering the native
dependencies too, rather than committing a static file.

## What this does NOT cover, stated plainly

- The **onnxruntime** native library the helper links against is outside
  `govulncheck`'s reach entirely. Nothing in this repository scans it.
- The **VS Code extension's** npm dependencies are not covered by any of the
  above; this ADR is about the Go modules.
