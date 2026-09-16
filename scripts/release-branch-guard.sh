#!/usr/bin/env bash
# NOTHING RELEASES FROM A BRANCH NOBODY AGREED TO RELEASE FROM.
#
# `release.yml` fires on `push: tags: ["v*"]`. A tag is not a branch: GitHub
# hands the workflow `refs/tags/v0.0.2` and no statement whatsoever about where
# that tag sits in the history. Before this guard existed, the `binaries` job
# carried no `if:` at all, so a `v*` tag pushed on ANY ref -- a feature branch, a
# dormant branch, a detached experiment -- ran the full three-platform release
# and, once the signing secrets exist, attached the result to a GitHub Release.
#
# THE ACCIDENT THAT WAS DOING THIS JOB. Measured 2026-09-16 on
# Rav-2007/codeterminal-core: `gh api .../actions/secrets` returns
# `{"total_count":0}`. With no MACOS_CERT_P12 the darwin marker is UNSIGNED, and
# on a tag the signing guard fails the whole `package` job -- so the attach step
# never runs and zero assets are published. That is a missing credential
# standing in for an access-control decision, and it stops standing in the
# moment the five MACOS_* secrets land. This guard must be in place BEFORE they
# are. It is the one ordering error C7 found in the repository.
#
# WHY A SCRIPT AND NOT AN `if:` EXPRESSION. Two reasons, and the first is
# decisive: a workflow `if:` cannot ask a question about ANCESTRY. `github.ref`
# is the tag; there is no `github.branch_the_tag_is_on`, because a tag can be on
# many branches or none. Answering "is this commit on the release line?" needs a
# git repository and `merge-base`. The second reason is the one
# release-signing-guard.sh already records: logic in a script can be
# self-tested, and a guard first exercised during a release is a guard nobody
# has tested.
#
# WHAT THE RELEASE LINE IS, DERIVED AND NOT HARDCODED. The caller passes
# `--release-line`, and release.yml fills it from
# `github.event.repository.default_branch` -- the repository's own declaration
# of its release line, read out of the event payload. No branch name appears in
# this script or in the workflow. If the repository's default branch changes,
# the guard follows it with no edit. Empty is refused rather than defaulted, for
# the reason SIGN_TARGET is refused rather than defaulted: the value decides
# what may ship, so a guess here is the worst possible place to be wrong.
#
# SCOPE, DERIVED FROM THE STEP IT PROTECTS. The guard asserts only for refs
# matching `refs/tags/v*`, because that is exactly the condition on the "Attach
# to the GitHub Release" step (`startsWith(github.ref, 'refs/tags/v')`). A
# workflow_dispatch rehearsal on an arbitrary branch stays green on purpose:
# rehearsing the build is what dispatch is for, it publishes nothing, and
# narrowing it would break the macOS pre-flight this repository relies on.
# Scoping the guard to the attach step's own predicate is what keeps the two
# from drifting apart.
#
# WHAT THIS DOES NOT CHECK, stated here and in the banner: it does not check
# that the tagged code is correct, that CI went green on it, that the version
# number is sane, or that anyone approved the release. It checks one thing --
# whether the tagged commit is on the release line -- and says so.
#
# Usage:
#   release-branch-guard.sh --ref <git-ref> --commit <sha> --release-line <branch>
#   release-branch-guard.sh --self-test

set -euo pipefail

err()  { echo "[release-branch-guard] $*" >&2; }
note() { echo "[release-branch-guard] $*"; }

# resolve_line_ref prints "<object-id> <refname>" for the release line, trying
# the remote-tracking ref first because a CI checkout's only remote is `origin`
# and `origin` there IS the repository the workflow runs in.
#
# IT PRINTS THE REFNAME IT USED, and that is not decoration. The first real-
# repository probe of this guard reported "on the release line 'main'" while
# resolving `refs/remotes/origin/main` on a developer machine whose `origin` is a
# FORK -- a true statement about the wrong repository, and it read as a pass.
# Naming the ref is what made it visible. A guard that says which reference frame
# it used can be checked; one that only says "ok" cannot.
#
# FAILS CLOSED and does not fall back to HEAD. "I could not find the release
# line, so I will assume this is fine" is the fail-open this guard exists to
# remove; an unresolvable release line is a broken guard, and a broken guard
# must block a release rather than wave it through.
resolve_line_ref() {
  local line="$1" c
  for c in "refs/remotes/origin/$line" "refs/heads/$line" "$line"; do
    if git rev-parse --verify --quiet "${c}^{commit}" >/dev/null 2>&1; then
      printf '%s %s' "$(git rev-parse "${c}^{commit}")" "$c"
      return 0
    fi
  done
  return 1
}

run_guard() { # run_guard <ref> <commit> <release-line>
  local ref="$1" commit="$2" line="$3" tip sha resolved

  if [ -z "$line" ]; then
    err "FAIL: no release line given. This is refused rather than defaulted:"
    err "      the value decides what may ship. Pass --release-line, and fill it"
    err "      from github.event.repository.default_branch."
    return 2
  fi
  if [ -z "$commit" ]; then
    err "FAIL: no commit given. Pass --commit, filled from github.sha."
    return 2
  fi
  if [ -z "$ref" ]; then
    err "FAIL: no ref given. Pass --ref, filled from github.ref."
    return 2
  fi

  # SCOPE. Exactly the attach step's condition, and nothing wider.
  case "$ref" in
    refs/tags/v*) ;;
    *)
      note "ok -- '$ref' is not a v* tag, so no release is attached from this run"
      note "      (the attach step's own condition is startsWith(github.ref,"
      note "      'refs/tags/v')). Ancestry NOT checked, deliberately: a"
      note "      workflow_dispatch rehearsal on any branch is what dispatch is for."
      return 0
      ;;
  esac

  if ! sha="$(git rev-parse --verify --quiet "${commit}^{commit}" 2>/dev/null)"; then
    err "FAIL: commit '$commit' does not resolve in this repository. A guard that"
    err "      cannot see the commit it is judging cannot clear it."
    return 2
  fi

  local tipref
  if ! resolved="$(resolve_line_ref "$line")"; then
    err "FAIL: release line '$line' does not resolve (tried"
    err "      refs/remotes/origin/$line, refs/heads/$line, $line)."
    err "      On a CI runner this usually means the checkout was shallow:"
    err "      release.yml uses fetch-depth: 0 for exactly this reason."
    err "      NOT waved through -- an unresolvable release line is a broken"
    err "      guard, and a broken guard blocks the release."
    return 2
  fi
  tip="${resolved%% *}"
  tipref="${resolved#* }"

  if git merge-base --is-ancestor "$sha" "$tip"; then
    note "ok -- ${ref#refs/tags/} ($(git rev-parse --short "$sha")) is on the release line '$line', read from $tipref ($(git rev-parse --short "$tip"))"
    note "NOT checked: whether the tagged code is correct, whether CI went green"
    note "             on it, or whether anyone approved this release. This gate"
    note "             checks which line the tag sits on and nothing else."
    return 0
  fi

  # Say WHICH of the two off-line shapes it is. They need different remedies and
  # reporting them in one sentence is the two-states-one-phrase error this
  # repository keeps finding.
  local shape remedy
  if git merge-base --is-ancestor "$tip" "$sha"; then
    shape="AHEAD OF the release line: '$line' is an ancestor of the tag, so the tag carries work that has not been merged."
    remedy="merge the work into '$line' first, then re-tag on '$line'. The tag is not wrong; its position is."
  else
    shape="DIVERGED from the release line: the tag is neither an ancestor nor a descendant of '$line'."
    remedy="find the branch this tag is on, get it onto '$line', then re-tag."
  fi

  err "FAIL: refusing to release ${ref#refs/tags/}."
  err ""
  err "  tag commit    $(git rev-parse --short "$sha")"
  err "  release line  $line at $(git rev-parse --short "$tip"), read from $tipref"
  err "  relationship  $shape"
  err ""
  err "  Remedy: $remedy"
  err ""
  err "  THIS IS NOT A STATEMENT ABOUT THE CODE. The code may be perfectly"
  err "  shippable; a release just does not come from here. Delete the tag"
  err "  (git push --delete origin ${ref#refs/tags/}) before re-tagging, or the"
  err "  next run repeats this failure."
  return 1
}

# ------------------------------------------------------------------------------
self_test() {
  local tmp pass=0 fail=0 RC=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  command -v git >/dev/null 2>&1 || { err "self-test: no git -- refusing to report a pass."; return 2; }

  check() { # check <desc> <expected:pass|fail> <rc>
    if { [ "$2" = "pass" ] && [ "$3" -eq 0 ]; } || { [ "$2" = "fail" ] && [ "$3" -ne 0 ]; }; then
      printf '  ok    %s\n' "$1"; pass=$((pass + 1))
    else
      printf '  FAIL  %s (expected %s, rc=%s)\n' "$1" "$2" "$3"; fail=$((fail + 1))
    fi
  }
  greps() { if grep -q "$2" "$3" 2>/dev/null; then printf '  ok    %s\n' "$1"; pass=$((pass+1)); else printf '  FAIL  %s (no /%s/ in output)\n' "$1" "$2"; fail=$((fail+1)); fi; }

  echo "release-branch-guard self-test"

  # A real repository with the three shapes that matter: a release line, a
  # branch ahead of it, and a branch diverged from it.
  local r="$tmp/repo"
  mkdir -p "$r"
  (
    cd "$r"
    git init --quiet -b trunk .
    git config user.email t@example.invalid; git config user.name t
    echo a > f; git add f; git commit --quiet -m a           # trunk~1
    echo b >> f; git add f; git commit --quiet -m b          # trunk tip
    git checkout --quiet -b ahead
    echo c >> f; git add f; git commit --quiet -m c          # ahead of trunk
    git checkout --quiet -b diverged trunk~1
    echo d > g; git add g; git commit --quiet -m d           # diverged from trunk
  ) >/dev/null 2>&1

  local ON AHEAD DIVERGED OLD
  ON="$(git -C "$r" rev-parse trunk)"
  OLD="$(git -C "$r" rev-parse trunk~1)"
  AHEAD="$(git -C "$r" rev-parse ahead)"
  DIVERGED="$(git -C "$r" rev-parse diverged)"

  # VACUITY FLOOR. If the fixture does not actually have the three shapes, every
  # assertion below is about nothing and the pass is meaningless. Asserted with
  # git directly rather than through the guard, so the guard cannot certify its
  # own fixture (H1).
  git -C "$r" merge-base --is-ancestor "$OLD" "$ON" || { err "self-test fixture: trunk~1 is not an ancestor of trunk -- refusing to compare nothing."; return 2; }
  git -C "$r" merge-base --is-ancestor "$ON" "$AHEAD" || { err "self-test fixture: 'ahead' is not a descendant of trunk -- refusing to compare nothing."; return 2; }
  if git -C "$r" merge-base --is-ancestor "$DIVERGED" "$ON" || git -C "$r" merge-base --is-ancestor "$ON" "$DIVERGED"; then
    err "self-test fixture: 'diverged' is related to trunk -- refusing to compare nothing."; return 2
  fi

  # g runs the guard inside the fixture and records the status in RC WITHOUT
  # propagating it. `set -e` is on, and an arm that is SUPPOSED to fail would
  # otherwise kill the self-test at the first red -- which is how a self-test
  # reports two passes and exits 1, as this one did on its first run.
  g() { # g <ref> <commit> <line>
    RC=0
    ( cd "$r" && run_guard "$1" "$2" "$3" ) >"$tmp/out" 2>&1 || RC=$?
  }

  # --- the four ancestry answers. These four differ ONLY in ancestry, with the
  # ref, the release line and the repository held identical -- so if 1 and 2
  # disagree, ancestry is what decided it. That differential is why this guard
  # needs no fail-open NEUTER switch to prove the assertion is load-bearing.
  g refs/tags/v1.0.0 "$ON" trunk; check "tag ON the release line -> green" pass "$RC"
  g refs/tags/v1.0.0 "$OLD" trunk; check "tag on an OLDER release-line commit -> green" pass "$RC"
  g refs/tags/v1.0.0 "$AHEAD" trunk; check "tag AHEAD of the release line -> FAILS" fail "$RC"
  greps "  and says AHEAD OF, not just 'off the line'" "AHEAD OF the release line" "$tmp/out"
  greps "  and names the remedy (merge, then re-tag)" "then re-tag on" "$tmp/out"
  greps "  and says it is not a statement about the code" "NOT A STATEMENT ABOUT THE CODE" "$tmp/out"
  g refs/tags/v1.0.0 "$DIVERGED" trunk; check "tag DIVERGED from the release line -> FAILS" fail "$RC"
  greps "  and says DIVERGED, a different remedy" "DIVERGED from the release line" "$tmp/out"

  # --- scope: exactly the attach step's predicate, no wider.
  g refs/heads/ahead "$AHEAD" trunk; check "a branch push (not a v* tag) -> green, ancestry not checked" pass "$RC"
  greps "  and says why it did not check" "not a v\\* tag" "$tmp/out"
  g refs/tags/nightly-1 "$AHEAD" trunk; check "a non-v tag -> green (matches the attach step's startsWith)" pass "$RC"

  # --- fails closed, never defaults.
  g refs/tags/v1.0.0 "$AHEAD" ""; check "missing release line -> FAILS rather than defaulting" fail "$RC"
  greps "  and says it is refused, not defaulted" "refused rather than defaulted" "$tmp/out"
  g refs/tags/v1.0.0 "" trunk; check "missing commit -> FAILS" fail "$RC"
  g "" "$ON" trunk; check "missing ref -> FAILS" fail "$RC"
  g refs/tags/v1.0.0 "$ON" no-such-branch; check "unresolvable release line -> FAILS closed" fail "$RC"
  greps "  and does not wave it through" "NOT waved through" "$tmp/out"
  g refs/tags/v1.0.0 0000000000000000000000000000000000000000 trunk
  check "unresolvable commit -> FAILS closed" fail "$RC"

  # --- the release line named by a remote-tracking ref, which is what CI has.
  git -C "$r" update-ref refs/remotes/origin/trunk "$ON"
  git -C "$r" branch -D trunk >/dev/null 2>&1
  g refs/tags/v1.0.0 "$ON" trunk; check "release line found via refs/remotes/origin/ (the CI shape)" pass "$RC"
  g refs/tags/v1.0.0 "$AHEAD" trunk; check "  and still refuses an off-line tag through that ref" fail "$RC"

  echo
  echo "release-branch-guard: $pass passed, $fail failed"
  echo "NOT COVERED HERE, and not claimed: that GitHub actually supplies"
  echo "  github.event.repository.default_branch on a tag push, and that"
  echo "  fetch-depth: 0 leaves the default branch resolvable on the runner."
  echo "  Both are properties of the runner, not of this script; release.yml"
  echo "  states them at the call site and this guard fails closed if either"
  echo "  is false, which is the whole reason it fails closed."
  [ "$fail" -eq 0 ]
}

# ------------------------------------------------------------------------------
REF=""; COMMIT=""; LINE=""
case "${1:-}" in
  --self-test) self_test; exit $? ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)          REF="$2"; shift 2 ;;
    --commit)       COMMIT="$2"; shift 2 ;;
    --release-line) LINE="$2"; shift 2 ;;
    *) err "unknown argument: $1"; exit 2 ;;
  esac
done
if [ -z "$REF" ]; then
  err "usage: $0 --ref <git-ref> --commit <sha> --release-line <branch>"
  err "   or: $0 --self-test"
  exit 2
fi
run_guard "$REF" "$COMMIT" "$LINE"
exit $?
