# Enterprise QA — command cheatsheet

Run from repo root. Go 1.25+ on PATH. There is no root module — use `make` or per-module `cd`.

## Phase 0 — baseline

```sh
make check          # fmt vet race lint ratchet errcheck
make fuzz           # default ~30s/target; FUZZTIME=10s for a short sample
git rev-parse HEAD
git status --short
```

## Phase 1 — security surfaces

```sh
# Edit gates / secrets / protected (package tests)
(cd editapply && go test ./... -count=1)

# Peer / transport / scrub
(cd daemon && go test ./... -count=1 -run 'Peer|Scrub|Secret|Protected|Path')

# Proxy spend / ZDR-shaped tests
(cd proxy && go test ./... -count=1)
```

## Phase 2 — correctness

```sh
make race
(cd daemon && go test ./... -count=1)
(cd protocol && go test ./... -count=1)
(cd helper && go test ./... -count=1)
```

## Phase 3 — agent / MCP (prefer cite, don’t duplicate)

See [docs/MCP_MASTER.md](../../../docs/MCP_MASTER.md). Only re-run if SHA changed or regression suspected:

```sh
go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected' -count=1
go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying|PrefixWriter' -count=1
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo   scripts/agent-cost-bench.sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=orphan scripts/agent-cost-bench.sh
MCP_INTEROP=1 go test ./daemon/mcp/ -run TestInterop -v -count=1
```

## Phase 4 — clients / proxy ops

```sh
(cd clients/tui && go test ./... -count=1)
make drill          # SIGTERM mid-stream
DURATION=180 make soak   # short soak; full default is 30m
```

## Phase 5 — perf / cost (zero spend unless budgeted)

```sh
scripts/agent-cost-bench.sh
scripts/latency-bench.sh
# Real-model eval: only with explicit spend cap
```

## Evidence labels

| Label | Means |
|---|---|
| CONFIRMED | Script/run or read off the cited line |
| PLAUSIBLE | Reasoned from source; path not executed |
| NOT RUN | Honestly not attempted |
