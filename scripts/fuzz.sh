#!/usr/bin/env bash
#
# fuzz.sh — run every fuzz target for a bounded time.
#
# Usage:
#   scripts/fuzz.sh            # 30s per target (what CI runs)
#   FUZZTIME=5m scripts/fuzz.sh
#
# WHAT IS FUZZED, and why only these
#
# Every target reads bytes this codebase did not author: a caller's request
# body, an SSE line from OpenRouter, or raw model output. Nothing else is
# fuzzed, because nothing else is a trust boundary -- fuzzing our own callers
# would measure the tests, not the exposure.
#
# The invariants are in the target files. Two are worth naming here because they
# are the reason this is not crash-hunting:
#
#   - zdrRoutingEnforced is the F1 gate. A false positive FORWARDS a request
#     that was never proven to carry zero-data-retention flags, so the target
#     asserts against json.Valid as an independent oracle.
#   - findSearch returns byte offsets a caller slices FILE CONTENT with. An
#     out-of-range offset is a corrupt write to a user's source file, not a
#     parse bug. A planted off-by-one on End is caught in 0.03s.
#
# CI runs this per-PR at 30s/target. That is a regression gate, not a search:
# 30 seconds re-walks the corpus and the shallow mutations. Finding something
# NEW wants minutes to hours, which is what the FUZZTIME override is for.
#
# Go writes any input that fails to testdata/fuzz/<Target>/ in the module. That
# file is a permanent regression seed -- COMMIT IT, then fix the bug. The seed
# corpus itself lives in f.Add calls inside each target, which keeps a seed next
# to the invariant it was chosen to exercise.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fuzztime="${FUZZTIME:-30s}"

# Go prints durations as "31s", "1m2s", "1h0m5s", and FUZZTIME is written the
# same way. One parser for both, so the value this script passes to -fuzztime
# and the value it reads back out of Go's progress lines cannot be interpreted
# by two different rules.
#
# An unparseable value returns 0, which makes the deadline case below
# unreachable. That is deliberate: this parser failing must fail CLOSED.
duration_seconds() {
  local v="$1" total=0 n
  if [[ "$v" =~ ^([0-9]+)h ]]; then total=$(( total + BASH_REMATCH[1] * 3600 )); v="${v#*h}"; fi
  if [[ "$v" =~ ^([0-9]+)m ]]; then total=$(( total + BASH_REMATCH[1] * 60 ));   v="${v#*m}"; fi
  if [[ "$v" =~ ^([0-9]+)(\.[0-9]+)?s ]]; then n="${BASH_REMATCH[1]}"; total=$(( total + n )); fi
  printf '%s' "$total"
}

# WHY A `context deadline exceeded` WITH NO CRASHER IS NOT A FINDING.
#
# Established by reading the toolchain, not inferred from a log. In
# src/internal/fuzz/fuzz.go, -fuzztime becomes a CONTEXT DEADLINE:
#
#     if opts.Timeout > 0 {                        // opts.Timeout == -fuzztime
#         ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
#     }
#     fuzzCtx, cancelWorkers := context.WithCancel(ctx)   // CHILD of ctx
#
# When the deadline fires the coordinator calls stop(ctx.Err()), and stop tries
# to suppress it as a normal termination:
#
#     if err == fuzzCtx.Err() || isInterruptError(err) { err = nil }
#     if err != nil && (fuzzErr == nil || fuzzErr == ctx.Err()) { fuzzErr = err }
#
# fuzzCtx is a CHILD of ctx, so the parent's cancellation reaches it
# ASYNCHRONOUSLY. If fuzzCtx has already observed the deadline, the comparison
# holds, the error is suppressed and the run passes. If propagation has not
# completed at that instant, fuzzCtx.Err() is still nil, fuzzErr becomes
# context.DeadlineExceeded, and the identical run FAILS.
#
# The outcome is decided by goroutine scheduling at one instant. Measured at 4
# failures in 15 attempts across three unrelated targets (FuzzStreamRequested,
# FuzzStripSSEAccountMetadata twice, FuzzPeekUsageTotal), and that 27% is a
# FLOOR rather than a rate, because a re-run overwrites a run's conclusion and
# the first attempt stops being visible in the run list.
#
# So this outcome carries NO INFORMATION about the code under test, and raising
# FUZZTIME cannot help -- the deadline IS FUZZTIME. This script already said as
# much in its own message and then set status=1 four lines later, failing
# against its own classification. That is the defect being fixed.
#
# WHAT IS NOT TOLERATED, and the ordering matters. The worker-died family is
# matched BEFORE the deadline case, because Go reports one of them with NO
# crasher file written -- "terminated by unexpected signal; no crash will be
# recorded" says so outright. Keying only on "is there a crasher?" would absorb
# a genuinely dead worker into the tolerated branch.
#
# Every conjunct on the tolerated case does work: a deadline firing EARLY is a
# real problem, and a deadline with zero execs is the vacuity case the
# "seed corpus only" branch below exists for. Anything unrecognised is OTHER,
# which fails.
classify_fuzz_outcome() {
  local rc="$1" fz_secs="$2" out="$3"
  local last_elapsed last_execs

  if grep -q "Failing input written to" <<<"$out"; then
    printf 'CRASH'; return
  fi

  if grep -qE 'fuzzing process (hung or terminated unexpectedly|exited unexpectedly due to an internal failure|terminated by unexpected signal|terminated without fuzzing)' <<<"$out"; then
    printf 'HANG'; return
  fi

  if [ "$rc" -eq 0 ]; then
    if grep -qE 'execs: [0-9]+' <<<"$out"; then printf 'OK'; else printf 'NO_EXECS'; fi
    return
  fi

  if grep -q 'context deadline exceeded' <<<"$out"; then
    last_elapsed="$(grep -oE 'elapsed: [0-9hms.]+' <<<"$out" | tail -1 | sed 's/elapsed: //')"
    last_execs="$(grep -oE 'execs: [0-9]+' <<<"$out" | tail -1 | grep -oE '[0-9]+')"
    if [ -n "$last_elapsed" ] && [ -n "$last_execs" ] && [ "$fz_secs" -gt 0 ] \
       && [ "$(duration_seconds "$last_elapsed")" -ge "$fz_secs" ] \
       && [ "$last_execs" -gt 0 ]; then
      printf 'COMPLETED_AT_DEADLINE'; return
    fi
  fi

  printf 'OTHER'
}

# module:Target pairs. Listed explicitly rather than discovered, so that adding a
# target is a deliberate act that shows up in review -- and so a target that
# stops being run is visible as a deletion.
TARGETS=(
  "proxy:FuzzTopLevelFields"
  "proxy:FuzzStreamRequested"
  "proxy:FuzzZDRRoutingEnforced"
  "proxy:FuzzPeekMaxTokens"
  "proxy:FuzzPeekUsageTotal"
  "proxy:FuzzStripSSEAccountMetadata"
  "proxy:FuzzExtractUsageAndProvider"
  "editapply:FuzzParseEditBlocks"
  "editapply:FuzzFindSearch"
  # The unified-diff reader, added 2026-08-29 with ingestion. Same rule as the
  # agent-mode note below, and the same result: FuzzParseUnifiedDiff found two
  # real defects in under a minute on its first run -- a one-sided hunk-count
  # check that let a body overrun its header into an empty-file create, and a
  # NUL byte reaching an EditBlock's FilePath. The second turned out to be a
  # PRE-EXISTING hole in FuzzParseEditBlocks' own contract, which had asserted
  # the property for weeks without ever generating an input that broke it.
  "editapply:FuzzParseUnifiedDiff"
  "editapply:FuzzParseEditPayload"
  # Agent mode's four readers of bytes this codebase did not author. Added
  # 2026-08-01 by the agent-mode QA gate, which found the rule above stated and
  # not applied: the daemon had no targets at all, and FuzzToolCallAccumulator
  # found a real defect in 4.5 seconds on its first run.
  "daemon:FuzzRenderToolResult"
  "daemon:FuzzVerifyApproval"
  "daemon:FuzzToolCallAccumulator"
  "daemon:FuzzSplitQualifiedName"

  # The TUI had targets and no entries here, so neither sanitizer fuzzer had
  # ever been run by this gate -- a fuzz target nobody runs is a comment.
  # FuzzIncrementalRenderMatchesFull is the render cache's equivalence gate:
  # cache.render must equal renderTranscript byte for byte, seeded from the
  # sanitizer corpus because those are the escapes that actually reach a
  # transcript.
  "clients/tui:FuzzSanitizeNoEscapeSurvives"
  "clients/tui:FuzzSanitizeChunkInvariance"
  "clients/tui:FuzzIncrementalRenderMatchesFull"
)

# THE SELF-TEST, and it is the ONLY thing that can verify the classifier.
#
# The condition it exists for is intermittent -- a green CI run means the race
# did not fire, not that the handling is right -- so a run can never confirm
# this. A known-answer table can.
#
# The first arm is the REAL recorded output of the fuzz job in run 35179379541
# on main, the run this rule was written from. Its middle progress lines are
# elided and the ones the classifier actually reads (the LAST elapsed and the
# LAST execs) are kept verbatim.
SELF_TEST_FAILURES=0
SELF_TEST_ARMS=0
SELF_TEST_EXPECT_FAIL=0
SELF_TEST_EXPECT_PASS=0

# arm <name> <expected token> <rc> <fuzztime> <output>
arm() {
  local name="$1" want="$2" rc="$3" fz="$4" out="$5" got
  SELF_TEST_ARMS=$((SELF_TEST_ARMS + 1))
  case "$want" in
    CRASH|HANG|OTHER) SELF_TEST_EXPECT_FAIL=$((SELF_TEST_EXPECT_FAIL + 1)) ;;
    *)                SELF_TEST_EXPECT_PASS=$((SELF_TEST_EXPECT_PASS + 1)) ;;
  esac
  got="$(classify_fuzz_outcome "$rc" "$(duration_seconds "$fz")" "$out")"
  if [ "$got" = "$want" ]; then
    echo "  ok   $name -> $got"
  else
    echo "  FAIL $name -> got '$got', want '$want'" >&2
    SELF_TEST_FAILURES=$((SELF_TEST_FAILURES + 1))
  fi
}

REAL_DEADLINE_OUTPUT='fuzz: elapsed: 0s, gathering baseline coverage: 7/7 completed, now fuzzing with 2 workers
fuzz: elapsed: 3s, execs: 22154 (7384/sec), new interesting: 65 (total: 72)
fuzz: elapsed: 24s, execs: 239045 (9917/sec), new interesting: 174 (total: 181)
fuzz: elapsed: 27s, execs: 240134 (363/sec), new interesting: 174 (total: 181)
fuzz: elapsed: 30s, execs: 302618 (20841/sec), new interesting: 179 (total: 186)
fuzz: elapsed: 31s, execs: 302618 (0/sec), new interesting: 179 (total: 186)
--- FAIL: FuzzStreamRequested (31.00s)
    context deadline exceeded
FAIL
FAIL	mochiii/proxy	31.008s'

run_self_test() {
  echo "fuzz: self-test -- classify_fuzz_outcome against known answers"

  # THE ONE THIS RULE EXISTS FOR, verbatim from CI.
  arm "real recorded deadline (run 35179379541)" COMPLETED_AT_DEADLINE 1 30s "$REAL_DEADLINE_OUTPUT"

  # A deadline that fired EARLY is not the race; something else ended the run.
  arm "deadline at 3s against FUZZTIME=30s" OTHER 1 30s \
'fuzz: elapsed: 3s, execs: 1200 (400/sec), new interesting: 2 (total: 9)
--- FAIL: FuzzX (3.00s)
    context deadline exceeded
FAIL'

  # A deadline having generated NOTHING is the vacuity case, not a race.
  arm "deadline with zero execs" OTHER 1 30s \
'fuzz: elapsed: 31s, execs: 0 (0/sec)
--- FAIL: FuzzX (31.00s)
    context deadline exceeded
FAIL'

  # An unparseable FUZZTIME must make the tolerated branch UNREACHABLE.
  arm "unparseable FUZZTIME fails closed" OTHER 1 banana "$REAL_DEADLINE_OUTPUT"

  # A real finding, and it must still be one when a deadline is also present.
  arm "crasher" CRASH 1 30s \
'--- FAIL: FuzzX (0.12s)
    testdata corpus entry failed
Failing input written to testdata/fuzz/FuzzX/abc123
FAIL'
  arm "crasher wins over a deadline" CRASH 1 30s \
"$REAL_DEADLINE_OUTPUT
Failing input written to testdata/fuzz/FuzzX/abc123"

  # THE WORKER-DIED FAMILY. Each string is taken from
  # src/internal/fuzz/worker.go, and each must fail even though NONE of them
  # writes a crasher file -- which is exactly why they are matched first.
  arm "worker hung"            HANG 1 30s 'fuzz: elapsed: 31s, execs: 400 (0/sec)
--- FAIL: FuzzX (31.00s)
    fuzzing process hung or terminated unexpectedly: signal: killed
FAIL'
  arm "worker internal failure" HANG 1 30s \
'--- FAIL: FuzzX (2.00s)
    fuzzing process exited unexpectedly due to an internal failure: EOF
FAIL'
  arm "worker signalled"        HANG 1 30s \
'--- FAIL: FuzzX (2.00s)
    fuzzing process terminated by unexpected signal; no crash will be recorded: signal: killed
FAIL'
  arm "worker never fuzzed"     HANG 1 30s \
'--- FAIL: FuzzX (2.00s)
    fuzzing process terminated without fuzzing: exit status 2
FAIL'

  # The ordinary outcomes.
  arm "clean pass" OK 0 30s \
'fuzz: elapsed: 30s, execs: 314446 (10481/sec), new interesting: 12 (total: 40)
PASS
ok  	mochiii/proxy	30.100s'
  arm "ran but never fuzzed" NO_EXECS 0 30s \
'fuzz: elapsed: 0s, gathering baseline coverage: 0/160 completed
PASS
ok  	mochiii/daemon	30.050s'

  # A FLOOR ON THE TABLE ITSELF. A self-test that lost its failing arms would
  # pass while proving only that nothing fails, which is the shape this gate
  # was built to catch elsewhere.
  if [ "$SELF_TEST_ARMS" -lt 12 ]; then
    echo "fuzz: self-test FAIL -- only $SELF_TEST_ARMS arm(s); the table has been emptied" >&2
    return 1
  fi
  if [ "$SELF_TEST_EXPECT_FAIL" -lt 6 ] || [ "$SELF_TEST_EXPECT_PASS" -lt 3 ]; then
    echo "fuzz: self-test FAIL -- $SELF_TEST_EXPECT_FAIL failing and $SELF_TEST_EXPECT_PASS passing arm(s);" >&2
    echo "      a table that only asserts passes proves the classifier cannot fail." >&2
    return 1
  fi
  if [ "$SELF_TEST_FAILURES" -ne 0 ]; then
    echo "fuzz: self-test FAIL -- $SELF_TEST_FAILURES of $SELF_TEST_ARMS arm(s) misclassified" >&2
    return 1
  fi
  echo "fuzz: self-test ok -- $SELF_TEST_ARMS arm(s), $SELF_TEST_EXPECT_FAIL of them failing shapes"
  return 0
}

if [ "${1:-}" = "--self-test" ]; then
  run_self_test
  exit $?
fi

fuzztime_secs="$(duration_seconds "$fuzztime")"
if [ "$fuzztime_secs" -eq 0 ]; then
  echo "FAIL  FUZZTIME='$fuzztime' is not a duration this script can parse." >&2
  echo "      Use a form like 30s, 5m or 1h. Refusing to run rather than" >&2
  echo "      classify outcomes against a budget of zero." >&2
  exit 1
fi

status=0
ran=0
deadline_count=0
for entry in "${TARGETS[@]}"; do
  module="${entry%%:*}"
  target="${entry##*:}"

  # THE TARGET MUST EXIST BEFORE IT CAN BE CLEAN.
  #
  # `go test -run ^X$ -fuzz ^X$` with no matching target prints "no tests to
  # run" and EXITS 0. This script reported that as `ok X (no exec count)` --
  # indistinguishable from a target that ran. That is not hypothetical: from
  # task 2.1 until 2026-09-04 the two TUI sanitizer fuzzers existed and were
  # not in TARGETS at all, and this gate reported green the whole time. The
  # same hole swallows a target that is renamed or deleted.
  #
  # `go test -list` prints the names it matched, so an empty match is a
  # missing target and is now a failure rather than a pass.
  listed="$( (cd "$repo_root/$module" && go test -list "^${target}\$" . 2>/dev/null) | grep -c "^${target}\$" )"
  if [ "$listed" -eq 0 ]; then
    echo "FAIL  $module/$target — no such fuzz target." >&2
    echo "      It was renamed, deleted, or moved to another module. A target listed" >&2
    echo "      here and absent from the code is a gate reporting on nothing." >&2
    status=1
    continue
  fi

  out="$(cd "$repo_root/$module" && go test -run "^${target}\$" -fuzz "^${target}\$" \
    -fuzztime="$fuzztime" . 2>&1)"
  rc=$?
  ran=$((ran + 1))

  # THE VERDICT IS COMPUTED BY A FUNCTION THAT CAN BE TESTED WITHOUT A FUZZER.
  #
  # It used to be computed inline, which is why this branch printed "not a
  # fuzzing FINDING" and then set status=1 anyway: the classification and the
  # consequence were written in two places and disagreed. There is now one
  # verdict, and --self-test asserts it against recorded outputs.
  verdict="$(classify_fuzz_outcome "$rc" "$fuzztime_secs" "$out")"

  case "$verdict" in
    CRASH)
      echo "FAIL  $module/$target" >&2
      echo "$out" >&2
      echo "" >&2
      echo "  A failing input WAS written under $module/testdata/fuzz/$target/." >&2
      echo "  Commit it as a regression seed, THEN fix the bug." >&2
      status=1
      ;;
    HANG)
      echo "FAIL  $module/$target" >&2
      echo "$out" >&2
      echo "" >&2
      # NO CRASHER FILE, AND STILL A REAL FAILURE. Go reports a dead worker in
      # four forms, one of which says "no crash will be recorded" outright, so
      # "was a corpus file written?" is the wrong question for this shape.
      echo "  The fuzzing PROCESS died -- this is not a deadline and not a" >&2
      echo "  crashing input. No corpus file was written and none is expected." >&2
      echo "  A worker that dies mid-run has usually run out of memory or been" >&2
      echo "  killed; the line above names which form Go saw." >&2
      status=1
      ;;
    COMPLETED_AT_DEADLINE)
      # NOT A FAILURE, and the long comment above classify_fuzz_outcome says why:
      # a missed suppression in Go's coordinator, decided by goroutine scheduling
      # at the deadline instant. Reported on its own line rather than as a plain
      # "ok", because a run that ended this way generated real inputs and the
      # count is worth seeing.
      deadline_count=$((deadline_count + 1))
      echo "ok    $module/$target ($(grep -oE 'execs: [0-9]+' <<<"$out" | tail -1), ended at the FUZZTIME deadline -- Go coordinator race, not a finding)"
      ;;
    OTHER)
      echo "FAIL  $module/$target" >&2
      echo "$out" >&2
      echo "" >&2
      echo "  NO crashing input was recorded, and this is NOT the known deadline" >&2
      echo "  race either -- that one ends at or after FUZZTIME=$fuzztime having" >&2
      echo "  generated inputs, and this did not. The line above is the reason." >&2
      echo "  Do not go looking for a corpus file; there is none." >&2
      status=1
      ;;
    *)
    execs="$(grep -oE 'execs: [0-9]+' <<<"$out" | tail -1)"
    if [ -n "$execs" ]; then
      echo "ok    $module/$target ($execs)"
    else
      # RAN, BUT DID NOT FUZZ. Go prints no "execs:" line when the budget ends
      # before fuzzing begins -- measured on all four daemon targets, each of
      # which sits at "gathering baseline coverage: 0/160 completed" for the
      # whole 30s and generates nothing.
      #
      # THE CAUSE, CORRECTED 2026-09-04. This comment used to say the budget
      # "went on replaying the seed corpus". That was a guess and it was wrong:
      # the on-disk corpora are one file and zero files. The real cause is
      # PACKAGE STARTUP, paid by every fuzz WORKER PROCESS -- daemon's TestMain
      # runs `go build ./testdata/fakehelper` before m.Run(), and
      # `go test -run XXXNOSUCHTEST ./daemon` therefore takes 5.68s with zero
      # tests. Go gathers baseline coverage using workers that each re-exec the
      # test binary, so none of them reaches a single input inside 30s.
      #
      # Still a useful regression replay of the cached corpus, and still NOT a
      # failure -- but reporting it as plain "ok" reads as fuzzing that happened
      # and did not. Recorded for the daemon's owner in
      # docs/DECISION_MEMO_2026-09-04.md, item 4, with the fix (build the fake
      # helper lazily) and the alternative (accept, with a trigger).
      echo "ok    $module/$target (seed corpus only, no new inputs generated at FUZZTIME=$fuzztime)"
    fi
      ;;
  esac
done

# A LIST THAT RAN NOTHING IS NOT A PASS. TARGETS is edited by hand, and an
# editing accident that empties it would otherwise exit 0 in silence.
if [ "$ran" -eq 0 ]; then
  echo "FAIL  no fuzz target ran. TARGETS has ${#TARGETS[@]} entr(ies); none of them" >&2
  echo "      resolved to a target that exists. This is not a clean run." >&2
  exit 1
fi

# A FLOOR ON THE TOLERATED CASE. One target ending at the deadline is the Go
# race; EVERY target ending there is not a race, it is a budget too small for
# this machine to get any work done in, and tolerating it would turn this gate
# into a no-op that reports "ok" for every line.
#
# Guarded on more than one target having run, because with a single target
# "all of them" and "one of them" are the same sentence and mean different
# things.
if [ "$ran" -gt 1 ] && [ "$deadline_count" -eq "$ran" ]; then
  echo "FAIL  every one of $ran target(s) ended at the FUZZTIME=$fuzztime deadline." >&2
  echo "      One is the coordinator race this script tolerates. All of them is a" >&2
  echo "      budget this machine cannot do any work inside, and a gate that" >&2
  echo "      passes every target on that basis is measuring nothing." >&2
  exit 1
fi

if [ "$deadline_count" -gt 0 ]; then
  echo "fuzz: $deadline_count of $ran target(s) ended at the FUZZTIME deadline (Go coordinator race, not findings)"
fi
echo "fuzz: $ran of ${#TARGETS[@]} target(s) ran at FUZZTIME=$fuzztime"
exit $status
