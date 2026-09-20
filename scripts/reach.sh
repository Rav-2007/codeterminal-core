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

# WHICH REMOTE IS CANONICAL, DERIVED FROM THE REPOSITORY IT NAMES.
#
# A LOCAL REMOTE NAME IS NOT A FACT ABOUT THE PROJECT. It is a fact about one
# clone, and this repository has already paid for treating it as the former: for
# months `origin` here was the FORK and `upstream` was canonical, which is
# backwards from the convention every reader assumes. The rename happened
# 2026-09-20 and made `origin` canonical -- in ONE clone. Anyone else's still has
# the old mapping, and nothing stops a future clone having a third.
#
# WHAT THIS BLOCK USED TO DO, and why it was not good enough. It tried
# `upstream/main` then `origin/main` and took the first that resolved. After the
# rename `upstream` stopped existing, so it fell through to `origin/main` and was
# CORRECT -- by the ordering's luck, not its design. Re-add an `upstream` remote
# pointing at the fork and this delivery gate silently begins measuring delivery
# against the fork, which is the exact failure it was written to catch.
#
# THE DERIVATION. The canonical repository's PATH is a project constant; which
# local name points at it is not. So: ask git which remotes exist, find the one
# whose URL names the canonical repository, and use its main. Zero matches or
# more than one is a REFUSAL, not a fallback -- a delivery gate that guesses
# which repository it is measuring against is worse than no delivery gate.
#
# AND THE REF MUST RESOLVE. Measured 2026-09-20 on the version above:
#
#   $ REACH_MAIN_REF=refs/heads/no-such-ref-at-all bash scripts/reach.sh
#   reach: 6 reach failure(s) of 11 item(s) examined, against refs/heads/no-such-ref-at-all
#
# It exited 1, so it was not silent -- but it reported the WORK as unreached
# rather than the REF as absent, and the number of items examined collapsed from
# 297 to 11 with nothing saying so. A bogus ref must be refused before anything
# is measured against it, not diagnosed afterwards from a number nobody is
# watching.
CANONICAL_REPO="Rav-2007/codeterminal-core"

resolve_main_ref() {
  # Honour the override, but hold it to the same standard: it must resolve.
  if [ -n "${REACH_MAIN_REF:-}" ]; then
    if ! git rev-parse --verify --quiet "${REACH_MAIN_REF}^{commit}" >/dev/null; then
      echo "reach: FAIL -- REACH_MAIN_REF=${REACH_MAIN_REF} does not resolve to a commit." >&2
      echo "reach:       Refusing to measure delivery against a ref that is not there." >&2
      return 2
    fi
    printf '%s' "${REACH_MAIN_REF}"
    return 0
  fi

  local matched count name
  matched="$(git remote -v \
    | awk '$3 == "(fetch)" { print $1 "\t" $2 }' \
    | grep -F "$CANONICAL_REPO" \
    | cut -f1 | sort -u)"
  count="$(printf '%s' "$matched" | grep -c . || true)"

  if [ "$count" -eq 0 ]; then
    echo "reach: FAIL -- no remote points at $CANONICAL_REPO." >&2
    echo "reach:       Remotes here:" >&2
    git remote -v | sed 's/^/reach:         /' >&2
    echo "reach:       This gate measures delivery TO that repository; with no remote for" >&2
    echo "reach:       it there is nothing to measure against. Add one, or set REACH_MAIN_REF." >&2
    return 2
  fi
  if [ "$count" -gt 1 ]; then
    echo "reach: FAIL -- $count remotes point at $CANONICAL_REPO:" >&2
    printf '%s\n' "$matched" | sed 's/^/reach:         /' >&2
    echo "reach:       Which one is authoritative is a judgement this script will not make." >&2
    echo "reach:       Remove the duplicate, or set REACH_MAIN_REF." >&2
    return 2
  fi

  name="$matched"
  if ! git rev-parse --verify --quiet "$name/main^{commit}" >/dev/null; then
    echo "reach: FAIL -- remote '$name' points at $CANONICAL_REPO but $name/main does not resolve." >&2
    echo "reach:       Run 'git fetch $name' and try again." >&2
    return 2
  fi
  printf '%s' "$name/main"
}

if [ "${1:-}" = "--self-test" ]; then
  # Five arms. Each NEUTERS the derivation in one specific way and asserts the
  # refusal, because a gate not demonstrated failing is not demonstrated.
  pass=0; fail=0
  check() { # name expected_rc actual_rc
    if [ "$2" = "$3" ]; then pass=$((pass+1)); else
      fail=$((fail+1)); echo "reach-self-test: FAIL $1 (want rc=$2, got rc=$3)" >&2
    fi
  }
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  # THE SCRIPT UNDER TEST IS THE ONE RUNNING, NOT THE ONE AT ITS USUAL PATH.
  #
  # The first draft of this harness copied "$repo_root/scripts/reach.sh" into
  # each fixture -- the real file, always, whatever file was actually driving
  # the self-test. So a deliberately NEUTERED copy, run to prove these arms can
  # fail, passed all six: every fixture was quietly exercising the good code.
  # Measured 2026-09-20, and it is the "a gate not demonstrated failing is not
  # demonstrated" rule catching the demonstration itself.
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

  # A copy of THIS script inside the fixture, because the script resolves its
  # own repository root from its own path: running the real one from elsewhere
  # would cd straight back into the real repository and test nothing. The first
  # draft of this self-test did exactly that, and three arms passed vacuously.
  mk() { # mk <dir> -- a repo with one commit and a copy of this gate in it
    git init -q "$1"
    git -C "$1" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
    mkdir -p "$1/scripts"
    cp "$self" "$1/scripts/reach.sh"
  }

  # 1. No remote at all names the canonical repository.
  mk "$tmp/none"
  rc=0; ( unset REACH_MAIN_REF; bash "$tmp/none/scripts/reach.sh" ) >/dev/null 2>&1 || rc=$?
  check "no remote for the canonical repository refuses" 2 "$rc"

  # 2. TWO remotes name it. Ambiguous, and it must not pick one.
  mk "$tmp/two"
  git -C "$tmp/two" remote add a "https://github.com/$CANONICAL_REPO.git"
  git -C "$tmp/two" remote add b "git@github.com:$CANONICAL_REPO.git"
  rc=0; ( unset REACH_MAIN_REF; bash "$tmp/two/scripts/reach.sh" ) >/dev/null 2>&1 || rc=$?
  check "two remotes for the canonical repository refuses" 2 "$rc"

  # 3. A remote names it, but nothing has been fetched, so <name>/main is absent.
  #    Also the arm that proves the derivation does not care what the remote is
  #    CALLED: this one is called `somename`.
  mk "$tmp/unfetched"
  git -C "$tmp/unfetched" remote add somename "https://github.com/$CANONICAL_REPO.git"
  rc=0; ( unset REACH_MAIN_REF; bash "$tmp/unfetched/scripts/reach.sh" ) >/dev/null 2>&1 || rc=$?
  check "a remote with no fetched main refuses" 2 "$rc"

  # 4. The override must be held to the same standard as the derivation. This is
  #    the arm that the pre-2026-09-20 version FAILED: it took the override on
  #    trust and measured 11 items against a ref that was not there.
  rc=0; ( REACH_MAIN_REF=refs/heads/no-such-ref-at-all bash "$self" ) >/dev/null 2>&1 || rc=$?
  check "a nonexistent REACH_MAIN_REF refuses before measuring" 2 "$rc"

  # 6. THE DECISIVE ARM, and the only one the previous version would have failed
  #    silently rather than loudly. A clone where `origin` is the FORK and some
  #    other name is canonical -- the exact topology this repository had for
  #    months. The old code took the first of `upstream/main`, `origin/main` that
  #    resolved, so it picked the FORK and measured delivery against it, exit 0,
  #    banner naming a repository nobody ships to. The derivation must pick the
  #    canonical one by the repository it names, whatever it is called locally.
  #
  #    The fixture paths literally contain the two repository paths, because
  #    matching is on the URL and a local directory is a URL git accepts.
  mkdir -p "$tmp/remotes/$CANONICAL_REPO" "$tmp/remotes/Rav-i24"
  canon_url="$tmp/remotes/$CANONICAL_REPO.git"
  fork_url="$tmp/remotes/Rav-i24/Mochiii.git"
  for u in "$canon_url" "$fork_url"; do
    git init -q --bare "$u"
    seed="$tmp/seed-$(basename "$u")"
    git init -q "$seed"
    git -C "$seed" -c user.email=t@t -c user.name=t commit -q --allow-empty -m seed
    git -C "$seed" branch -M main
    git -C "$seed" push -q "$u" main
  done
  mk "$tmp/inverted"
  git -C "$tmp/inverted" remote add origin "$fork_url"
  git -C "$tmp/inverted" remote add elsewhere "$canon_url"
  git -C "$tmp/inverted" fetch -q --all
  out="$( { unset REACH_MAIN_REF; bash "$tmp/inverted/scripts/reach.sh"; } 2>&1 || true )"
  case "$out" in
    *"elsewhere/main"*) pass=$((pass+1)) ;;
    *)
      fail=$((fail+1))
      echo "reach-self-test: FAIL the derivation prefers the canonical remote over one named origin" >&2
      echo "reach-self-test:      it resolved: $(printf '%s' "$out" | grep -o 'against [^,]*' | head -1)" >&2
      ;;
  esac

  # 5. THE POSITIVE ARM, and it is not decoration: the derivation must find the
  #    canonical remote when it is NOT called origin, which is the whole point.
  want="$(git remote -v \
    | awk '$3 == "(fetch)" { print $1 "\t" $2 }' \
    | grep -F "$CANONICAL_REPO" | cut -f1 | sort -u)"
  out="$( { unset REACH_MAIN_REF; bash "$self"; } 2>&1 || true )"
  case "$out" in
    *"against $want/main"*) pass=$((pass+1)) ;;
    *)
      fail=$((fail+1))
      echo "reach-self-test: FAIL this clone's canonical remote is '$want' but the gate" >&2
      echo "reach-self-test:      reported: $(printf '%s' "$out" | grep -o 'against [^,]*' | head -1)" >&2
      ;;
  esac

  echo "reach-self-test: $pass passed, $fail failed"
  [ "$fail" -eq 0 ] || exit 1
  exit 0
fi

if ! MAIN_REF="$(resolve_main_ref)"; then
  exit 2
fi

failures=0
checked=0
allowed_count=0
allowed_report=""
allowed_seen="|"
covered_count=0
covered_cites=0
covered_branches="|"
arrived_count=0
arrived_report=""

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
# AND A TRIGGER MUST NAME A REF OPERATION, NEVER AN OWNER INTENTION. Audited
# 2026-09-17, after the merge landed and two entries kept firing: ten of fifteen
# triggers named something this script cannot observe. "The remote rename" was
# the worst, because it looks observable and is not -- MEASURED in a throwaway
# repository the same day, `git remote rename` REWRITES branch.<name>.remote to
# follow the remote object, so a branch tracking the fork still tracks the fork
# afterwards and `git branch -u` is what actually retires the entry. "ca96966 is
# confirmed redundant" and "the owner reverses the hold" are judgements with no
# ref to look at. The five well-authored triggers all named an operation on a
# ref: merged, deleted, re-pointed, pushed. That is the distinction.
#
# The key is a branch name, `remote:<branch>`, or a commit sha of any length
# `git rev-parse` accepts.
allowlist() {
  cat <<'ALLOW'
ca96966|this commit becoming reachable from the canonical main, or deletion of the branch that holds it|HELD BY OWNER DECISION, 2026-09-15. It reduces main from two failure causes to one but does not green it, so pushing it would break the fast-forward for no gain. The merge is the vehicle instead.
ci/main-lint-pin|this branch's commits becoming reachable from the canonical main, or deletion of this branch|The branch holding ca96966, and the ONLY ref in this repository with a commit that is not an ancestor of HEAD. Same decision, same reason: held as a fallback, not delivered.
audit/adversarial-pass|this branch's commits becoming reachable from the canonical main, or deletion of this branch|RE-TRIGGERED 2026-09-20, AND THE ENTRY DID IT AGAIN. Its own reason below records being split out of an entry that was covering two facts under one key; its TRIGGER then named the wrong one of the two. This key is check 1 -- commits not reachable from the canonical main -- but the trigger it carried named the wrong-remote fact, which `remote:audit/adversarial-pass` covered and which the rename retired. So on 2026-09-20 the trigger fired while the situation it was written for persisted: the branch is now pushed to canonical and tracking it, and its commits are still not in canonical main. The trigger now names the merge, which is the event that actually retires this. Original text follows. MEASURED 2026-09-17, AFTER THE MERGE: 77 commit(s) ahead of the FORK's copy with 0 of them unreachable from HEAD, and 2 ahead of the CANONICAL copy -- the macOS baseline fix awaiting a verification run. A stale pointer at the fork, not lost work. This entry replaces one keyed to "the merge lands", and removing that one is how this was found: it was covering TWO facts under one key -- the DELIVERY fact, which the merge retired, and the WRONG-REMOTE fact, which the merge does not touch. They were sharing a phrase, which this script's own header forbids.
remote:fix/ci-limiter-probe-and-interrupt-race|pushing this branch to the canonical remote, or deletion of this branch|Dormant since 2026-09-02. Fully contained in HEAD. AFTER THE 2026-09-20 RENAME this is one of only two branches still tracking the fork, and NOT by oversight: the canonical remote does not hold a branch of this name, so `git branch -u origin/...` has nothing to point at. The trigger was re-worded on that date for exactly that reason -- `git branch -u` had become an event that could never happen, which is the permanent-by-default shape this file's header forbids.
remote:sec/untrusted-text-channels|pushing this branch to the canonical remote, or deletion of this branch|Dormant since 2026-09-02. Fully contained in HEAD. Same as its sibling above: the canonical remote holds no branch of this name, so re-pointing is not an available operation and the trigger was re-worded on 2026-09-20.
feat/canonical-language-table|deletion of this branch, or repointing it at a remote|MEASURED SAFE, 2026-09-15: 0 commits unreachable from HEAD and 0 unique patches by `git cherry`. A stale pointer into HEAD's own history, not 487 commits of lost work -- which is what the gate's first message made it look like.
feat/edit-payload-ingestion|deletion of this branch, or repointing it at a remote|MEASURED SAFE, 2026-09-15: 0 commits unreachable from HEAD and 0 unique patches. Same stale-pointer shape as its sibling.
docs/readme-rewrite|deletion of this branch, or pushing it to the canonical remote|PRE-PLACED, same clone, same measurement: 1 commit ahead of the canonical branch and 0 unreachable from HEAD. The canonical remote holds this branch at aa0e755 and the fork holds it at 9c9ec50 -- one commit that exists on the fork and on HEAD but not on the canonical remote. NOT retired by the merge: check 1 compares against the BRANCH's own upstream, which the merge does not touch. remote:docs/readme-rewrite above covers the wrong-remote fact; this covers the ahead-of-canonical fact underneath it, which only becomes measurable once the branch is re-pointed.
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
  local ref up track ahead unreachable ci_copy ci_ahead
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
    # WHERE THE PIPELINE WATCHES, and not only where the branch is configured to
    # push. This is the same reference-frame defect as check 1's stale-pointer
    # blindness, one layer further in, and it was found the only way it could be:
    # by pushing.
    #
    # MEASURED 2026-09-16. This branch was pushed to the canonical remote --
    # upstream/audit/adversarial-pass and HEAD both at aa81555 -- and this gate
    # went on reporting it as in flight, plus 57 pipeline commits and 178
    # document citations as undelivered. Every one of those had arrived. The
    # branch tracks the FORK, `ahead` was measured against the fork's copy, and
    # the question "has this work reached the pipeline?" was being answered about
    # a repository the pipeline does not read.
    #
    # A gate for the delivery gap that cannot see a delivery is worse than no
    # gate: it teaches you to ignore it. So `ahead` is now asked of the canonical
    # remote's copy of the same branch whenever one exists.
    #
    # THE WRONG-REMOTE FACT IS NOT SWALLOWED BY THIS. The `remote:<branch>` check
    # above still fires, because "your tracking ref points at a fork" and "your
    # work has not arrived" are two facts and this script's own comment says they
    # must not share a phrase. What changes is that arrival is no longer reported
    # as its absence.
    ci_copy=""; ci_ahead=""
    if git rev-parse --verify --quiet "refs/remotes/${ci_remote}/${ref}" >/dev/null 2>&1; then
      ci_copy="${ci_remote}/${ref}"
      ci_ahead="$(git rev-list --count "${ci_copy}..${ref}" 2>/dev/null || echo 0)"
    fi

    [ "$ahead" -gt 0 ] || continue

    if [ -n "$ci_copy" ] && [ "$ci_ahead" = "0" ]; then
      arrived_count=$((arrived_count + 1))
      arrived_report="${arrived_report}
reach:         $ref -- $ahead ahead of '$up', 0 ahead of '$ci_copy'"
      continue
    fi

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

# MARK THE COVERING BRANCHES CONSULTED, AND DO IT HERE RATHER THAN IN
# covering_branch. That function runs inside a `cover="$(...)"` command
# substitution, so anything it marks is marked in a SUBSHELL and discarded --
# which is how the first version of the unconsulted-entry report said
# `audit/adversarial-pass` matched nothing, two lines below a line saying that
# same entry accounted for 57 commits and 178 citations. One run contradicting
# itself, from a subshell, which is the second time a subshell or a shadowed
# variable has produced a wrong row in this pass.
for cb in ${covered_branches//|/ }; do
  [ -n "$cb" ] || continue
  is_allowed "$cb" >/dev/null 2>&1 || true
done

# ALLOWLISTED IS NOT INVISIBLE, and that distinction is the whole design.
#
# An allowlist that silences DETECTION is a way to forget. An allowlist that
# silences only the EXIT STATUS is a record of decisions you still have to read
# past. Every exemption is printed, with the event that retires it, on every run.
if [ "$covered_count" -gt 0 ] || [ "$covered_cites" -gt 0 ]; then
  echo "reach: COVERED $covered_count pipeline commit(s) and $covered_cites document citation(s) have not reached $MAIN_REF,"
  echo "reach:         accounted for by the allowlisted branch(es):${covered_branches//|/ }"
  echo "reach:         They are DETECTED, not silenced. When that branch's entry retires, these become failures."
fi

if [ "$arrived_count" -gt 0 ]; then
  echo
  echo "reach: $arrived_count branch(es) have ARRIVED where the pipeline watches, while their"
  echo "reach:       tracking ref still says otherwise. NOT a delivery failure; the"
  echo "reach:       wrong-remote fact is reported separately:${arrived_report}"
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
