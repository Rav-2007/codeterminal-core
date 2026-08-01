#!/usr/bin/env bash
#
# latency-bench.sh — per-stage latency of the REAL proxy binary, from its own
#                    access log, against the tracked test harness.
#
# Usage:
#   scripts/latency-bench.sh                      # proxy overhead only (loopback Supabase)
#   SUPABASE_DELAY=90ms scripts/latency-bench.sh  # production-shaped: 90ms per DB call
#   REQUESTS=100 scripts/latency-bench.sh
#
# WHY TWO MODES, and why reading only one of them is a mistake
#
#   SUPABASE_DELAY=0 (default) measures what the PROXY ITSELF costs. Everything
#   it reports is code, so a regression here is a regression someone wrote.
#
#   SUPABASE_DELAY=90ms models production. The 2026-07-17 measurement put
#   reserveQuota -- the SECOND sequential Supabase call -- at ~80-100ms, and that
#   cost is a ROUND TRIP, not CPU. It is invisible on loopback, which is exactly
#   why a loopback-only benchmark would report that the money path is free and
#   send someone off to optimise regexes instead.
#
#   The delayed mode is what prices a round-trip-count change: an auth cache, or
#   collapsing authorize+reserve into one RPC. Run both; report both.
#
# WHAT IT IS NOT: this is not end-to-end TTFT. It excludes the daemon, the
# network legs to Railway and to the provider, and the provider's own prefill --
# which the same 2026-07-17 note measured at ~1,125ms, about 78% of the total.
# No change to this repository moves that number. See docs/LATENCY_BASELINE.md.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="${WORK:-$(mktemp -d)}"
mkdir -p "$work"

REQUESTS="${REQUESTS:-60}"
WARMUP="${WARMUP:-5}"
SUPABASE_DELAY="${SUPABASE_DELAY:-0}"
# Small, so the stream finishes quickly. ttfb_ms is time to the FIRST chunk, so
# this does not distort it -- but it does bound how long the whole run takes.
CHUNK_DELAY="${CHUNK_DELAY:-5ms}"
CHUNKS="${CHUNKS:-8}"
# Seconds between requests. keyRatePerSecond is 2.0 (ratelimit.go), so an
# unpaced loop is throttled after the burst and the run silently measures a
# subset -- the first version of this script did exactly that and reported n=18
# for REQUESTS=40 without saying why. 0.55s stays under the per-key limit; the
# per-source pre-auth limit (5/s) is looser and not the binding one.
PACE="${PACE:-0.55}"

SUPABASE_PORT="${SUPABASE_PORT:-9501}"
UPSTREAM_PORT="${UPSTREAM_PORT:-9502}"
PROXY_PORT="${PROXY_PORT:-9500}"

harness_pid=""
proxy_pid=""
cleanup() {
  [ -n "$harness_pid" ] && kill "$harness_pid" 2>/dev/null
  [ -n "$proxy_pid" ] && kill "$proxy_pid" 2>/dev/null
  return 0
}
trap cleanup EXIT

echo "building..."
(cd "$repo_root/proxy" && go build -o "$work/proxy-bin" . && go build -o "$work/harness-bin" ./testharness) || {
  echo "build failed" >&2
  exit 1
}

"$work/harness-bin" -supabase "127.0.0.1:$SUPABASE_PORT" -upstream "127.0.0.1:$UPSTREAM_PORT" \
  -chunk-delay "$CHUNK_DELAY" -chunks "$CHUNKS" -supabase-delay "$SUPABASE_DELAY" \
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

body='{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}'

echo "supabase-delay=$SUPABASE_DELAY  requests=$REQUESTS (first $WARMUP discarded)  pace=${PACE}s"
total=$((REQUESTS + WARMUP))
for i in $(seq 1 "$total"); do
  curl -s -N -o /dev/null \
    -H "Authorization: Bearer mk_live_test" -H "Content-Type: application/json" \
    -d "$body" "http://127.0.0.1:$PROXY_PORT/v1/chat/completions"
  sleep "$PACE"
done

# Let the deferred finalizers land so the ledger read-out below is settled.
sleep 1

# The access log is the instrument. Parsing it here -- rather than timing curl --
# is deliberate: curl can only see the whole request, which is the number the
# proxy already had and the reason this stage exists.
python3 - "$work/proxy.log" "$WARMUP" "$REQUESTS" <<'PY'
import re, sys, statistics

path, warmup, expected = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
stages = ["auth_ms", "reserve_ms", "upstream_ms", "ttfb_ms", "latency_ms"]
rows = []
for line in open(path, encoding="utf-8", errors="replace"):
    if "msg=request" not in line or "path=/v1/chat/completions" not in line:
        continue
    if "status=200" not in line:
        continue
    vals = {}
    for s in stages:
        m = re.search(r"\b%s=(\d+)" % s, line)
        if m:
            vals[s] = int(m.group(1))
    rows.append(vals)

throttled = sum(1 for line in open(path, encoding="utf-8", errors="replace")
                if "msg=request" in line and "status=429" in line)

rows = rows[warmup:]
if not rows:
    print("no successful requests parsed -- check the proxy log", file=sys.stderr)
    sys.exit(1)

# A number computed from a silently-reduced sample is worse than no number: it
# looks like a measurement. Say so loudly, and fail rather than report.
if throttled:
    print("\n%d request(s) were RATE LIMITED (429). Raise PACE and re-run; these "
          "numbers describe only the survivors." % throttled, file=sys.stderr)
if len(rows) < expected:
    print("\nFAIL: parsed %d successful requests, expected %d. Refusing to report a "
          "median over a subset that was chosen by the rate limiter."
          % (len(rows), expected), file=sys.stderr)
    sys.exit(1)

def pct(xs, p):
    xs = sorted(xs)
    if len(xs) == 1:
        return xs[0]
    k = (len(xs) - 1) * p
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)

print()
print("n = %d successful requests" % len(rows))
print("%-14s %8s %8s %8s %8s" % ("stage", "median", "p90", "min", "max"))
for s in stages:
    xs = [r[s] for r in rows if s in r]
    if not xs:
        print("%-14s %8s   (never recorded -- stage did not run)" % (s, "-"))
        continue
    print("%-14s %8.0f %8.0f %8d %8d" % (
        s, statistics.median(xs), pct(xs, 0.90), min(xs), max(xs)))

# The parts must account for the whole, or one of them is lying.
med = {s: statistics.median([r[s] for r in rows if s in r])
       for s in stages if any(s in r for r in rows)}
parts = sum(med.get(s, 0) for s in ("auth_ms", "reserve_ms", "ttfb_ms"))
whole = med.get("latency_ms", 0)
print()
print("auth+reserve+ttfb = %.0f ms of a %.0f ms request (%.0f%%)" % (
    parts, whole, (100.0 * parts / whole) if whole else 0))
print("NOTE: upstream_ms and ttfb_ms overlap -- upstream_ms ends at response")
print("      HEADERS, ttfb_ms is measured from there to the first data chunk.")
PY

echo
echo "ledger: $(curl -s -m 3 "http://127.0.0.1:$SUPABASE_PORT/__ledger")"
echo "artifacts in $work"
