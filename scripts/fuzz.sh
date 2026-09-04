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

status=0
ran=0
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

  if [ $rc -ne 0 ]; then
    echo "FAIL  $module/$target" >&2
    echo "$out" >&2
    echo "" >&2
    # A NON-ZERO EXIT IS NOT THE SAME AS A CRASHING INPUT, and this said it was.
    #
    # It printed "a failing input has been written ... commit it as a regression
    # seed, THEN fix the bug" on EVERY failure, unconditionally. Run 31204152210
    # failed here with `context deadline exceeded` -- Go's fuzzing coordinator
    # timing out on a loaded two-worker runner, against a target that is a pure
    # function taking no context at all. No input was written, because none
    # crashed. The message sent its reader looking for a file that does not exist
    # and a bug that is not there.
    #
    # Go announces a real crasher in a fixed form, so that is what gets matched
    # rather than the exit code. When it is absent, say what actually happened
    # and name the likeliest cause instead of inventing a finding.
    if grep -q "Failing input written to" <<<"$out"; then
      echo "  A failing input WAS written under $module/testdata/fuzz/$target/." >&2
      echo "  Commit it as a regression seed, THEN fix the bug." >&2
    else
      echo "  NO crashing input was recorded -- this is not a fuzzing FINDING." >&2
      echo "  The fuzz command failed for another reason (the line above is it)." >&2
      echo "  'context deadline exceeded' at ~\$FUZZTIME is the coordinator timing" >&2
      echo "  out under load, and is expected to be intermittent on a busy runner." >&2
      echo "  Re-run before investigating; do not go looking for a corpus file." >&2
    fi
    status=1
  else
    execs="$(grep -oE 'execs: [0-9]+' <<<"$out" | tail -1)"
    if [ -n "$execs" ]; then
      echo "ok    $module/$target ($execs)"
    else
      # RAN, BUT DID NOT FUZZ. Go prints no "execs:" line when the whole budget
      # went on replaying the seed corpus -- measured on daemon/FuzzRenderToolResult,
      # which spends 40s "gathering baseline coverage: 0/197" at FUZZTIME=3s and
      # never generates an input of its own. That is still a useful regression
      # replay and it is NOT a failure, but reporting it as plain "ok" reads as
      # fuzzing that happened and did not.
      echo "ok    $module/$target (seed corpus only, no new inputs generated at FUZZTIME=$fuzztime)"
    fi
  fi
done

# A LIST THAT RAN NOTHING IS NOT A PASS. TARGETS is edited by hand, and an
# editing accident that empties it would otherwise exit 0 in silence.
if [ "$ran" -eq 0 ]; then
  echo "FAIL  no fuzz target ran. TARGETS has ${#TARGETS[@]} entr(ies); none of them" >&2
  echo "      resolved to a target that exists. This is not a clean run." >&2
  exit 1
fi

echo "fuzz: $ran of ${#TARGETS[@]} target(s) ran at FUZZTIME=$fuzztime"
exit $status
