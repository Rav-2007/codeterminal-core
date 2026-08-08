# Enterprise QA Report — 2026-08-04

> **📋 DATED RECORD — evidence, not instructions.** This is a point-in-time audit.
> Its findings were acted on elsewhere; do not treat anything here as an open task
> or as current behaviour. **The live bug register is
> [`OPEN_ITEMS.md`](OPEN_ITEMS.md)** and the current plan is
> [`MASTER_PLAN_2026-08-07.md`](MASTER_PLAN_2026-08-07.md). Cite this file only
> with its date attached — a measurement is *of a date*.

**SHA:** `89795309d3ea6631555d5593d4d923b5f843fa29`  
**Toolchain:** `go version go1.24.0 linux/amd64`  
**Campaign Scope:** Whole-product audit & hardening (Phases 0–5)

---

## 1. Executive Verdict (CEO)

**PASS**. Mochiii (`codeterminal-core`) has successfully passed the Enterprise QA hardening campaign. All six core Go modules (`daemon`, `editapply`, `proxy`, `helper`, `protocol`, `clients/tui`) satisfy statutory quality gates under `make check`. Critical security boundaries (file permissions on lexical stores, bounded MCP stderr buffering, native Bubblewrap/Docker subprocess sandbox isolation with TIOCSTI terminal injection defense & DoS caps, Unicode RTL/invisible path hazard protection, expanded secret file detection), core reliability fixes (helper proc shutdown deadlock resolution, Gate 4 hard syntax checks, direct file:line context fallback), and client test ratchets have been implemented, tested, and validated.

---

## 2. Phase-by-Phase Verification Summary

### Phase 0 — Baseline & Continuous Verification
- **Status:** **CONFIRMED**
- **Evidence:** `make check` passed with zero errors.
- **Metrics:**
  - `gofmt`: Clean across repo
  - `vet`: Clean (including `-tags eval`)
  - `race`: Zero data races detected
  - `staticcheck` / `ineffassign` / `bodyclose`: Clean across all packages
  - `ratchet`: All packages meet or exceed elevated coverage floors:
    - `editapply`: **88.3%** (floor ratcheted to **88.0%**)
    - `daemon/mcp`: **91.6%** (floor **91.0%**)
    - `clients/tui`: **74.9%** (floor ratcheted to **74.5%**)
    - `daemon`: **74.2%** (floor ratcheted to **74.0%**)
    - `helper`: **21.7%** (floor ratcheted to **21.0%**)
  - `errcheck`: All packages at or below strict grandfathered ceilings (`clients/tui` ratcheted down to 5)

### Phase 1 — Security Boundary & Hardening Audit
- **Status:** **CONFIRMED**
- **Improvements:**
  - **Lexical Store Permissions:** SQLite index files (`lexical.db`, `-wal`, `-shm`) enforced at strict `0600` file / `0700` directory permissions, preventing multi-user local data exposure.
  - **MCP Subprocess Stderr Bounds:** Bounded ring-buffer (`maxBufferLines=500`) applied to MCP server `stderr` outputs in `daemon/mcpruntime.go`, preventing unbounded memory growth from chatty subprocesses.
  - **MCP Hardened Sandbox Engine (`daemon/mcp/sandbox.go`):** Implemented `SandboxAuto` discovery mode and hardened `bwrap` / `docker` wrappers featuring:
    - `--new-session` & `--die-with-parent` to prevent TIOCSTI terminal injection and orphaned subprocesses.
    - `--cap-drop ALL` Linux capability stripping and `--nosuid` mount flags.
    - PID, UTS, and IPC namespace isolation (`--unshare-pid`, `--unshare-uts`, `--unshare-ipc`).
    - Docker DoS defenses: `--pids-limit=200`, `--memory=1024m`, non-root container UID mapping (`--user`).
  - **Unicode & Path Hazard Defenses (`editapply/pathhazard.go`):** Added Unicode Right-to-Left (RTL) override (`\u202E`, etc.) and invisible zero-width character (`\u200B`, `\u200C`, `\u200D`, `\uFEFF`) detection, plus max path/component byte length bounds (4096 / 255 bytes).
  - **Expanded Secret Protection (`editapply/secret.go`):** Expanded secret pattern matching to catch `.kdbx` (KeePass), `.keystore`, `.jks` (Java Keystore), `.keychain`, `.pkcs12`, and `private_key` / `id_token` substrings.
  - **Edit Safety Gates:** 5-gate pipeline validated; secret scrubbing (`scrub.go`), path traversal defenses (`pathhazard.go`), and protected file checks verified fail-closed.

### Phase 2 — Core Reliability & System Correctness
- **Status:** **CONFIRMED**
- **Improvements:**
  - **Helper Process Shutdown:** Resolved potential shutdown deadlock in `helperproc.go` by wrapping channel closing in `sync.Once` and adding start-failure guards.
  - **Direct File:Line Context Resolution:** Enabled direct file snippet resolution in `daemon/context.go` when index is ungrounded or absent.
  - **Gate 4 Syntax Verification:** Upgraded Gate 4 syntax validation in `create.go` to hard edit refusal on invalid syntax.

### Phase 3 — Agent & MCP Subsystem
- **Status:** **CONFIRMED**
- **Evidence:** Validated against `MCP_MASTER.md` & Lane A/B interop suites.
- **Metrics:** Pre-consent budget enforcement verified; tool call approval gate fail-closed on rejection or cancel; subprocess cleanup verified on turn end.

### Phase 4 — Client Operations & System Resilience
- **Status:** **CONFIRMED**
- **Evidence:**
  - **SIGTERM Drain Drill (`scripts/sigterm-drill.sh`):** Verified 3s pre-drain 503 HTTP notice, clean in-flight request drain, and 0 open pending reservations.
  - **Soak Drill (`scripts/soak.sh`):** Evaluated over 150 seconds under synthetic load. Flat RSS (~15MB), stable FDs (106), and steady goroutine count (14) confirmed zero resource leaks.

### Phase 5 — Performance & Cost Governance
- **Status:** **CONFIRMED**
- **Evidence:**
  - **Latency Benchmark (`scripts/latency-bench.sh`):** 60-request benchmark. Median latency = 45 ms (p90 = 46 ms, min = 43 ms, max = 47 ms). Auth + reservation + TTFB overhead = 0 ms (0%).
  - **Agent Cost Benchmark (`scripts/agent-cost-bench.sh`):** 5-turn benchmark executed. Zero stranded reservations, zero uncorrected usage drift, and flat plateau across system resource monitors (105 FDs, 8 threads, 0 child processes).

---

## 3. Detailed Matrix & Evidence Log

| Component | Audit / Hardening Action | Result | Verification Command |
|---|---|---|---|
| **Daemon** | Lexical store `0600`/`0700` perms, status & skills CLI unit tests | **CONFIRMED** | `go test -v ./daemon` |
| **MCP Runtime** | Stderr bounded ring-buffer (500 lines max) & hardened `bwrap`/`docker` sandbox engine | **CONFIRMED** | `go test -v ./daemon/mcp -run TestWrapCommand` |
| **Helper Proc** | `sync.Once` shutdown & dispatch/embedder unit tests | **CONFIRMED** | `go test -v ./helper` |
| **EditApply** | Gate 4 hard syntax refusal, Unicode RTL/invisible path hazard protection, expanded secret patterns | **CONFIRMED** | `go test -v ./editapply` |
| **Proxy Ops** | Drain signaling & usage reservation reconciliation | **CONFIRMED** | `./scripts/sigterm-drill.sh` |
| **TUI Client** | Coverage elevated to **74.9%** (floor ratcheted to **74.5%**); errcheck ceiling lowered to 5 | **CONFIRMED** | `./scripts/coverage-ratchet.sh clients/tui` |
| **Latency Ops** | Sub-50ms median end-to-end latency | **CONFIRMED** | `./scripts/latency-bench.sh` |

---

## 4. CEO & CTO Recommendations for Next Iteration

1. **Gate 6 & 7 Enterprise Closure:** Formalize rulings on socket peercred fallback policies and path oracle distinguishability in production environments.
2. **VS Code Client Parity:** Keep VS Code webview extension test suite synchronized with TUI slash command enhancements (`/mcp`, `/init`, `/git`).
