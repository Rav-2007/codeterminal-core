# PROJECT INTELLIGENCE REPORT

## 1. Executive Summary
Mochiii (codeterminal-core) is a hyper-secure, local-first AI coding assistant that runs as a background service (daemon) on the user's machine. Unlike cloud-based AI tools that ingest codebases globally, Mochiii performs indexing, chunking, embedding, and retrieval entirely locally. It communicates with AI providers via a managed proxy that enforces Zero Data Retention (ZDR) and holds API keys, ensuring that only the minimal, scrubbed prompt ever leaves the machine. It features a strict 5-gate pipeline for code edits and an opt-in agentic loop with explicit, per-tool human consent.

## 2. What This Project Actually Is
**One Sentence:** A local-first AI pair programmer that indexes your repository on-device and enforces cryptographic-level safety and privacy over every AI response and code edit.
**One Paragraph:** Mochiii is a background daemon that provides intelligent codebase traversal and editing through three interfaces: a terminal chat (TUI), a VS Code extension, and a CLI. It keeps all intellectual property local by computing embeddings on-device, stripping secrets heuristically, and routing requests through a strict proxy that enforces provider-side zero data retention.
**Technical Explanation:** A Go-based local daemon communicating via owner-only Unix/Windows sockets. It uses a local SQLite/Vector store for semantic search and conversational memory. Inference is proxied to external LLMs, but API keys are held server-side by the proxy, which rejects any request lacking ZDR flags. Code edits are bounded by a 5-gate state machine ensuring exact matches and valid syntax before proposing diffs to the human.
**Non-Technical Explanation:** It’s an AI coder that actually respects your privacy. It reads your code on your own computer, only sends the tiny piece of code it needs to answer your question to the AI, forces the AI to forget it immediately, and refuses to write any broken code to your files.

## 3. Problem It Solves
**Existing Problem:** Modern AI coding tools force developers to choose between high capability (uploading their entire codebase to a cloud provider) and high privacy (running weaker local models with poor context).
**Why it matters:** Enterprises, regulated industries, and security-conscious developers cannot legally or practically upload proprietary code to third-party servers.
**How this solves it:** Mochiii splits the workload. Heavy lifting (indexing, retrieval, secrets scrubbing, safety gating) happens locally. Only a minimal prompt is sent to a powerful cloud LLM over a proxy that guarantees no data is saved.
**Value:** Peace of mind. Developers get cloud-tier AI intelligence with local-tier privacy.

## 4. Complete Capability Map
* **On-Device Indexing:** Local embedding generation and storage.
* **Hybrid Retrieval:** Semantic + lexical search surfacing actual implementation.
* **5-Gate Edit Pipeline:** 
  1. Path safety (no escapes, no secrets)
  2. Exact match (SEARCH block found exactly once)
  3. Syntax valid (parses .go files post-edit)
  4. Human diff approval
  5. Backup before write
* **Honest Undo:** Multi-run undo capability backed by timestamped snapshots.
* **Zero Data Retention Proxy:** Cloud proxy holds keys and drops requests missing ZDR headers.
* **Secret Scrubbing:** Heuristic redaction of API keys/PEMs before network egress.
* **Agentic Loop (Opt-in):** Multi-step tool execution bounded by strict iteration and byte ceilings.
* **Tool Approval Engine:** Explicit cryptographic SHA-256 bindings between tool arguments shown to the user and what actually runs.
* **Multi-Client Interface:** TUI, VSCode, and CLI sharing one daemon.

## 5. Complete Feature Inventory
### Core Features
* Local Semantic Code Search
* TUI & VS Code integration
* 5-Gate `SEARCH/REPLACE` editing
* Secure Managed Proxy

### Advanced Features
* Context Threading (Memory across sessions)
* Bounded Agentic Loop
* Multi-run Undo

## 6. AI Capabilities
* **AI Models:** Connects to cloud providers (proxied)
* **Embeddings:** On-device embedding models (isolated subprocess)
* **AI Workflows:** "Ask-and-Answer" or "Agent Mode" (bounded iteration)
* **Retrieval:** Hybrid chunking, reranking, and exact grounding.

## 7. Automation Capabilities
* **Background Indexing:** Watches workspace for changes.
* **Pipeline:** 5-gate state machine for safe code mutations.
* **Agent Triggers:** Automated consecutive tool executions (only if explicitly allowed in `models.json` or approved per-turn).

## 8. Data Capabilities
* Local Vector & Lexical Stores.
* Memory persistence per-workspace.
* Data skipping (ignores `.env`, keys during index).

## 9. Integration Capabilities
* **MCP (Model Context Protocol):** Supports 3rd-party local tools/servers with strict "Ask" policies.
* **Editor:** VS Code LSP bridge.
* **Terminal:** TUI implementation via Bubble Tea.

## 10. Security Capabilities
* **Owner-only socket:** No exposed network ports.
* **Zero Data Retention:** Network requests fail-closed if ZDR isn't enforced.
* **Secret Scrubber:** On-device regex/heuristic redaction.
* **API Key Isolation:** Client never sees the inference key.
* **Spend Containment:** Token limits enforced mid-stream.
* **Confined Execution:** Path escape prevention, exact-match requirements.

## 11. Developer Capabilities
* Hackable CLI for scripting.
* Extensible via MCP servers.
* Transparent audit logs (`.mochiii/logs/toolcalls.jsonl`).

## 12. Technical Architecture
* **Frontend:** Go TUI (Bubble Tea) / VS Code Extension (TypeScript/Webviews)
* **Backend:** Go Daemon (Local), Go Proxy (Cloud)
* **Database:** Local SQLite/Vector stores
* **AI:** Isolated local embedder subprocess, proxied cloud LLM.
* **Data Flow:** USER -> CLI/VSCODE -> DAEMON (Retrieval/Scrub) -> PROXY (Append Key/Enforce ZDR) -> LLM -> PROXY -> DAEMON (5-Gate Edit) -> USER (Approve) -> DISK

## 13. Technology Stack
* Go (Core Daemon, Proxy, TUI, CLI)
* TypeScript/VS Code API (Extension)
* SQLite (Local Storage)
* WebSockets/Unix Sockets (Communication)
* On-device Embedders (ONNX)

## 14. Top 5 WOW Features
1. **The 5-Gate Safety Pipeline**
   * *Why:* Makes AI edits completely trustworthy.
   * *Visual:* An animated 5-step lock mechanism (Path, Match, Syntax, Human, Backup) lighting up green sequentially.
2. **Proxy-Enforced Zero Data Retention**
   * *Why:* Proves privacy isn't just a promise.
   * *Visual:* A packet being scanned by a futuristic gateway; packets without the "ZDR" glowing badge are disintegrated.
3. **Local-First Indexing**
   * *Why:* Code never leaves the laptop.
   * *Visual:* A glowing web of connections forming entirely within the silhouette of a laptop, disconnected from the cloud.
4. **Honest Undo**
   * *Why:* Real safety net.
   * *Visual:* A reverse-timeline animation showing code un-mutating instantly.
5. **Agentic Tool Approval**
   * *Why:* Perfect control over AI agency.
   * *Visual:* A floating command terminal pausing execution until a cryptographic hash match confirms user consent.

## 15. Hidden Gems
* **Secret Scrubbing:** Heuristic redaction before egress (massively undervalued by standard users, loved by enterprise).
* **Multi-Client Daemon:** You can start a chat in the TUI and see the results in VS Code.
* **Budget Limits:** Strict byte/iteration ceilings on agents to prevent runaway token spend.

## 16. Target Users
* Security-conscious software engineers
* FinTech / MedTech developers (Regulated industries)
* Enterprise platform teams
* Privacy-minded solo developers

## 17. Product Positioning
**Category:** Enterprise-Grade Local AI Coding Assistant
**Primary Value Proposition:** "Mochiii gives you cloud-tier AI coding intelligence without ever uploading your codebase or sacrificing your privacy."

## 18. Differentiators
* Copilot/Cursor upload your code. Mochiii keeps it local.
* Other tools apply edits blindly. Mochiii gates them behind a strict 5-stage pipeline.
* Other tools promise privacy in their ToS. Mochiii enforces it at the architectural proxy level.

---

# WEBSITE INFORMATION ARCHITECTURE

## 1. Hero
* **Headline:** Intelligence of the Cloud. Privacy of Localhost.
* **Subtitle:** The AI pair programmer that indexes your code locally, enforces Zero Data Retention by architecture, and never writes a line without passing a 5-gate safety pipeline.
* **Primary CTA:** Download for Linux / macOS
* **Secondary CTA:** Read the Security Model
* **Visual:** A highly dynamic, 2.5D interactive terminal floating above a glowing local CPU, showing a direct, guarded beam to a cloud proxy.

## 2. Feature Showcase: The 5-Gate Pipeline (Scroll-driven)
As the user scrolls, a proposed code edit moves through 5 physical-looking security checkpoints.
* Gate 1: Path Integrity (Blocks escapes)
* Gate 2: Exact Match (Ensures precision)
* Gate 3: Syntax Verification (Parses AST)
* Gate 4: Human Consent (Diff review)
* Gate 5: Snapshot (Backup before write)

## 3. Privacy Architecture (Interactive Diagram)
A toggleable diagram comparing "Industry Default" vs "Mochiii".
* *Industry:* Red lines pulling the whole codebase into a cloud.
* *Mochiii:* Green lines keeping the codebase local, sending only a scrubbed, ZDR-flagged prompt via a proxy.

## 4. Agentic Loop (Terminal Simulation)
An animated TUI showing Mochiii thinking, requesting a tool (e.g., reading a file), pausing for the user to explicitly hit `[Y] Allow`, and resuming. Emphasizes "Control."

## 5. Ecosystem
Three floating chips: Terminal (TUI), Editor (VS Code), Scripting (CLI), all connected to one glowing core (The Daemon).

## 6. Footer & Trust
Links to GitHub, Security Audits, Backlog, and Documentation.

---

# UI/UX DESIGN SYSTEM (FUTURISTIC & PREMIUM)
* **Vibe:** Technical, Precise, High-Performance, Secure. Think "Advanced Cyber-Security Lab" meets "Premium Developer Tool."
* **Colors:**
  * Background: Deep Obsidian (`#0A0A0A`)
  * Primary Accent: Terminal Green (`#2ECC71`) - Represents local/safe.
  * Secondary Accent: Amber/Gold (`#F39C12`) - Represents the guarded cloud hop.
  * Typography/Grid: Muted Slate (`#64748B`) and Crisp White (`#FFFFFF`).
* **Typography:** 
  * Headings: `Inter` or `Geist` (Sleek, geometric).
  * Monospace (Code/Data): `JetBrains Mono` or `Fira Code`.
* **Components:** 
  * Glassmorphic panels with very subtle green/amber borders.
  * Live code diff boxes with high-contrast red/green highlights.
  * Metric readouts (e.g., "0 Bytes Retained", "100% Local Index").
* **Motion:** 
  * Fast, easing out (snappy). Terminal typing effects. Data flow paths animated with moving dashed lines or glowing particles.

---

# DO NOT CLAIM
* **DO NOT CLAIM** that 3rd-party MCP servers are sandboxed (they are not, they run with full user permissions).
* **DO NOT CLAIM** the secret scrubber is flawless (it is heuristic and prefix-based).
* **DO NOT CLAIM** it runs completely offline (it requires the cloud for LLM inference, it's the *context* that is local).
* **DO NOT CLAIM** it auto-applies code without human oversight (even auto-apply mode respects the 5 gates).

---

# IMPLEMENTATION PRIORITY
* **P0:** Hero Section, Value Proposition, The 5-Gate Pipeline Animation.
* **P1:** Privacy Architecture Diagram (ZDR Proxy vs Local), Feature matrix (TUI/VSCode/CLI).
* **P2:** Agentic Loop interactive terminal demo.
* **P3:** Responsive 3D/2.5D visual enhancements.
