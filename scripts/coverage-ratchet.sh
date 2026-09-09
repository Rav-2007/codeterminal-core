#!/usr/bin/env bash
#
# coverage-ratchet.sh — fail if any package's test coverage dropped below its floor.
#
# Usage:
#   scripts/coverage-ratchet.sh                 # all six modules
#   scripts/coverage-ratchet.sh proxy daemon    # named modules only (CI does this)
#
# WHY THIS EXISTS
#
# Phase 2 of the robustness program added daemon counters and watched coverage
# fall from 69.3% to 68.9%. The uncovered code turned out to be printStatus --
# untestable because it took an *os.File, and the exact place a deliberate
# output-ordering property lived. The number found a real gap.
#
# But it found it because a human ran `go test -cover` and read the result. That
# is a habit, not a control, and habits do not survive contact with a deadline.
# This script is the control.
#
# WHAT IT DELIBERATELY DOES NOT DO
#
# It does not compute a repo-wide average. An average lets a well-tested package
# pay for a neglected one, which is precisely the trade the two lowest floors
# here (helper at 6.5%) must not be allowed to make silently.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
floors_file="$repo_root/scripts/coverage-floors.txt"

# There is no root Go module -- only go.work -- so `./...` from the repo root
# fails outright ("directory prefix . does not contain modules listed in
# go.work"). Every module is therefore entered and tested explicitly. Do not
# "simplify" this to a root-level ./... ; it does not work.
ALL_MODULES=(daemon editapply proxy helper protocol clients/tui)

if [ $# -gt 0 ]; then
  MODULES=("$@")
else
  MODULES=("${ALL_MODULES[@]}")
fi

if [ ! -f "$floors_file" ]; then
  echo "ratchet: missing floors file: $floors_file" >&2
  exit 2
fi

# --- load floors -------------------------------------------------------------

declare -A floor
# Counted explicitly rather than via ${#floor[@]}: under `set -u`, expanding the
# length of an EMPTY associative array aborts the script with "unbound variable"
# before the emptiness check below can run. That is fail-closed by luck (bash
# exits 1) rather than by design, and it loses the diagnostic. Found by neutering
# this script with an empty floors file -- the check that exists to catch a
# vacuous pass was itself unreachable.
floors_loaded=0
while read -r pkg pct _rest; do
  [ -z "${pkg:-}" ] && continue
  case "$pkg" in \#*) continue ;; esac
  if [ -z "${pct:-}" ]; then
    echo "ratchet: malformed floor line for '$pkg' (expected: <import-path> <float>)" >&2
    exit 2
  fi
  floor["$pkg"]="$pct"
  floors_loaded=$((floors_loaded + 1))
done < "$floors_file"

if [ "$floors_loaded" -eq 0 ]; then
  echo "ratchet: no floors parsed from $floors_file -- refusing to pass vacuously" >&2
  exit 2
fi

# --- measure -----------------------------------------------------------------

status=0
declare -A seen

for module in "${MODULES[@]}"; do
  module_dir="$repo_root/$module"
  if [ ! -d "$module_dir" ]; then
    echo "ratchet: no such module directory: $module" >&2
    status=1
    continue
  fi

  # -cover without -race: the race detector is a separate gate with its own step,
  # and instrumenting for both makes the daemon suite meaningfully slower without
  # changing a single coverage number.
  output="$(cd "$module_dir" && go test -cover ./... 2>&1)"
  rc=$?
  if [ $rc -ne 0 ]; then
    echo "ratchet: 'go test' FAILED in $module -- coverage not evaluated" >&2
    echo "$output" >&2
    status=1
    continue
  fi

  # `go test -cover` emits the package name in one of two columns depending on
  # whether the package had tests:
  #
  #   ok  \tcodeterminal/proxy\t8.4s\tcoverage: 83.6% of statements
  #       \tcodeterminal/proxy/testharness\t\tcoverage: 0.0% of statements
  #   ?   \tcodeterminal/foo\t[no test files]
  #
  # The second shape -- leading TAB, no "ok" -- is what a package with no test
  # files looks like under -cover, and an earlier version of this parser matched
  # only the first and third. A newly added test-only-adjacent package therefore
  # slipped through the gate silently, which is precisely the failure this script
  # claims to prevent. Found when proxy/testharness was added. Both shapes are
  # normalised here by skipping a leading ok/?/FAIL token if there is one.
  while IFS= read -r line; do
    case "$line" in
      *coverage:*)
        pkg="$(awk '{ print ($1 == "ok" || $1 == "?" || $1 == "FAIL") ? $2 : $1 }' <<<"$line")"
        pct="$(sed -n 's/.*coverage: \([0-9.]*\)% of statements.*/\1/p' <<<"$line")"
        ;;
      # Counted as 0.0 rather than skipped. A package with no tests at all is the
      # single most likely place for a regression to hide, so it must be listed
      # with an explicit floor (0.0 if that is the honest answer) rather than
      # falling through the gate for the very reason it is risky.
      *"[no test files]"*)
        pkg="$(awk '{ print ($1 == "ok" || $1 == "?" || $1 == "FAIL") ? $2 : $1 }' <<<"$line")"
        pct="0.0"
        ;;
      *) continue ;;
    esac

    [ -z "$pkg" ] && continue
    seen["$pkg"]=1

    if [ -z "${floor[$pkg]:-}" ]; then
      echo "FAIL  $pkg — no floor recorded. Add it to scripts/coverage-floors.txt (measured: ${pct}%)." >&2
      status=1
      continue
    fi

    want="${floor[$pkg]}"
    if awk -v got="$pct" -v want="$want" 'BEGIN { exit !(got < want) }'; then
      delta="$(awk -v got="$pct" -v want="$want" 'BEGIN { printf "%.1f", want - got }')"
      echo "FAIL  $pkg — coverage ${pct}% is below the ${want}% floor (down ${delta})." >&2
      status=1
    else
      over="$(awk -v got="$pct" -v want="$want" 'BEGIN { printf "%.1f", got - want }')"
      echo "ok    $pkg — ${pct}% (floor ${want}%, +${over})"
    fi
  done <<<"$output"
done

# --- a floor whose package vanished is a gate that silently stopped gating -----
#
# THIS USED TO RUN ONLY ON A FULL INVOCATION, AND CI NEVER MAKES ONE.
# build.yml calls this script as `coverage-ratchet.sh ${{ matrix.module }}`, one
# module per matrix job, so `$# -eq 0` was false in every CI run this repository
# has ever had. The sweep existed, was tested, and gated nothing outside a
# developer's laptop -- found on 2026-09-09 while comparing the two gate lists.
#
# The old comment said a partial run "legitimately does not visit them", which
# was true only because the check was written whole-repo. A partial run cannot
# speak for OTHER modules' floors; it can speak for its own, and that is the
# scope applied here. Each module's floors are now swept by the job that owns
# them, so the union of CI's six jobs covers exactly what a full local run does.
belongs_to_module() {
  # floor keys are import paths (codeterminal/daemon/mcp); modules are
  # directories (daemon, clients/tui). Strip the module path prefix and ask
  # whether what remains is the module itself or something beneath it.
  local pkg="${1#codeterminal/}" m="$2"
  [ "$pkg" = "$m" ] || [ "${pkg#"$m"/}" != "$pkg" ]
}

for pkg in "${!floor[@]}"; do
  [ -n "${seen[$pkg]:-}" ] && continue
  if [ $# -eq 0 ]; then
    echo "FAIL  $pkg — has a floor but was never measured (renamed or deleted?)." >&2
    status=1
    continue
  fi
  for m in "${MODULES[@]}"; do
    if belongs_to_module "$pkg" "$m"; then
      echo "FAIL  $pkg — has a floor, belongs to module '$m' which WAS measured, and was never seen" >&2
      echo "      (renamed or deleted?). A floor for a package that no longer exists is a gate" >&2
      echo "      that silently stopped gating." >&2
      status=1
      break
    fi
  done
done

if [ $status -ne 0 ]; then
  echo "" >&2
  echo "ratchet: FAILED. Floors are in scripts/coverage-floors.txt and may only be" >&2
  echo "raised, never lowered to make a build green -- see the header there." >&2
fi

exit $status
