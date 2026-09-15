#!/usr/bin/env bash
# THE GATE ON REACH, NOT ON CORRECTNESS.
#
# Every other gate in this repository asks "is this work right?". All of them
# pass on a branch nobody has pushed. This one asks the question none of them
# ask: HAS THE WORK ARRIVED WHERE IT BELONGS?
#
# It exists because the answer was no, six times, and each instance was found by
# accident:
#
#   1. a memo committed with nobody told
#   2. a checkpoint written and never committed
#   3. `142e57d`, the lint pin, fixed on a branch and never merged -- while main
#      stayed red for weeks with the fix sitting one merge away
#   4. `ca96966`, cherry-picked, verified, and never pushed
#   5. 40 commits on a stale local ref
#   6. the eval fix, green on the branch across three runs, never delivered
#
# And a seventh found on 2026-09-15 while auditing something else: `0925a3d`
# repairs the failure that killed the ONLY release run this repository has ever
# had, and is not on main, so main's release path is still broken for win32-x64.
#
# THE SHAPE IS ALWAYS THE SAME. The artifact exists. The delivery does not. An
# audit of the WORK passes every time, because the work is fine.
#
# WHAT THIS DOES NOT CHECK, stated here and in the closing banner, because a
# gate whose scope is invisible is how docs-coderefs checked one file extension
# for its entire life: it does not check whether pushed work is CORRECT, whether
# a run went green, whether a deploy happened, or whether a human was told. It
# checks reach and nothing else.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

MAIN_REF="${REACH_MAIN_REF:-upstream/main}"

failures=0
checked=0

fail() {
  echo "reach: FAIL $1" >&2
  failures=$((failures + 1))
}

# --- the allowlist: expected state, with a MANDATORY reason --------------------
#
# This gate is noisy on a working branch by design -- that is the point of it.
# The allowlist is how a KNOWN, DELIBERATE gap stays quiet, and the reason field
# is what stops it becoming a place to hide things. A blank reason is a failure,
# exactly as gate-parity.sh treats a blank reason for an asymmetry.
#
# Format: key|reason. The key is a branch name or a commit sha (any length that
# `git rev-parse` accepts).
allowlist() {
  cat <<'ALLOW'
ca96966|HELD BY OWNER DECISION, 2026-09-15. It reduces main from two failure causes to one but does not green it, so pushing it would break the fast-forward for no gain. The merge is the vehicle instead. Re-examine when the merge lands or when the decision changes.
ci/main-lint-pin|The branch holding ca96966. Same decision, same reason: held as a fallback, not delivered, by owner decision on 2026-09-15.
audit/adversarial-pass|IN FLIGHT, and the merge is its delivery. upstream/main is a strict ancestor of this branch, so everything on it arrives in one fast-forward once C7 returns a verdict -- C7 is the review boundary. This entry is the ONE decision that accounts for the pipeline fixes and document citations on this branch; remove it the moment the merge lands, or this gate stops measuring the thing it exists for.
ALLOW
}

allow_reason() {
  local key="$1" line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      "${key}|"*) printf '%s' "${line#*|}"; return 0 ;;
    esac
  done < <(allowlist)
  return 1
}

# is_allowed prints nothing and returns 0 if key is allowlisted WITH a reason.
# An entry with an empty reason fails the gate rather than silencing it.
is_allowed() {
  local key="$1" reason
  if reason="$(allow_reason "$key")"; then
    if [ -z "$reason" ]; then
      fail "allowlist entry '$key' has no reason. An exemption without a reason is a place to hide things; gate-parity.sh refuses these for the same cause."
      return 1
    fi
    return 0
  fi
  return 1
}

# covering_branch prints the name of an ALLOWLISTED local branch that contains
# the given commit, if there is one.
#
# WHY THIS EXISTS, because it is the difference between a gate and a wall. The
# first run of this script produced 213 failures: every commit touching .github
# or scripts on an undelivered branch was reported separately. That is 213
# restatements of ONE fact -- the branch has not been delivered -- and an
# allowlist with 213 entries is not a record of decisions, it is a way to stop
# reading.
#
# A commit's reach failure is SUBSUMED by its branch's. So a branch entry, with
# its one reason, accounts for the commits on it; anything the branch does not
# explain is still reported on its own. One decision, one reason, and a commit
# that is undelivered for some OTHER reason still surfaces.
covering_branch() {
  local sha="$1" ref
  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    allow_reason "$ref" >/dev/null 2>&1 || continue
    if git merge-base --is-ancestor "$sha" "$ref" 2>/dev/null; then
      printf '%s' "$ref"
      return 0
    fi
  done < <(git for-each-ref --format='%(refname:short)' refs/heads)
  return 1
}

# --- 1. unpushed commits ------------------------------------------------------
# A local branch ahead of its upstream. Instance 5, and instance 6's shape.
check_unpushed() {
  local ref up track ahead
  while IFS='|' read -r ref up track; do
    [ -n "$ref" ] || continue
    [ -n "$up" ] || continue # no upstream at all is check 2's business
    checked=$((checked + 1))
    ahead="$(git rev-list --count "${up}..${ref}" 2>/dev/null || echo 0)"
    # WHERE IT TRACKS IS PART OF WHETHER IT HAS ARRIVED. `gh` in this repository
    # resolves to a FORK, and a branch tracking the fork is "pushed" to a place
    # CI does not run. Reported whether or not it is also ahead, because a branch
    # fully pushed to the wrong remote is the purest form of the thing this gate
    # is for: delivered, and not arrived.
    #
    # KEYED SEPARATELY (`remote:<branch>`) AND NOT BY THE BRANCH NAME, on
    # purpose. "This branch is in flight" and "this branch points at the wrong
    # remote" are two different facts about one branch, and letting one
    # exemption silence both is the M5 error this repository keeps finding --
    # two states sharing a phrase. A branch can be deliberately in flight AND
    # accidentally aimed at a fork; the second must still be said.
    ci_remote="${MAIN_REF%%/*}"
    if [ "${up%%/*}" != "$ci_remote" ]; then
      is_allowed "remote:$ref" || fail "branch '$ref' tracks '${up}', but $MAIN_REF lives on remote '$ci_remote'. A bare 'git push' delivers it to a remote the pipeline does not watch."
    fi
    [ "$ahead" -gt 0 ] || continue
    is_allowed "$ref" && continue
    fail "branch '$ref' is $ahead commit(s) ahead of '$up'. The work exists; it has not been delivered."
  done < <(git for-each-ref --format='%(refname:short)|%(upstream:short)|%(upstream:track)' refs/heads)
}

# --- 2. branches with no upstream at all --------------------------------------
# The ca96966 shape: a branch that was never pointed anywhere, so "ahead of
# upstream" cannot even be computed and check 1 is structurally blind to it.
# This is the (b) direction -- a gate that can only see what it can measure is
# the defect, not the gate.
check_no_upstream() {
  local ref up n
  while IFS='|' read -r ref up; do
    [ -n "$ref" ] || continue
    [ -n "$up" ] && continue
    checked=$((checked + 1))
    n="$(git rev-list --count "$ref" 2>/dev/null || echo 0)"
    is_allowed "$ref" && continue
    if is_allowed "$(git rev-parse --short "$ref")"; then continue; fi
    fail "branch '$ref' ($n commit(s)) has NO upstream. Nothing tracks it, so nothing can report it as undelivered."
  done < <(git for-each-ref --format='%(refname:short)|%(upstream:short)' refs/heads)
}

# --- 3. workflow and script fixes that never reached main ---------------------
# Instance 3 (142e57d) and instance 7 (0925a3d) exactly. Restricted to .github
# and scripts because that is where a fix repairs the PIPELINE -- a fix that is
# not on main is a pipeline still broken for everyone.
#
# NOT `git cherry ... -- <pathspec>`, and the difference was measured on
# 2026-09-15: git cherry reported 43 where rev-list and --cherry-pick
# --right-only both reported 50, and the two disagree about which commits count.
# --cherry-pick --right-only computes patch-id equivalence restricted to the
# pathspec, which is the question being asked.
check_pipeline_fixes() {
  local sha subject short
  git rev-parse --verify --quiet "$MAIN_REF" >/dev/null || {
    echo "reach: SKIP checks 3 and 4 -- '$MAIN_REF' does not resolve. Set REACH_MAIN_REF or fetch it." >&2
    return 0
  }
  while read -r sha subject; do
    [ -n "$sha" ] || continue
    checked=$((checked + 1))
    short="$(git rev-parse --short "$sha")"
    is_allowed "$short" && continue
    covering_branch "$sha" >/dev/null && continue
    fail "$short touches .github/ or scripts/ and is not on $MAIN_REF -- the pipeline fix has not reached the pipeline: $subject"
  done < <(git log --no-merges --cherry-pick --right-only --format='%H %s' "${MAIN_REF}...HEAD" -- .github scripts)
}

# --- 4. enforced documents citing commits absent from main --------------------
# A document that cites a sha nobody else can resolve is a claim about work the
# reader cannot reach. Restricted to ENFORCED documents, the same set
# docs-coderefs governs, so the two gates agree about what is load-bearing.
check_doc_shas() {
  local doc sha short
  git rev-parse --verify --quiet "$MAIN_REF" >/dev/null || return 0
  shopt -s nullglob
  for doc in docs/*.md *.md; do
    [ -f "$doc" ] || continue
    grep -q '<!-- coderefs: enforced -->' "$doc" || continue
    while IFS= read -r sha; do
      [ -n "$sha" ] || continue
      # A hex-looking token is not a commit. `1734900` is a decimal that
      # matches a 7-hex pattern, and this gate learned that the hard way.
      git cat-file -e "${sha}^{commit}" 2>/dev/null || continue
      checked=$((checked + 1))
      short="$(git rev-parse --short "$sha")"
      git merge-base --is-ancestor "$sha" "$MAIN_REF" 2>/dev/null && continue
      is_allowed "$short" && continue
      covering_branch "$sha" >/dev/null && continue
      fail "$doc cites $short, which is not on $MAIN_REF. A reader following that citation reaches nothing."
    done < <(grep -ohE '`[0-9a-f]{7,40}`' "$doc" 2>/dev/null | tr -d '`' | sort -u)
  done
}

check_unpushed
check_no_upstream
check_pipeline_fixes
check_doc_shas

echo
echo "reach: NOT checked -- whether delivered work is CORRECT, whether any run went green,"
echo "reach:               whether a deploy happened, or whether a human was told."
echo "reach:               This gate measures arrival and nothing else."

if [ "$checked" -eq 0 ]; then
  echo "reach: FAIL -- nothing was examined at all; the gate is not measuring anything" >&2
  exit 1
fi

if [ "$failures" -gt 0 ]; then
  echo "reach: $failures reach failure(s) of $checked item(s) examined, against $MAIN_REF" >&2
  exit 1
fi

echo "reach: ok -- $checked item(s) examined against $MAIN_REF, all delivered or allowlisted with a reason"
