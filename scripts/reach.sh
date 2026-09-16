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

# WHICH REMOTE IS CANONICAL, resolved rather than assumed.
#
# Today `upstream` is Rav-2007/codeterminal-core (where CI runs) and `origin` is
# a fork. That is backwards from the convention, and C5b recommends renaming
# them -- at which point a script hardcoding `upstream/main` would silently
# start measuring against nothing, or worse, against the fork.
#
# So the ref is resolved from what actually exists, in preference order, and the
# banner NAMES the one it used. Hardcoding either name would be the enumerated-
# scope defect this repository keeps finding; asking git which refs exist is the
# derivation.
MAIN_REF="${REACH_MAIN_REF:-}"
if [ -z "$MAIN_REF" ]; then
  for candidate in upstream/main origin/main; do
    if git rev-parse --verify --quiet "$candidate" >/dev/null; then
      MAIN_REF="$candidate"
      break
    fi
  done
fi
if [ -z "$MAIN_REF" ]; then
  echo "reach: FAIL -- no canonical main ref found (tried upstream/main, origin/main). Set REACH_MAIN_REF." >&2
  exit 1
fi

failures=0
checked=0
allowed_count=0
allowed_report=""
allowed_seen="|"
covered_count=0
covered_cites=0
covered_branches="|"

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
# Format: key|trigger|reason. BOTH the trigger and the reason are mandatory; an
# entry missing either fails the gate.
#
# THE TRIGGER IS THE HALF THAT KEEPS THIS HONEST. A reason explains why the gap
# is acceptable today. A trigger says what event makes it unacceptable -- and
# without one, an exemption written for a week-long situation silently becomes
# permanent, which is how every stale record in this repository started.
#
# The key is a branch name, `remote:<branch>`, or a commit sha of any length
# `git rev-parse` accepts.
allowlist() {
  cat <<'ALLOW'
ca96966|the merge lands, or the owner reverses the hold|HELD BY OWNER DECISION, 2026-09-15. It reduces main from two failure causes to one but does not green it, so pushing it would break the fast-forward for no gain. The merge is the vehicle instead.
ci/main-lint-pin|the merge lands and ca96966 is confirmed redundant|The branch holding ca96966, and the ONLY ref in this repository with a commit that is not an ancestor of HEAD. Same decision, same reason: held as a fallback, not delivered.
audit/adversarial-pass|the merge lands|IN FLIGHT, and the merge is its delivery. The canonical main is a strict ancestor of this branch, so everything on it arrives in one fast-forward once C7 returns a verdict. This entry is the ONE decision that accounts for the pipeline fixes and document citations on this branch; remove it the moment the merge lands, or this gate stops measuring the thing it exists for.
remote:audit/adversarial-pass|the remote rename in C5b Part 1 (owner action)|Tracks the fork because every branch here does; the topology is backwards repo-wide, not a mistake on this branch. Measured 2026-09-15: nothing in scripts/, .github/ or the Makefile is keyed to the remote NAME, so the rename is safe.
remote:main|the remote rename in C5b Part 1 (owner action)|The most consequential of the eight. Local main tracks the FORK, whose main is 79 commits ahead of the canonical main. A bare `git push` from main delivers to a repository CI does not watch.
remote:ci/cross-go-test|the remote rename, or deletion of this branch|Dormant since 2026-08-08 and 51 behind the canonical main. Tracks the fork like every other branch.
remote:docs/readme-rewrite|the remote rename, or deletion of this branch|Dormant since 2026-08-08 and 48 behind. Tracks the fork like every other branch.
remote:feat/web-grounding|the remote rename, or deletion of this branch|Dormant since 2026-08-29. Fully contained in HEAD, so it holds nothing undelivered.
remote:fix/ci-limiter-probe-and-interrupt-race|the remote rename, or deletion of this branch|Dormant since 2026-09-02. Fully contained in HEAD.
remote:sec/untrusted-text-channels|the remote rename, or deletion of this branch|Dormant since 2026-09-02. Fully contained in HEAD.
remote:security/ultra-vuln-pass-2026-08-06|the remote rename, or deletion of this branch|Dormant since 2026-08-06 and 101 behind. Fully contained in HEAD.
feat/canonical-language-table|deletion of this branch, or repointing it at a remote|MEASURED SAFE, 2026-09-15: 0 commits unreachable from HEAD and 0 unique patches by `git cherry`. A stale pointer into HEAD's own history, not 487 commits of lost work -- which is what the gate's first message made it look like.
feat/edit-payload-ingestion|deletion of this branch, or repointing it at a remote|MEASURED SAFE, 2026-09-15: 0 commits unreachable from HEAD and 0 unique patches. Same stale-pointer shape as its sibling.
ALLOW
}

allow_entry() {
  local key="$1" line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      "${key}|"*) printf '%s' "${line#*|}"; return 0 ;;
    esac
  done < <(allowlist)
  return 1
}

# Kept as a name so covering_branch reads the same way; returns success when the
# key has an entry at all, sound or not.
allow_reason() { allow_entry "$1" >/dev/null; }

# is_allowed prints nothing and returns 0 if key is allowlisted WITH a reason.
# An entry with an empty reason fails the gate rather than silencing it.
is_allowed() {
  local key="$1" entry trigger reason
  entry="$(allow_entry "$key")" || return 1
  trigger="${entry%%|*}"
  reason="${entry#*|}"
  [ "$reason" = "$entry" ] && reason=""
  if [ -z "$trigger" ]; then
    fail "allowlist entry '$key' has no TRIGGER. An exemption with no event that retires it becomes permanent by default, which is how stale records start."
    return 1
  fi
  if [ -z "$reason" ]; then
    fail "allowlist entry '$key' has no reason. An exemption without a reason is a place to hide things; gate-parity.sh refuses these for the same cause."
    return 1
  fi
  # One line per EXEMPTION, not per hit. A sha cited by four documents is one
  # decision, and printing it four times is the 213-failures mistake in
  # miniature.
  case "$allowed_seen" in
    *"|$key|"*) ;;
    *)
      allowed_seen="${allowed_seen}$key|"
      allowed_count=$((allowed_count + 1))
      allowed_report="${allowed_report}
reach: ALLOWED $key -- retires when: $trigger"
      ;;
  esac
  return 0
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
  local ref up track ahead unreachable
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
    # WHICH OF TWO STATES, because "ahead of its upstream" covers both a branch
    # holding work that exists nowhere else and a stale pointer whose every
    # commit is already on HEAD. Those need opposite responses and they were
    # sharing one sentence.
    #
    # CHECK 2 LEARNED THIS AND CHECK 1 DID NOT. Check 2 measures HEAD..ref for
    # exactly this reason -- its first version printed the total on the ref and
    # made two stale pointers read as "487 commit(s)" of lost work. Check 1
    # shipped in the same commit without the same correction.
    #
    # FOUND 2026-09-16 by simulating the C5b remote rename in a throwaway clone:
    # re-pointed at the canonical remote, local `main` became "40 commit(s)
    # ahead", which reads as forty lost commits and is forty commits every one of
    # which is already on HEAD. Instance 5 in this script's own header is that
    # very ref -- so the header named a delivery-gap instance that check 1, as
    # written, described in the words of a different one.
    unreachable="$(git rev-list --count "HEAD..${ref}" 2>/dev/null || echo 0)"
    if [ "$unreachable" -eq 0 ]; then
      fail "branch '$ref' is $ahead commit(s) ahead of '$up', and NOT ONE of them is unreachable from HEAD. A stale pointer, not lost work: those commits arrive wherever HEAD arrives. Delete the branch or move it forward -- nothing is at risk either way, which is why this must not read like forty lost commits."
    else
      fail "branch '$ref' is $ahead commit(s) ahead of '$up', $unreachable of them reachable from nowhere else. The work exists; it has not been delivered."
    fi
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
    # WHAT TO COUNT, and the first version got this wrong in a way that
    # mattered. It printed the TOTAL commits on the ref, so two stale pointers
    # into HEAD's own history were reported as "487 commit(s)" and "486
    # commit(s)" -- which reads as half a year of lost work and is nothing of
    # the kind. What matters is how much is NOT already reachable elsewhere.
    ahead="$(git rev-list --count "${MAIN_REF}..${ref}" 2>/dev/null || echo 0)"
    unreachable="$(git rev-list --count "HEAD..${ref}" 2>/dev/null || echo 0)"
    is_allowed "$ref" && continue
    if is_allowed "$(git rev-parse --short "$ref")"; then continue; fi
    if [ "$unreachable" -eq 0 ]; then
      fail "branch '$ref' has NO upstream, but every one of its commits is already reachable from HEAD ($ahead ahead of $MAIN_REF). A stale pointer, not undelivered work -- but nothing tracks it, so nothing would have told you either way."
    else
      fail "branch '$ref' has NO upstream and holds $unreachable commit(s) reachable from nowhere else ($ahead ahead of $MAIN_REF). Nothing tracks it, so nothing can report it as undelivered."
    fi
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
    # COVERED IS NOT INVISIBLE. Skipping silently here would mean the allowlist
    # suppressed DETECTION rather than the exit status -- and a gate that stops
    # seeing what it excuses is a way to forget. Counted per covering branch and
    # summarised below, because one line per commit is the 213-failures mistake.
    if cover="$(covering_branch "$sha")"; then
      covered_count=$((covered_count + 1))
      case "$covered_branches" in
        *"|$cover|"*) ;;
        *) covered_branches="${covered_branches}$cover|" ;;
      esac
      continue
    fi
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
      if cover="$(covering_branch "$sha")"; then
        covered_cites=$((covered_cites + 1))
        case "$covered_branches" in
          *"|$cover|"*) ;;
          *) covered_branches="${covered_branches}$cover|" ;;
        esac
        continue
      fi
      fail "$doc cites $short, which is not on $MAIN_REF. A reader following that citation reaches nothing."
    done < <(grep -ohE '`[0-9a-f]{7,40}`' "$doc" 2>/dev/null | tr -d '`' | sort -u)
  done
}

check_unpushed
check_no_upstream
check_pipeline_fixes
check_doc_shas

# ALLOWLISTED IS NOT INVISIBLE, and that distinction is the whole design.
#
# An allowlist that silences DETECTION is a way to forget. An allowlist that
# silences only the EXIT STATUS is a record of decisions you still have to read
# past. Every exemption is printed, with the event that retires it, on every run.
if [ "$covered_count" -gt 0 ] || [ "$covered_cites" -gt 0 ]; then
  echo "reach: COVERED $covered_count pipeline commit(s) and $covered_cites document citation(s) are undelivered,"
  echo "reach:         accounted for by the allowlisted branch(es):${covered_branches//|/ }"
  echo "reach:         They are DETECTED, not silenced. When that branch's entry retires, these become failures."
fi

if [ "$allowed_count" -gt 0 ]; then
  printf '%s\n' "${allowed_report# }"
  echo "reach: $allowed_count exemption(s) above are DETECTED and not fatal. Each names the event that retires it."
fi

# AN ENTRY NOTHING CONSULTED IS THE ONE KIND OF EXEMPTION THIS GATE COULD STILL
# HIDE. Everything above prints the exemptions that FIRED. An entry that matches
# nothing prints nothing at all -- so an obsolete one survives forever unread,
# and one written ahead of an event (two were, on 2026-09-16, for the remote
# rename) is invisible until the event happens. Either way the allowlist stops
# being a record you have to read past, which is its entire justification.
#
# NOT a failure. An entry written for a future event is a legitimate recorded
# decision, and so is one whose situation has just resolved. Both want saying out
# loud, and the difference between them is a judgement no gate can make.
unconsulted=""
unconsulted_count=0
while IFS='|' read -r key trigger reason; do
  [ -n "${key:-}" ] || continue
  case "$allowed_seen" in
    *"|$key|"*) ;;
    *)
      unconsulted_count=$((unconsulted_count + 1))
      unconsulted="${unconsulted}
reach:         $key -- retires when: ${trigger:-(none)}"
      ;;
  esac
done < <(allowlist)
if [ "$unconsulted_count" -gt 0 ]; then
  echo
  echo "reach: $unconsulted_count allowlist entry(ies) matched NOTHING on this run. Either the"
  echo "reach:       situation has resolved and the entry should go, or it was written"
  echo "reach:       ahead of an event that has not happened yet:${unconsulted}"
fi

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
