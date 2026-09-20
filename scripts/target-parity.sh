#!/usr/bin/env bash
# ------------------------------------------------------------------------------
# THE TARGET SET IS WRITTEN OUT IN FIVE PLACES. THIS ASSERTS THEY AGREE.
#
# scripts/release-targets.txt is the source of truth, and four places mirror it
# because they cannot read it:
#
#   1. release.yml's `binaries` matrix   -- GitHub Actions has no include
#                                           directive; a matrix must be literal.
#   2. release.yml's packaging loop      -- `for TARGET in ...`
#   3. release.yml's staging loop        -- `for TARGET in ...`
#   4. stage-runtime.js's validation     -- a regex of the legal target names
#   5. any COUNT of targets in release.yml -- see below; checked by absence.
#
# WHY THIS GATE EXISTS. Dropping darwin-arm64 on 2026-09-17 meant editing all
# six. Miss one and CI stays green while the release is wrong, and the failure
# is silent in both directions: the matrix builds a binary no loop packages, or
# a loop packages one the matrix never built and `cp` fails halfway through a
# release. Neither is caught by any existing gate -- release.yml is only
# exercised by a release.
#
# MIRROR 5 IS A DIFFERENT SHAPE, and it was added on 2026-09-20 because this
# gate was green while the release was broken. Mirrors 1-4 are LISTS of target
# names, and this script compares lists. release.yml also contained a NUMBER --
# `[ "$N" -eq 3 ]`, asserting three staged terminal-client binaries. 43e17bc
# edited both loops from three names to two and left the 3, because a 3 is not
# a target name and nothing here was looking for one. Two files staged, three
# demanded: `package` would have died before the signing guard and before the
# attach, so the tag would have published nothing.
#
# A count cannot be compared against the source of truth the way a list can --
# a correct count and a stale count are both just integers. So mirror 5 is
# enforced by ABSENCE: release.yml must contain no literal count threshold at
# all, and must derive one from the loop it is counting. `-eq 0` and `-ne 0`
# stay legal, because a zero is an exit-status check, not a tally.
#
# clients/vscode/scripts/verify-vsix.js is deliberately NOT in this list. It
# asks `target.startsWith('win32')` rather than enumerating, so it has nothing
# to drift from. That is the pattern the other four should eventually adopt;
# until they can, this gate stands in for it. Mirror 5 IS that pattern, applied
# to counts: the fix was not to check the number but to stop writing one down.
#
# NOT CHECKED, and not claimed: that the targets are the RIGHT ones, that the
# runners exist, or that anything builds. This gate asks one question -- do the
# six places agree -- and a green result means only that. In particular mirror 5
# proves release.yml hard-codes no count; it does NOT prove the derived count is
# the right one, which only a release run can show.
#
#   target-parity.sh              check the working tree
#   target-parity.sh --self-test  prove the check can fail
# ------------------------------------------------------------------------------
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
failures=0

say()  { echo "target-parity: $*"; }
err()  { echo "target-parity: $*" >&2; failures=$((failures + 1)); }

# expected_targets prints the target names from the source of truth, in order.
expected_targets() { # expected_targets <targets-file>
  local name signing rest
  while read -r name signing rest; do
    case "$name" in "" | \#* | "signing-required:") continue ;; esac
    echo "$name"
  done < "$1"
}

# expected_runners prints "<target> <runner>" pairs from the source of truth.
expected_runners() { # expected_runners <targets-file>
  local name signing runner
  while read -r name signing runner; do
    case "$name" in "" | \#* | "signing-required:") continue ;; esac
    echo "$name $runner"
  done < "$1"
}

compare() { # compare <what> <expected-newline-list> <actual-newline-list>
  local what="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    say "ok -- $what"
    return 0
  fi
  err "MISMATCH in $what"
  err "  source of truth (scripts/release-targets.txt):"
  echo "$want" | sed 's/^/    /' >&2
  err "  this mirror says:"
  echo "$got" | sed 's/^/    /' >&2
  err "  Edit whichever is wrong. If a target was added or removed, every mirror"
  err "  named in this script's header has to move together."
  return 1
}

run_check() { # run_check <root>
  local root="$1"
  local tfile="$root/scripts/release-targets.txt"
  local rel="$root/.github/workflows/release.yml"
  local stage="$root/clients/vscode/scripts/stage-runtime.js"
  failures=0

  local f
  for f in "$tfile" "$rel" "$stage"; do
    if [ ! -f "$f" ]; then
      err "FAIL: $f does not exist; this gate cannot compare what it cannot read"
      return 1
    fi
  done

  local want want_pairs
  want="$(expected_targets "$tfile")"
  want_pairs="$(expected_runners "$tfile")"
  if [ -z "$want" ]; then
    err "FAIL: $tfile declares no targets; refusing to assert that five mirrors"
    err "      agree with nothing, which they trivially would"
    return 1
  fi

  # 1. the binaries matrix, as `- os: X` / `target: Y` pairs.
  local matrix
  matrix="$(awk '
    /^[[:space:]]*-[[:space:]]*os:[[:space:]]*/ { os=$NF; next }
    /^[[:space:]]*target:[[:space:]]*/          { if (os != "") { print $NF, os; os="" } }
  ' "$rel")"
  compare "release.yml binaries matrix (target + runner)" "$want_pairs" "$matrix"

  # 2 and 3. every `for TARGET in ...` loop, each checked separately so a gate
  # failure names WHICH loop drifted rather than just "a loop did".
  #
  # COMMENT LINES ARE SKIPPED, since 2026-09-20. This scanner matched any line
  # CONTAINING `for TARGET in`, so the first comment in release.yml's history to
  # mention the loops -- one written to explain the bug mirror 5 now catches --
  # was parsed as a third loop and failed the gate with a mismatch listing the
  # comment's own words as target names. A gate that cannot be described in
  # prose next to the thing it guards gets worked around, not fixed.
  local n=0 line loop
  while IFS= read -r line; do
    n=$((n + 1))
    loop="$(echo "$line" | sed 's/.*for TARGET in //; s/;.*//' | tr ' ' '\n' | grep -v '^$')"
    compare "release.yml \`for TARGET in\` loop #$n" "$want" "$loop"
  done < <(grep -h 'for TARGET in' "$rel" | grep -v '^[[:space:]]*#')
  if [ "$n" -eq 0 ]; then
    err "FAIL: found no \`for TARGET in\` loop in release.yml. Either the"
    err "      packaging loops were restructured and this gate needs updating,"
    err "      or they were deleted -- both need a human, and neither is a pass."
  fi

  # 4. stage-runtime.js's validation regex.
  local re
  re="$(sed -n 's/.*\/\^(\([^)]*\))\$\/.*/\1/p' "$stage" | head -1 | tr '|' '\n' | grep -v '^$')"
  if [ -z "$re" ]; then
    err "FAIL: could not find the target regex in $stage. It is the only thing"
    err "      stopping a typo'd --target from packaging silently."
  else
    compare "stage-runtime.js target regex" "$want" "$re"
  fi

  # 5. NO LITERAL COUNT OF TARGETS ANYWHERE IN release.yml.
  #
  # Checked by absence rather than by comparison -- see this script's header for
  # why a count cannot be compared. `-eq 0` / `-ne 0` are exempt: a zero is an
  # exit-status or emptiness check, never a tally of targets.
  #
  # Whole-line comments are skipped, for the reason given at the loop scanner
  # above. NOT covered, and deliberately not attempted: a trailing comment on a
  # code line. Stripping those means deciding whether a `#` is a comment or a
  # character in a string, and guessing wrong would make this gate fail on
  # correct code -- the one thing that would get it deleted.
  local counts
  counts="$(awk '$0 !~ /^[[:space:]]*#/ { print FNR": "$0 }' "$rel" \
    | grep -E '\-(eq|ne)[[:space:]]+[1-9][0-9]*([^0-9]|$)' || true)"
  if [ -n "$counts" ]; then
    err "FAIL: release.yml hard-codes a count. Derive it from the loop instead."
    echo "$counts" | sed 's/^/    /' >&2
    err "  A count is a mirror of the target set that this gate cannot compare"
    err "  against scripts/release-targets.txt: a stale 3 and a correct 2 are"
    err "  both just integers. On 2026-09-20 exactly this shape would have made"
    err "  a v0.0.2 tag publish nothing -- two binaries staged, three demanded."
    err "  Count in the loop body (EXPECT=\$((EXPECT + 1))) so the tally cannot"
    err "  drift from the enumeration, because it IS the enumeration."
    failures=$((failures + 1))
  else
    say "ok -- release.yml hard-codes no target count (mirror 5, by absence)"
  fi

  [ "$failures" -eq 0 ]
}

self_test() {
  local tmp pass=0 fail=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  check() { # check <desc> <expected:pass|fail> <rc>
    if { [ "$2" = "pass" ] && [ "$3" -eq 0 ]; } || { [ "$2" = "fail" ] && [ "$3" -ne 0 ]; }; then
      printf '  ok    %s\n' "$1"; pass=$((pass + 1))
    else
      printf '  FAIL  %s (expected %s, rc=%s)\n' "$1" "$2" "$3"; fail=$((fail + 1))
    fi
  }

  # mkfixture builds a miniature repo whose five places all agree.
  mkfixture() { # mkfixture <dir>
    local d="$1"
    rm -rf "$d"
    mkdir -p "$d/scripts" "$d/.github/workflows" "$d/clients/vscode/scripts"
    printf 'linux-x64    none    ubuntu-latest\nwin32-x64    none    windows-latest\nsigning-required: none\n' \
      > "$d/scripts/release-targets.txt"
    cat > "$d/.github/workflows/release.yml" <<'YAML'
        include:
          - os: ubuntu-latest
            target: linux-x64
          - os: windows-latest
            target: win32-x64
      - run: |
          for TARGET in linux-x64 win32-x64; do
            echo "$TARGET"
          done
      - run: |
          for TARGET in linux-x64 win32-x64; do
            echo "$TARGET"
          done
YAML
    printf 'if (target && !/^(linux-x64|win32-x64)$/.test(target)) {\n' \
      > "$d/clients/vscode/scripts/stage-runtime.js"
  }

  echo "target-parity self-test"

  local d="$tmp/ok"; mkfixture "$d"
  run_check "$d" >/dev/null 2>&1; check "a tree where all six agree -> green" pass $?

  # EACH MIRROR NEUTERED SEPARATELY. A gate that only catches one kind of drift
  # is a gate that will miss the other four on the day it matters.
  d="$tmp/n1"; mkfixture "$d"
  sed -i 's/target: win32-x64/target: darwin-arm64/' "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "matrix drifts from the file -> FAILS" fail $?

  d="$tmp/n2"; mkfixture "$d"
  sed -i '0,/for TARGET in linux-x64 win32-x64/s//for TARGET in linux-x64 darwin-arm64 win32-x64/' \
    "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "packaging loop drifts -> FAILS" fail $?

  d="$tmp/n3"; mkfixture "$d"
  # the SECOND loop only, to prove both are checked rather than just the first
  awk '{ if (/for TARGET in/ && ++c == 2) sub(/win32-x64/, "darwin-arm64"); print }' \
    "$d/.github/workflows/release.yml" > "$d/tmp.yml" && mv "$d/tmp.yml" "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "the SECOND loop drifting is caught too -> FAILS" fail $?

  d="$tmp/n4"; mkfixture "$d"
  sed -i 's/win32-x64)\$/darwin-arm64)$/' "$d/clients/vscode/scripts/stage-runtime.js"
  run_check "$d" >/dev/null 2>&1; check "stage-runtime regex drifts -> FAILS" fail $?

  d="$tmp/n5"; mkfixture "$d"
  sed -i 's/os: windows-latest/os: macos-latest/' "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "the RUNNER drifting is caught -> FAILS" fail $?

  d="$tmp/n6"; mkfixture "$d"
  printf 'linux-x64    none    ubuntu-latest\nwin32-x64    none    windows-latest\ndarwin-arm64 required macos-latest\nsigning-required: none\n' \
    > "$d/scripts/release-targets.txt"
  run_check "$d" >/dev/null 2>&1; check "adding a target to the FILE alone -> FAILS" fail $?

  d="$tmp/n7"; mkfixture "$d"
  printf '# only comments\n' > "$d/scripts/release-targets.txt"
  run_check "$d" >/dev/null 2>&1; check "an empty source of truth -> FAILS (not a vacuous pass)" fail $?

  d="$tmp/n8"; mkfixture "$d"
  sed -i '/for TARGET in/d' "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "loops deleted entirely -> FAILS" fail $?

  d="$tmp/n9"; mkfixture "$d"
  rm -f "$d/clients/vscode/scripts/stage-runtime.js"
  run_check "$d" >/dev/null 2>&1; check "a missing mirror file -> FAILS" fail $?

  # MIRROR 5. The literal below is the real one, verbatim: it stood in
  # release.yml from the day the staging step was written until 2026-09-20, and
  # every one of the nine arms above was green the whole time.
  d="$tmp/n10"; mkfixture "$d"
  printf '      - run: |\n          [ "$N" -eq 3 ] || exit 1\n' \
    >> "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "a hard-coded count in release.yml -> FAILS" fail $?

  # AND THE EXEMPTION IS REAL, not an accident of the regex. A zero threshold is
  # an exit-status check and must stay legal, or this gate becomes a tax on
  # every `|| exit` in the file and gets deleted by the first person it annoys.
  d="$tmp/n11"; mkfixture "$d"
  printf '      - run: |\n          [ "$rc" -eq 0 ] || exit 1\n          [ "$found" -ne 0 ] || exit 1\n' \
    >> "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "a zero threshold stays legal -> green" pass $?

  # PROSE IS NOT A MIRROR. Both arms below failed before 2026-09-20, which is
  # how they got written: the comment added to release.yml explaining mirror 5
  # tripped mirror 2 AND mirror 5 on its own text.
  d="$tmp/n12"; mkfixture "$d"
  printf '      # a comment saying for TARGET in linux-x64 darwin-arm64 win32-x64\n' \
    >> "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "a COMMENT naming a stale loop -> green" pass $?

  d="$tmp/n13"; mkfixture "$d"
  printf '      # this assertion used to read -eq 3 and nothing noticed\n' \
    >> "$d/.github/workflows/release.yml"
  run_check "$d" >/dev/null 2>&1; check "a COMMENT naming a stale count -> green" pass $?

  echo "target-parity: $pass passed, $fail failed"
  echo "NOT COVERED HERE: whether the targets are the right ones, whether the"
  echo "  runners exist, or whether anything builds. This gate asks only whether"
  echo "  the six places agree -- and for mirror 5, only that no count is written"
  echo "  down, never that the derived one is correct."
  [ "$fail" -eq 0 ]
}

case "${1:-}" in
  --self-test) self_test; exit $? ;;
  "")          run_check "$ROOT"; exit $? ;;
  *)           echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
