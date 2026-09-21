#!/usr/bin/env bash
#
# agent-cost-bench.sh — what one agent turn costs, measured end to end through
#                       the REAL daemon and the REAL proxy, with zero spend.
#
# Usage:
#   scripts/agent-cost-bench.sh                       # 5 turns, 2 tool calls each
#   TURNS=20 TOOL_CALLS=7 scripts/agent-cost-bench.sh # worst case at the default budget
#   TOKEN_LIMIT=8192 scripts/agent-cost-bench.sh      # find where a turn dies mid-way
#   POLICY=ask scripts/agent-cost-bench.sh            # exercise the approval round-trip
#
# WHY THIS EXISTS
#
# The MCP plan's risk register listed "N x quota reservations per turn --
# reconcile with QUOTA_RESERVATION_DESIGN.md before enabling for managed keys"
# and it was never done. Meanwhile proxy/main.go reserves quota PER REQUEST at
# defaultReservationTokens = 4096, and the daemon declares no max_tokens at all,
# so an 8-iteration agent turn reserves ~32,768 tokens for ONE user prompt.
#
# That is not a hypothesis. Phase 0's first tool-calling eval lost 33 of 84
# probes to quota_exceeded through the managed proxy, was declared void, and was
# re-run direct to OpenRouter. The workaround was never a fix, and nothing has
# measured it since.
#
# WHAT IT MEASURES
#
#   reserve_usage calls / turn   the ledger, straight from the fake Supabase
#   tokens reserved / turn       ditto -- this is the quota a turn actually holds
#   upstream request bodies      per iteration, so context GROWTH is visible;
#                                the loop re-sends the whole message list every
#                                step, and whether that is linear or quadratic is
#                                the difference between 2x and 8x cost
#   ttft / wall / machine ms     from agentbench, with approval waits excluded
#   result_bytes / turn          post-scrub tool output -- what leaves the machine
#
# WHAT IT IS NOT: not a model-quality measurement (the upstream is a fake that
# always asks for the same tool), and not a real-token count (the fake reports a
# fixed usage frame). It measures the SHAPE and the COST STRUCTURE of a turn --
# request counts, reservations, body growth, latency -- which is what no number
# in this repo currently answers. docs/AGENT_LOOP_RELIABILITY_2026-07-31.md
# covers behaviour against a real model.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="${WORK:-$(mktemp -d)}"
mkdir -p "$work"

TURNS="${TURNS:-5}"
# One turn is TOOL_CALLS+1 upstream requests. The daemon's default ceiling is
# max_iterations 8, so TOOL_CALLS=7 is the worst case that budget allows.
TOOL_CALLS="${TOOL_CALLS:-2}"
TOKEN_LIMIT="${TOKEN_LIMIT:-10000000}"
# Kill the turn part-way by refusing the Nth reservation, which is what a key
# running out of quota mid-turn looks like on the wire. 0 never fails.
RESERVE_FAIL_AFTER="${RESERVE_FAIL_AFTER:-0}"
# "allow" runs without asking; "ask" makes the daemon stop and wait, which is
# what exercises the approval round-trip and the deadline credit-back.
POLICY="${POLICY:-allow}"
DECISION="${DECISION:-approve}"
THINK_MS="${THINK_MS:-0}"
CHUNK_DELAY="${CHUNK_DELAY:-2ms}"
CHUNKS="${CHUNKS:-8}"
SUPABASE_DELAY="${SUPABASE_DELAY:-0}"
# The per-key limiter is 2 req/s, burst 20 (proxy/ratelimit.go) and ONE turn is
# several requests. Unpaced, this measures throttling rather than cost -- which
# is a real thing to measure, but only on purpose. PACE=0 to do that.
PACE="${PACE:-3s}"

# A Lane B MCP server for the soak. Empty means built-ins only, which is what
# every previous run of this script measured -- and it is why the 2026-07-30
# gate's P2-3 (RSS +8 MB over 50 turns, no plateau shown) could not be settled:
# the registry is built and CLOSED per turn, so a leaked subprocess or fd is
# only visible when there IS a subprocess. "echo" uses the cooperative fixture;
# "orphan" uses the one that leaves a child behind.
MCP_SERVER="${MCP_SERVER:-}"
# How often to sample daemon resources DURING the run. Two endpoint samples can
# tell growth from no-growth; they cannot tell growth from a PLATEAU, which is
# what the threshold actually asks for.
SAMPLE_EVERY="${SAMPLE_EVERY:-2s}"

SUPABASE_PORT="${SUPABASE_PORT:-9601}"
UPSTREAM_PORT="${UPSTREAM_PORT:-9602}"
PROXY_PORT="${PROXY_PORT:-9600}"

harness_pid=""; proxy_pid=""; daemon_pid=""
cleanup() {
  [ -n "$daemon_pid" ] && kill "$daemon_pid" 2>/dev/null
  [ -n "$proxy_pid" ] && kill "$proxy_pid" 2>/dev/null
  [ -n "$harness_pid" ] && kill "$harness_pid" 2>/dev/null
  return 0
}
trap cleanup EXIT

echo "building..."
(cd "$repo_root/proxy" && go build -o "$work/proxy-bin" . && go build -o "$work/harness-bin" ./testharness) || exit 1
(cd "$repo_root" && go build -o "$work/daemon-bin" ./daemon && go build -o "$work/agentbench" ./daemon/testdata/agentbench) || exit 1
if [ -n "$MCP_SERVER" ]; then
  (cd "$repo_root/daemon" && go build -o "$work/echoserver" ./mcp/testdata/echoserver \
     && go build -o "$work/badserver" ./mcp/testdata/badserver) || exit 1
fi

# An empty workspace: the tool under test is list_directory, and pointing it at
# the repo would make result_bytes a function of how big this checkout is.
ws="$work/ws"
mkdir -p "$ws"
printf 'alpha\n' > "$ws/a.txt"
printf 'beta\n'  > "$ws/b.txt"

# The Lane B block, when asked for. acknowledged_unconfined is required by the
# config validator and is not a formality here: this really does start somebody
# else's program once per turn, which is the whole point of soaking it.
servers_json=""
case "$MCP_SERVER" in
  "")     servers_json="" ;;
  echo)   servers_json=', "servers": { "soak": { "command": "'"$work/echoserver"'", "acknowledged_unconfined": true } }' ;;
  orphan) servers_json=', "servers": { "soak": { "command": "'"$work/badserver"'", "args": ["orphan"], "acknowledged_unconfined": true } }' ;;
  *)      echo "MCP_SERVER must be empty, echo or orphan" >&2; exit 1 ;;
esac

cat > "$work/models.json" <<EOF
{
  "config_version": 1,
  "default_tier": "primary",
  "tiers": { "primary": { "slug": "deepseek/deepseek-v4-flash", "active": true } },
  "mcp": {
    "enabled": true,
    "builtin": { "tools": { "list_directory": "$POLICY" } },
    "budget": { "max_iterations": $((TOOL_CALLS + 1)) }$servers_json
  }
}
EOF
printf 'You are a test fixture.\n' > "$work/system.txt"

"$work/harness-bin" -supabase "127.0.0.1:$SUPABASE_PORT" -upstream "127.0.0.1:$UPSTREAM_PORT" \
  -chunk-delay "$CHUNK_DELAY" -chunks "$CHUNKS" -supabase-delay "$SUPABASE_DELAY" \
  -token-limit "$TOKEN_LIMIT" -tool-calls "$TOOL_CALLS" \
  -reserve-fail-after "$RESERVE_FAIL_AFTER" \
  > "$work/harness.log" 2>&1 &
harness_pid=$!
sleep 1

OPENROUTER_API_KEY=fake \
OPENROUTER_API_BASE="http://127.0.0.1:$UPSTREAM_PORT" \
PORT="$PROXY_PORT" \
SUPABASE_URL="http://127.0.0.1:$SUPABASE_PORT" \
SUPABASE_SERVICE_ROLE_KEY=fake \
  "$work/proxy-bin" > "$work/proxy.log" 2>&1 &
proxy_pid=$!
sleep 1.5

if [ "$(curl -s -m 3 "http://127.0.0.1:$PROXY_PORT/health")" != '{"status":"ok"}' ]; then
  echo "proxy did not come up; see $work/proxy.log" >&2
  exit 1
fi

# XDG_RUNTIME_DIR is what protocol.SocketPath()/LockPath() key off, so the
# daemon and agentbench find each other without touching the developer's real
# socket -- a bench that killed someone's running daemon would be its own bug.
export XDG_RUNTIME_DIR="$work/run"
mkdir -p "$XDG_RUNTIME_DIR"

MOCHIII_API_BASE="http://127.0.0.1:$PROXY_PORT/v1" \
MOCHIII_API_KEY=mochi_fake \
  "$work/daemon-bin" -config "$work/models.json" -workspace "$ws" \
  -system-prompt "$work/system.txt" > "$work/daemon.log" 2>&1 &
daemon_pid=$!
sleep 2

if [ ! -f "$XDG_RUNTIME_DIR/mochiii/daemon.lock" ]; then
  echo "daemon did not come up; see $work/daemon.log" >&2
  tail -20 "$work/daemon.log" >&2
  exit 1
fi

# Resource sampling. Agent mode spawns and reaps an MCP registry PER TURN
# (runAgentTurn), so a missed Close is a process or fd leak that no short test
# can see -- the same reasoning soak.sh applies to the proxy. Sampled around the
# run rather than continuously: what matters is whether it PLATEAUS.
sample_resources() {
  local label="$1"
  local fds procs threads rss
  fds="$(ls /proc/$daemon_pid/fd 2>/dev/null | wc -l)"
  threads="$(awk '/^Threads:/{print $2}' /proc/$daemon_pid/status 2>/dev/null)"
  rss="$(awk '/^VmRSS:/{print $2}' /proc/$daemon_pid/status 2>/dev/null)"
  procs="$(pgrep -P "$daemon_pid" 2>/dev/null | wc -l)"
  printf '%-8s fds=%-5s threads=%-4s children=%-3s rss_kb=%s\n' "$label" "$fds" "$threads" "$procs" "$rss"
}

echo "running $TURNS turn(s), $TOOL_CALLS tool call(s) each, policy=$POLICY decision=$DECISION mcp_server=${MCP_SERVER:-none}"
echo
echo "=== daemon resources ==="
sample_resources "before"

# Sampled DURING the run, not only around it. Two endpoints distinguish growth
# from no growth; only a series distinguishes growth from a plateau, and a
# plateau is what the P2-3 threshold asks for.
sampler_pid=""
if [ "$SAMPLE_EVERY" != "0" ]; then
  ( while :; do sample_resources "during" >> "$work/samples.txt"; sleep "$SAMPLE_EVERY"; done ) &
  sampler_pid=$!
fi

"$work/agentbench" -turns "$TURNS" -workspace "$ws" -decision "$DECISION" \
  -think-ms "$THINK_MS" -pace "$PACE" > "$work/turns.json" 2>"$work/agentbench.err"

[ -n "$sampler_pid" ] && kill "$sampler_pid" 2>/dev/null
sample_resources "after"

# Every MCP server this daemon started should be gone with the turn that
# started it. A survivor is the per-turn teardown not working, and it is
# invisible in any run short enough to have only one turn.
if [ -n "$MCP_SERVER" ]; then
  strays="$(pgrep -f "$work/(echoserver|badserver)" 2>/dev/null | wc -l)"
  echo "stray MCP server processes after the run: $strays"
  if [ "$strays" != "0" ]; then pkill -f "$work/(echoserver|badserver)" 2>/dev/null; fi
fi

if [ -f "$work/samples.txt" ]; then
  echo
  echo "=== resource series (a plateau, or a slope?) ==="
  python3 - "$work/samples.txt" <<'PY'
import re, sys

rows = [dict(re.findall(r'(\w+)=(\d+)', line)) for line in open(sys.argv[1])]
rows = [r for r in rows if r]
if not rows:
    sys.exit(0)

print(f"{'#':>3} {'fds':>5} {'threads':>8} {'children':>9} {'rss_kb':>8}")
for i, r in enumerate(rows):
    print(f"{i:>3} {r.get('fds','?'):>5} {r.get('threads','?'):>8} "
          f"{r.get('children','?'):>9} {r.get('rss_kb','?'):>8}")

print()
for key in ("fds", "threads", "children", "rss_kb"):
    vals = [int(r[key]) for r in rows if key in r]
    if len(vals) < 4:
        continue
    half = len(vals) // 2
    first = sum(vals[:half]) / half
    second = sum(vals[half:]) / (len(vals) - half)
    # A plateau means the second half is not meaningfully above the first. 5%
    # is loose enough for GC noise and tight enough to catch a real slope.
    verdict = "PLATEAU" if second <= first * 1.05 else "STILL RISING"
    print(f"{key:>8}: first half {first:.0f}, second half {second:.0f}, peak {max(vals)} -> {verdict}")
PY
fi

ledger="$(curl -s -m 5 "http://127.0.0.1:$SUPABASE_PORT/__ledger")"

echo
echo "=== per-turn ==="
python3 - "$work/turns.json" "$TURNS" "$TOOL_CALLS" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
turns, expected_calls = int(sys.argv[2]), int(sys.argv[3])
print(f"{'turn':>4} {'ttft_ms':>8} {'wall_ms':>8} {'machine':>8} {'iters':>6} {'tools':>6} {'appr':>5} {'bytes':>7}  note")
for r in d["turns"]:
    note = r.get("error") or r.get("incomplete") or ""
    print(f"{r['turn']:>4} {r['ttft_ms']:>8} {r['wall_ms']:>8} {r['wall_ms']-r['waited_ms']:>8} "
          f"{r['iterations']:>6} {r['tool_calls']:>6} {r['approvals']:>5} {r['result_bytes']:>7}  {note}")
s = d["summary"]
print()
print(f"p50 wall {s['p50_wall_ms']}ms   p95 wall {s['p95_wall_ms']}ms   "
      f"p50 machine {s['p50_machine_ms']}ms   p50 ttft {s['p50_ttft_ms']}ms")
print(f"errors {s['errors']}   incomplete {s['incomplete']}   "
      f"tool calls {s['tool_calls_total']}   approvals {s['approvals_total']}   "
      f"tool bytes to model {s['result_bytes_total']}")
PY

echo
echo "=== money path (fake Supabase ledger) ==="
python3 - "$ledger" "$TURNS" "$TOOL_CALLS" <<'PY'
import json, sys
l = json.loads(sys.argv[1])
turns, tool_calls = int(sys.argv[2]), int(sys.argv[3])
calls = l["calls"]
reserves = sum(1 for c in calls if c == "reserve_usage")
auths    = sum(1 for c in calls if c == "authorize")
corrs    = l["corrections"]
expected_per_turn = tool_calls + 1
print(f"authorize        {auths}   ({auths/turns:.1f} per turn)")
print(f"reserve_usage    {reserves}   ({reserves/turns:.1f} per turn, expected {expected_per_turn})")
print(f"apply_correction {corrs}")
print(f"tokens reserved  {l['reserved']}   ({l['reserved']//turns} per turn)")
print(f"open pending     {l['open_pending']}   (>0 means a reservation was stranded)")

bodies = l.get("upstream_bodies") or []
tools  = l.get("upstream_tools") or []
if bodies:
    print()
    print("upstream request body bytes, one turn's worth (context growth per iteration):")
    one = bodies[:expected_per_turn]
    for i, b in enumerate(one, 1):
        carried = "tools" if i <= len(tools) and tools[i-1] else "-"
        delta = "" if i == 1 else f"  (+{b - one[i-2]})"
        print(f"  iter {i}: {b:>7} bytes  [{carried}]{delta}")
    if len(one) > 1:
        growth = one[-1] / one[0]
        print(f"  growth across the turn: {growth:.2f}x")
PY

echo
echo "artifacts in $work"
