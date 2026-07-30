#!/usr/bin/env bash
#
# soak.sh — sustained load against the real proxy, looking for growth that a
#           short test cannot see.
#
# Usage:
#   scripts/soak.sh                      # 30 minutes (the real run)
#   DURATION=180 scripts/soak.sh         # 3 minutes, for checking the script
#   DURATION=1800 SAMPLE=30 CONCURRENCY=8 scripts/soak.sh
#
# NOT in per-PR CI, deliberately: a 30-minute job in the PR path is a job people
# route around, which is the failure mode this whole pipeline exists to fix. Run
# it before a release, or when something smells like a leak.
#
# WHAT IT WATCHES, and why each number is here rather than "some metrics"
#
#   threads  /proc/<pid>/status   Phase-0 plateau: 11 under 12 concurrent
#   fds      /proc/<pid>/fd       Phase-0 plateau: 10, and it did NOT grow with
#                                 request count -- that plateau is the assertion
#   rss_kb   /proc/<pid>/VmRSS    Phase-0: 12,792 kB settled
#   goroutines        /admin/metrics
#   rate_limiter_buckets  /admin/metrics
#
# Baselines are from docs/ROBUSTNESS_BASELINE.md §2, measured on the real binary.
#
# THE BUCKET DIMENSION IS THE POINT of running long. The limiters hold one bucket
# per distinct api_keys.id, reclaimed by sweepLocked only after bucketIdleTTL
# (10 minutes) and at most every bucketSweepE (2 minutes). A run shorter than
# ~12 minutes cannot observe a reclamation at all, so it cannot tell a working
# sweep from an absent one. The harness is driven with -distinct-keys so buckets
# genuinely accumulate; a passing 30-minute run means they also genuinely go away.
#
# VERDICT: compares the median of the SECOND half of samples against the FIRST
# half, after a warmup. Medians rather than endpoints because a single GC pause
# or scheduling blip at the wrong sample would otherwise decide the outcome.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DURATION="${DURATION:-1800}"     # seconds
SAMPLE="${SAMPLE:-15}"           # seconds between samples
CONCURRENCY="${CONCURRENCY:-4}"  # parallel request loops
WARMUP="${WARMUP:-30}"           # seconds of samples to discard
DISTINCT_KEYS="${DISTINCT_KEYS:-200}"

# Pause between requests in each loop, so the OFFERED rate stays under the
# proxy's global pre-auth ceiling (preAuthRateGlobalPerSecond = 50/s, keyed by a
# fixed "global" bucket that every request shares).
#
# This is not politeness, it is validity. Without it, 4 loops offer ~400 req/s,
# the global bucket is permanently empty, and the overwhelming majority of
# "load" is a 429 that never reaches the money path -- so the soak would be
# measuring the rate limiter's rejection path while appearing to measure
# streaming. It also starves the metrics scraper, which shares that same global
# bucket no matter what source address it comes from.
#
# 4 loops x ~6.7 req/s ~= 27/s, comfortably inside 50/s with headroom to scrape.
REQUEST_DELAY="${REQUEST_DELAY:-0.15}"

# Growth allowed between the first and second half, as a percentage of the first
# half's median. Generous on RSS because Go returns memory to the OS lazily; tight
# on fds and goroutines, where growth has no benign explanation.
RSS_GROWTH_PCT="${RSS_GROWTH_PCT:-25}"
FD_GROWTH_PCT="${FD_GROWTH_PCT:-10}"
GOROUTINE_GROWTH_PCT="${GOROUTINE_GROWTH_PCT:-15}"
# Buckets legitimately climb to a steady state before the 10-minute idle TTL
# starts reclaiming, so this is the loosest threshold here. A sweep that never
# fires shows ~200%+; a working one settles well under 50%.
BUCKET_GROWTH_PCT="${BUCKET_GROWTH_PCT:-50}"

work="$(mktemp -d)"
csv="$work/samples.csv"
ADMIN_TOKEN="soak-admin-token-at-least-24-chars"

SUPABASE_PORT=9501
UPSTREAM_PORT=9502
PROXY_PORT=9500

harness_pid=""
proxy_pid=""
load_pids=()
cleanup() {
  for pid in "${load_pids[@]:-}"; do kill "$pid" 2>/dev/null; done
  [ -n "$harness_pid" ] && kill "$harness_pid" 2>/dev/null
  [ -n "$proxy_pid" ] && kill "$proxy_pid" 2>/dev/null
  return 0
}
trap cleanup EXIT

echo "building..."
(cd "$repo_root/proxy" && go build -o "$work/proxy-bin" . && go build -o "$work/harness-bin" ./testharness) || exit 1

# Short chunks: the soak wants request THROUGHPUT, not long individual streams.
"$work/harness-bin" -supabase "127.0.0.1:$SUPABASE_PORT" -upstream "127.0.0.1:$UPSTREAM_PORT" \
  -chunk-delay 1ms -chunks 5 -distinct-keys "$DISTINCT_KEYS" > "$work/harness.log" 2>&1 &
harness_pid=$!
sleep 1

OPENROUTER_API_KEY=fake \
OPENROUTER_API_BASE="http://127.0.0.1:$UPSTREAM_PORT" \
PORT="$PROXY_PORT" \
SUPABASE_URL="http://127.0.0.1:$SUPABASE_PORT" \
SUPABASE_SERVICE_ROLE_KEY=fake \
PROXY_ADMIN_TOKEN="$ADMIN_TOKEN" \
  "$work/proxy-bin" > "$work/proxy.log" 2>&1 &
proxy_pid=$!
sleep 2

if [ "$(curl -s -m 3 "http://127.0.0.1:$PROXY_PORT/health")" != '{"status":"ok"}' ]; then
  echo "proxy did not come up; see $work/proxy.log" >&2
  exit 1
fi

# The listener PID, not the wrapper. `pgrep -f` also matches a setsid wrapper,
# which is how Phase 0 first mis-sampled these numbers.
pid="$proxy_pid"
echo "proxy pid $pid, duration ${DURATION}s, sampling every ${SAMPLE}s, ${CONCURRENCY} loops"

body='{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}'

# Each request carries a DISTINCT X-Forwarded-For, for two reasons.
#
# It is more realistic -- real traffic is many clients, not one hammering host --
# and clientSource reads XFF first, so this is what the per-source limiter would
# actually see behind a load balancer.
#
# It also keeps the metrics scraper alive. Without it, the load and the scrape
# share the 127.0.0.1 bucket (5/s, burst 20), the bucket is permanently empty
# under load, and every scrape after the first 429s -- which is exactly what the
# first version of this script did. Now the load spreads across synthetic
# sources and the scraper has 127.0.0.1 to itself.
#
# The side effect is the one the soak most wants: preAuthPerSource accumulates a
# bucket per synthetic IP, so bucket growth AND its reclamation become visible.
for i in $(seq 1 "$CONCURRENCY"); do
  (
    n=0
    while :; do
      n=$((n + 1))
      octet3=$(( (n / 250) % 250 ))
      octet4=$(( n % 250 ))
      curl -s -o /dev/null -m 30 \
        -H "Authorization: Bearer mk_live_soak_$i" \
        -H "X-Forwarded-For: 10.$i.$octet3.$octet4" \
        -H "Content-Type: application/json" \
        -d "$body" "http://127.0.0.1:$PROXY_PORT/v1/chat/completions"
      sleep "$REQUEST_DELAY"
    done
  ) &
  load_pids+=($!)
done

echo "elapsed,threads,fds,rss_kb,goroutines,buckets" > "$csv"
start=$(date +%s)
while :; do
  now=$(date +%s)
  elapsed=$((now - start))
  [ "$elapsed" -ge "$DURATION" ] && break

  threads=$(awk '/^Threads:/ {print $2}' "/proc/$pid/status" 2>/dev/null)
  fds=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l)
  rss=$(awk '/^VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null)

  # The admin scrape competes with the LOAD for the same pre-auth rate-limit
  # bucket: both come from 127.0.0.1, and admitPreAuth is keyed by source IP
  # (deliberately -- every route is gated, including this one). Under sustained
  # load the bucket is empty and a single scrape attempt gets a 429, which is
  # how the first version of this script recorded "?" for every sample after the
  # first. Retry briefly; the load loops pause between requests, so a gap opens.
  #
  # Worth knowing operationally, not just here: on any deployment where the
  # scraper shares a source address with real traffic -- or sits behind a proxy
  # that collapses client IPs -- metrics can be throttled exactly when load is
  # highest, i.e. when you most want them.
  goroutines=""
  buckets=""
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    metrics=$(curl -s -m 3 -H "Authorization: Bearer $ADMIN_TOKEN" \
      "http://127.0.0.1:$PROXY_PORT/admin/metrics")
    goroutines=$(grep -oE '"goroutines": *[0-9]+' <<<"$metrics" | grep -oE '[0-9]+$')
    buckets=$(grep -oE '"rate_limiter_buckets": *[0-9]+' <<<"$metrics" | grep -oE '[0-9]+$')
    [ -n "$goroutines" ] && [ -n "$buckets" ] && break
    sleep 0.5
  done

  if [ -z "${threads:-}" ]; then
    echo "proxy died at ${elapsed}s; see $work/proxy.log" >&2
    exit 1
  fi
  if [ -z "$goroutines" ] || [ -z "$buckets" ]; then
    echo "could not scrape /admin/metrics at ${elapsed}s after 10 attempts -- the " >&2
    echo "pre-auth limiter is starving the scraper; lower CONCURRENCY." >&2
    exit 1
  fi
  echo "$elapsed,$threads,$fds,$rss,$goroutines,$buckets" >> "$csv"
  printf "  t=%-6s threads=%-4s fds=%-4s rss=%-8s goroutines=%-6s buckets=%-6s\n" \
    "$elapsed" "$threads" "$fds" "$rss" "$goroutines" "$buckets"
  sleep "$SAMPLE"
done

# ---- verdict ---------------------------------------------------------------
#
# median of second half vs first half, over post-warmup samples.
# bucketIdleTTL is 10 minutes and bucketSweepE 2 minutes, so no reclamation can
# possibly have happened in a run shorter than roughly twelve. Judging buckets on
# a short run would report a FAIL for a sweep that simply has not come due yet --
# and a false failure is worse than no signal, because someone eventually "fixes"
# a bug that was never there. Short runs still record the column; they just do
# not rule on it.
JUDGE_BUCKETS=1
if [ "$DURATION" -lt 900 ]; then
  JUDGE_BUCKETS=0
fi

verdict=$(awk -F, -v warmup="$WARMUP" -v rssPct="$RSS_GROWTH_PCT" \
             -v fdPct="$FD_GROWTH_PCT" -v grPct="$GOROUTINE_GROWTH_PCT" \
             -v buckPct="$BUCKET_GROWTH_PCT" -v judgeBuckets="$JUDGE_BUCKETS" '
  # Median of arr[lo..hi], via insertion sort into a scratch array. Defined at
  # top level because awk only allows function definitions there -- nesting it
  # inside END is a syntax error, which is how the first version of this script
  # produced an empty verdict while still exiting through the success path.
  function med(arr, lo, hi,   i, j, s, tmp, cnt) {
    cnt = 0
    for (i = lo; i <= hi; i++) { cnt++; s[cnt] = arr[i] }
    for (i = 2; i <= cnt; i++) { tmp = s[i]; j = i-1
      while (j > 0 && s[j] > tmp) { s[j+1] = s[j]; j-- }
      s[j+1] = tmp }
    return (cnt % 2) ? s[int(cnt/2)+1] : (s[cnt/2] + s[cnt/2+1]) / 2
  }
  NR == 1 { next }
  $1 >= warmup { n++; t[n]=$2; f[n]=$3; r[n]=$4; g[n]=$5; b[n]=$6 }
  END {
    if (n < 4) { print "INCONCLUSIVE|too few samples after warmup (" n ")|"; exit }
    half = int(n/2)

    rss1 = med(r, 1, half);        rss2 = med(r, half+1, n)
    fd1  = med(f, 1, half);        fd2  = med(f, half+1, n)
    gr1  = med(g, 1, half);        gr2  = med(g, half+1, n)
    bMax = 0; for (i = 1; i <= n; i++) if (b[i] > bMax) bMax = b[i]
    # Sample-to-sample DECREASES are direct evidence that sweepLocked reclaimed
    # something. A steady state shows a sawtooth: buckets climb with arrivals and
    # drop every bucketSweepE. This is strictly better than the median comparison
    # below, which is a threshold that has to be tuned and passed with only ~10
    # points of margin on the first real 30-minute run. A sweep that never fires
    # produces a monotonically rising series and ZERO decreases, no matter what
    # the rates happen to be.
    drops = 0
    for (i = 2; i <= n; i++) if (b[i] < b[i-1]) drops++
    bLast = b[n]
    buck1 = med(b, 1, half); buck2 = med(b, half+1, n)

    fail = ""
    if (rss1 > 0 && (rss2 - rss1) * 100.0 / rss1 > rssPct)
      fail = fail sprintf("RSS %d->%d kB (+%.1f%%, allowed %s%%); ", rss1, rss2, (rss2-rss1)*100.0/rss1, rssPct)
    if (fd1 > 0 && (fd2 - fd1) * 100.0 / fd1 > fdPct)
      fail = fail sprintf("FDs %d->%d (+%.1f%%, allowed %s%%); ", fd1, fd2, (fd2-fd1)*100.0/fd1, fdPct)
    if (gr1 > 0 && (gr2 - gr1) * 100.0 / gr1 > grPct)
      fail = fail sprintf("goroutines %d->%d (+%.1f%%, allowed %s%%); ", gr1, gr2, (gr2-gr1)*100.0/gr1, grPct)
    # Buckets are EXPECTED to grow: the load presents a new synthetic client IP
    # per request, and each gets a bucket. What must happen is that they
    # PLATEAU, at roughly (arrival rate x bucketIdleTTL), because sweepLocked
    # reclaims the idle ones. If the sweep never ran they would instead grow
    # linearly for the whole run, putting the second-half median at ~3x the
    # first-half. That is the signal this threshold separates.
    if (judgeBuckets == 1 && drops == 0)
      fail = fail sprintf("limiter buckets NEVER decreased across %d samples -- the series is monotonic, so sweepLocked reclaimed nothing all run; ", n)
    if (judgeBuckets == 1 && buck1 > 0 && (buck2 - buck1) * 100.0 / buck1 > buckPct)
      fail = fail sprintf("limiter buckets %d->%d (+%.1f%%, allowed %s%%) -- still growing rather than plateauing, so sweepLocked is not reclaiming; ", buck1, buck2, (buck2-buck1)*100.0/buck1, buckPct)

    bnote = (judgeBuckets == 1) ? "" : " [buckets NOT judged: run < 15m, sweep not yet due]"
    printf "%s|samples=%d rss %d->%d kB, fds %d->%d, goroutines %d->%d, buckets %d->%d (peak %d, %d reclamations)%s|%s\n",
      (fail == "" ? "PASS" : "FAIL"), n, rss1, rss2, fd1, fd2, gr1, gr2, buck1, buck2, bMax, drops, bnote, fail
  }' "$csv")

result="${verdict%%|*}"
rest="${verdict#*|}"
summary="${rest%%|*}"
detail="${rest#*|}"

echo
echo "samples: $csv"
echo "summary: $summary"
echo "VERDICT: $result"
[ "$result" = "PASS" ] || echo "reasons: $detail" >&2

echo
echo "Phase-0 baseline for comparison (docs/ROBUSTNESS_BASELINE.md §2):"
echo "  proxy settled under 12 concurrent: 11 threads / 10 fds / 12,792 kB"

[ "$result" = "PASS" ]
