#!/usr/bin/env bash
# Every commit a pull request adds must carry its author's Signed-off-by line.
#
# WHY. CONTRIBUTING.md asks each contributor to certify the Developer
# Certificate of Origin 1.1 by signing off each commit (`git commit -s`). The
# sign-off is the record that the certification was made. A rule that nothing
# checks is a rule that holds only while everyone remembers it.
#
# ADVISORY, NOT REQUIRED (added 2026-10-25). .github/workflows/dco.yml runs this
# with continue-on-error, so an unsigned commit is reported but does not fail
# the pull request. Pull requests opened before CONTRIBUTING.md existed have no
# sign-offs, and a required check would block them for a rule they never saw.
# To enforce it: set the repository variable DCO_ENFORCE to "true", then make
# the "DCO sign-off" check required on main. Both are owner actions.
#
# THE RULE. A commit passes when one of its Signed-off-by trailers carries the
# commit's author email, compared case-insensitively. The name is not compared,
# because the same person writes it differently in different places. Merge
# commits are skipped. GitHub's merge button writes them, and they add no change
# of their own beyond the commits they join.
#
# ONLY THE COMMITS A PULL REQUEST ADDS. None of the 1,093 non-merge commits on
# main before 2026-10-25 is signed off, and history is not rewritten to add one.
#
# Usage:
#   scripts/dco.sh <base> <head>   check every commit in base..head
#   scripts/dco.sh --self-test     prove the check still fails when it should
set -uo pipefail
cd "$(dirname "$0")/.."

# check_range prints one FAIL line per unsigned commit in $1..$2 and returns 1
# if there was any. It returns 2 if either ref does not resolve.
check_range() {
  local base="$1" head="$2" sha author subject trailers bad=0 n=0 ok email
  for ref in "$base" "$head"; do
    if ! git rev-parse --verify --quiet "${ref}^{commit}" >/dev/null; then
      echo "dco: ${ref} is not a commit here. Fetch it (actions/checkout needs fetch-depth: 0)." >&2
      return 2
    fi
  done
  while IFS=$'\x1f' read -r sha author subject trailers; do
    [ -z "$sha" ] && continue
    n=$((n + 1))
    ok=0
    while IFS= read -r email; do
      if [ -n "$email" ] && [ "${email,,}" = "${author,,}" ]; then
        ok=1
      fi
    done < <(printf '%s\n' "$trailers" | tr '\036' '\n' | sed -nE 's/.*<([^>]+)>.*/\1/p')
    if [ "$ok" -eq 0 ]; then
      echo "FAIL  ${sha:0:12} ${subject}"
      echo "      no Signed-off-by: line for its author, <${author}>"
      bad=$((bad + 1))
    fi
  done < <(git log --no-merges \
    --format='%H%x1f%ae%x1f%s%x1f%(trailers:key=Signed-off-by,valueonly,unfold,separator=%x1e)' \
    "${base}..${head}")

  if [ "$bad" -gt 0 ]; then
    echo "dco: ${bad} of ${n} commit(s) are not signed off by their author."
    echo "     Sign off each one: \`git rebase --signoff ${base}\` adds the line to every"
    echo "     commit on the branch, and then the branch needs a force-push. That rewrites"
    echo "     the branch, so it is the author's choice. CONTRIBUTING.md says what the"
    echo "     sign-off certifies."
    return 1
  fi
  echo "dco: ${n} commit(s) in ${base:0:12}..${head:0:12}, all signed off by their author"
}

self_test() {
  local tmp out rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  # Nothing here may reach this repository: a hook's GIT_DIR would point the
  # scratch commits at it (see .githooks/pre-push).
  # shellcheck disable=SC2046 # one variable name per word, split on purpose
  unset $(git rev-parse --local-env-vars)
  export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
  git -C "$tmp" init -q -b main
  g() { git -C "$tmp" -c user.name=Ann -c user.email=Ann@Example.com -c commit.gpgsign=false "$@"; }
  g commit -q --allow-empty -m base
  g checkout -q -b topic
  g commit -q --allow-empty -m signed -m 'Signed-off-by: Ann <ann@example.com>'
  g commit -q --allow-empty -m unsigned
  g commit -q --allow-empty -m 'signed by someone else' -m 'Signed-off-by: Bob <bob@example.com>'
  g commit -q --allow-empty -m 'mentions it in the body, Signed-off-by: Ann <ann@example.com>, not as a trailer'
  g checkout -q main
  g commit -q --allow-empty -m 'main moves on'
  g checkout -q topic
  g merge -q --no-ff --no-edit main

  out="$(cd "$tmp" && check_range main topic)"; rc=$?
  if [ "$rc" -ne 1 ] || [ "$(printf '%s\n' "$out" | grep -c '^FAIL')" -ne 3 ]; then
    echo "dco: SELF-TEST FAIL -- expected exactly 3 unsigned commits (rc 1), got rc ${rc}:" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
  for want in unsigned 'signed by someone else' 'mentions it in the body'; do
    if ! printf '%s\n' "$out" | grep -q "^FAIL .* ${want}"; then
      echo "dco: SELF-TEST FAIL -- \"${want}\" was not reported" >&2
      return 1
    fi
  done
  if printf '%s\n' "$out" | grep -qE "^FAIL .* (signed|main moves on|Merge)$"; then
    echo "dco: SELF-TEST FAIL -- a signed-off or merge commit was reported" >&2
    return 1
  fi
  out="$(cd "$tmp" && check_range main topic~4)"; rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "dco: SELF-TEST FAIL -- a range of signed-off commits was refused:" >&2
    printf '%s\n' "$out" >&2
    return 1
  fi
  (cd "$tmp" && check_range main no-such-ref) >/dev/null 2>&1; rc=$?
  if [ "$rc" -ne 2 ]; then
    echo "dco: SELF-TEST FAIL -- an unknown ref gave rc ${rc}, not 2" >&2
    return 1
  fi
  echo "dco: self-test passed (3 unsigned commits found, signed and merge commits passed, bad ref refused)"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") echo "usage: $0 <base> <head> | --self-test" >&2; exit 2 ;;
  *) [ $# -eq 2 ] || { echo "usage: $0 <base> <head> | --self-test" >&2; exit 2; }
     check_range "$1" "$2" ;;
esac
