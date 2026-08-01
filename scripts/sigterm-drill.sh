#!/usr/bin/env bash
#
# sigterm-drill.sh — reproduce the Phase-0 finding, and prove P1.2 still fixes it.
#
# Usage: scripts/sigterm-drill.sh [workdir]     (default: a mktemp -d)
#
# WHAT THIS IS
#
# Phase 0 measured, against the real proxy binary, that a SIGTERM delivered
# mid-stream produced `authorize -> reserve_usage` and NOTHING ELSE: the client
# got a truncated body with no error frame (curl exit 18), and 3,959 of 4,096
# reserved tokens stayed charged forever because the sweep deliberately never
# refunds. A platform redeploy is a SIGTERM, so it fired on every deploy for
# every request in flight.
#
# P1.2 fixed it. This script is how you check that it STAYS fixed, against real
# processes and a real TCP client rather than in-process fakes -- the two things
# proxy/integration_test.go structurally cannot do.
#
# EXPECTED OUTPUT (the Phase-1 gate):
#
#   chunks at SIGTERM:  ~13     <- signal genuinely landed mid-stream
#   curl:               HTTP=200 CURL_EXIT=0
#   chunks / DONE:      42 / 1  <- stream ran to completion anyway
#   ledger:             apply_correction(tokens=-3959,pending=1001)
#                       open_pending: 0
#   log:                "drain complete, all in-flight requests finished"
#
# The BASELINE (pre-P1.2) output, for comparison: curl exit 18, ~13 chunks and no
# DONE frame, ledger showing reserve_usage with no correction, open_pending: 1.
#
# Note the model must be one of defaultAllowedModels (proxy/main.go) or the
# request is refused at the cost_surface gate before the money path is reached --
# which is itself a demonstration that the gate works, but not this drill.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="${1:-$(mktemp -d)}"
mkdir -p "$work"

SUPABASE_PORT=9401
UPSTREAM_PORT=9402
PROXY_PORT=9400

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
  -chunk-delay 200ms -chunks 40 > "$work/harness.log" 2>&1 &
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

health="$(curl -s -m 3 "http://127.0.0.1:$PROXY_PORT/health")"
echo "health:            $health"
if [ "$health" != '{"status":"ok"}' ]; then
  echo "proxy did not come up; see $work/proxy.log" >&2
  exit 1
fi

body='{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}'

: > "$work/stream.out"
curl -s -N -o "$work/stream.out" -w "HTTP=%{http_code} CURL_EXIT=%{exitcode}" \
  -H "Authorization: Bearer mk_live_test" -H "Content-Type: application/json" \
  -d "$body" "http://127.0.0.1:$PROXY_PORT/v1/chat/completions" > "$work/curl.txt" 2>&1 &
curl_pid=$!

# Long enough that the stream is genuinely under way, short enough that it is
# nowhere near finished -- the signal must land MID-stream or this proves nothing.
sleep 2.5
mid="$(grep -c 'data: ' "$work/stream.out")"
echo "chunks at SIGTERM: $mid"
if [ "$mid" -eq 0 ]; then
  echo "no chunks had been relayed: the signal would not land mid-stream" >&2
  exit 1
fi

kill -TERM "$proxy_pid"
wait "$curl_pid"

echo "curl:              $(cat "$work/curl.txt")"
echo "chunks / DONE:     $(grep -c 'data: ' "$work/stream.out") / $(grep -c '\[DONE\]' "$work/stream.out")"
sleep 0.5
echo "ledger:            $(curl -s -m 3 "http://127.0.0.1:$SUPABASE_PORT/__ledger")"
echo "shutdown log:"
grep -E "draining|drain complete|drain INCOMPLETE|ABANDONED|exiting" "$work/proxy.log" | sed 's/^/  /'

echo
echo "artifacts in $work"
