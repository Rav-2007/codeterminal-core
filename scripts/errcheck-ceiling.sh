#!/usr/bin/env bash
#
# errcheck-ceiling.sh — bound unchecked errors per module without demanding a
#                       170-site triage first.
#
# WHY THIS EXISTS RATHER THAN `errcheck` AS A HARD GATE
#
# Debt item (j) posed errcheck as adopt-or-defer, and deferred it for a good
# reason: turning it green means ~170 `_ =` assignments (noise that makes a real
# unchecked error HARDER to spot) or an exclusion list long enough to be
# arbitrary. That reasoning still holds.
#
# But the deferral had no mechanism, and the count DRIFTED: the entry recorded
# 329 total / 157 non-test, and the same measurement two weeks later reads
# 365 / 170. Thirty-six new unchecked errors arrived precisely because nothing
# was watching. That is the failure mode this repo already solved once, for
# coverage, with a ratchet -- so this is the same shape:
#
#   the existing 170 are grandfathered; NEW ones fail the build.
#
# A module whose count DROPS is reported, so ceilings can be lowered as triage
# happens. Same discipline as scripts/coverage-ratchet.sh, and deliberately the
# same vocabulary, so a reader who knows one knows this.
#
# NOT counting tests (-ignoretests): an unchecked error in a test is a test that
# might silently not test anything, which matters, but it is a different problem
# from an unchecked error on a production path and mixing them would let one
# hide behind the other's budget.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ceilings="$repo_root/scripts/errcheck-ceilings.txt"

if ! command -v errcheck >/dev/null 2>&1; then
  echo "errcheck-ceiling: errcheck not found on PATH"
  echo "  go install github.com/kisielk/errcheck@latest"
  exit 1
fi

if [ ! -f "$ceilings" ]; then
  echo "errcheck-ceiling: missing $ceilings" >&2
  exit 1
fi

modules="${*:-daemon editapply proxy protocol helper clients/tui}"

status=0
for m in $modules; do
  ceiling="$(awk -v m="$m" '$1 == m { print $2 }' "$ceilings")"
  if [ -z "$ceiling" ]; then
    # Fail closed, exactly like the coverage ratchet: a module with no recorded
    # ceiling must not silently escape the gate.
    echo "FAIL  $m — no ceiling recorded in $(basename "$ceilings")"
    status=1
    continue
  fi

  # THE GATE USED TO FAIL OPEN, and it was found by a neuter check that did not
  # compile. errcheck exits 2 and prints its complaint to STDERR when it cannot
  # load a package -- a syntax error, a missing dependency, a broken build tag.
  # With stderr discarded that produced zero lines, the count read as 0, and a
  # module that would not even build was reported "at ceiling". A gate that
  # reports success when it could not run is worse than no gate: it is a gate
  # everyone believes in.
  #
  # errcheck exits 1 when it finds unchecked errors (the normal case here) and
  # 2 when it could not run. Only 0 and 1 are results.
  errout="$( (cd "$repo_root/$m" && errcheck -ignoretests ./... 2>&1) )"
  rc=$?
  if [ "$rc" -ne 0 ] && [ "$rc" -ne 1 ]; then
    echo "FAIL  $m — errcheck could not run (exit $rc). This is not a score of zero."
    echo "$errout" | sed 's/^/      /' >&2
    status=1
    continue
  fi
  count="$(printf '%s' "$errout" | grep -c . || true)"
  count="${count//[[:space:]]/}"

  if [ "$count" -gt "$ceiling" ]; then
    echo "FAIL  $m — $count unchecked errors, ceiling $ceiling (+$((count - ceiling)))"
    echo "      new unchecked errors were added. Handle them, or \`_ =\` with a"
    echo "      reason at the call site. Raising the ceiling is a decision, not a fix."
    status=1
  elif [ "$count" -lt "$ceiling" ]; then
    echo "ok    $m — $count (ceiling $ceiling, -$((ceiling - count))) — LOWER the ceiling"
    status=1
  else
    echo "ok    $m — $count (at ceiling)"
  fi
done

if [ "$status" -ne 0 ]; then
  echo
  echo "errcheck-ceiling: FAILED — see scripts/errcheck-ceilings.txt"
fi
exit "$status"
