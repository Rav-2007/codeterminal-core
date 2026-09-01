#!/usr/bin/env bash
# Go version floor: every module must state the same one, and it must be a floor
# that actually holds.
#
# WHY THIS EXISTS. Item 22 (docs/OPEN_ITEMS.md) was closed by `6bbe549` after
# bumping `go.work` to go1.25.13 and measuring 0 reachable vulnerabilities across
# all six modules. That measurement was true and the fix was incomplete, because
# both were taken in the one configuration where `go.work` governs.
#
# Two directives, and they are not the same kind of thing:
#
#   toolchain go1.25.13   SELECTION HINT. Consulted only when the go command is
#                         deciding whether to switch. GOTOOLCHAIN=local skips
#                         that decision, and the line is then ignored outright.
#   go 1.25.13            HARD FLOOR. A toolchain below it is an error, always.
#
# WHY THIS CHECKS THE GO LINE AND ONLY TOLERATES THE TOOLCHAIN LINE. The first
# version of this script required both to be present and equal. That is not a
# state the repo can hold: `go mod tidy` DELETES a toolchain line that does not
# exceed the go directive, because at that point it selects nothing the go line
# has not already required. Demanding it back would have put this gate in a loop
# with the toolchain itself, and a gate that argues with `go mod tidy` is a gate
# people pass with --no-verify. So: the go line must equal the floor, and a
# toolchain line is optional but may never sit BELOW it.
#
# Measured 2026-09-01 on a machine whose installed toolchain was go1.25.12:
# `daemon`, which HAS `toolchain go1.25.13`, built rc=0 under
# `GOTOOLCHAIN=local GOWORK=off` and govulncheck then reported FOUR reachable
# stdlib flaws. `proxy` -- the only internet-facing component -- reported five.
# The hint was doing nothing and looked like a pin.
#
# WHY A SCRIPT AND NOT A ONE-TIME FIX. A floor spread across seven files is a
# state, not an event. Nothing in this repo compared any Go version string to any
# other before this script: thirteen places name one, and drift in any of them is
# invisible until someone builds in the configuration that exposes it. That is
# the same argument scripts/actions-pinned.sh makes about mutable tags.
#
# WHAT IT DELIBERATELY DOES NOT CHECK. Dated report headers under docs/ name the
# toolchain a measurement was taken on. Those are records, not pins, and demanding
# their falsification is how a gate teaches people to route around it. Nor does it
# check that the floor is the NEWEST Go -- that is a decision, and govulncheck.sh
# is what answers whether the current one is still safe.
#
# ENFORCED in `make supplychain` (via `make check`) and in .githooks/pre-push. It
# is in the hook because it meets the hook's stated admission rule -- sub-second,
# offline, no toolchain needed -- which its sibling govulncheck.sh does not.
#
# TO RAISE THE FLOOR: edit go.work's `go` and `toolchain` lines, then run
#   sed -i 's/^go 1\.25\.13$/go <new>/' go.work */go.mod clients/tui/go.mod
# and re-run this script; it names every file still disagreeing.
set -uo pipefail
# Resolved BEFORE the cd, because --self-test re-invokes this script from a
# fixture directory and a relative $0 does not survive that.
SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
cd "$(dirname "$0")/.."

WORK="${GO_PINS_WORK:-go.work}"
FLOOR_MIN="${GO_PINS_FLOOR:-7}"
MODULES="${GO_PINS_MODULES:-daemon editapply proxy helper protocol clients/tui}"

# --self-test proves THIS SCRIPT still detects drift, on fixtures whose answer is
# known. A checker that has only ever seen agreeing input cannot demonstrate it
# can fail, and every gate in this repo that skipped this step was later found
# passing over something broken.
if [ "${1:-}" = "--self-test" ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/a" "$tmp/b"
  printf 'go 1.25.13\n\ntoolchain go1.25.13\n\nuse (\n\t./a\n\t./b\n)\n' > "$tmp/go.work"
  printf 'module a\n\ngo 1.25.13\n' > "$tmp/a/go.mod"
  printf 'module b\n\ngo 1.25.13\n' > "$tmp/b/go.mod"
  # GO_PINS_WORK is absolute: the script cds to its own repo root, so a relative
  # fixture path would resolve against the wrong tree and silently check nothing.
  run() { GO_PINS_WORK="$tmp/go.work" GO_PINS_MODULES="$tmp/a $tmp/b" GO_PINS_FLOOR=3 "$SELF"; }

  # (a) an agreeing set MUST pass, or this checker is merely always-red.
  if ! run >/dev/null 2>&1; then
    echo "go-toolchain-pinned: SELF-TEST FAIL -- an agreeing set was reported as disagreeing."
    echo "  This checker cannot be trusted until it is fixed."
    exit 1
  fi

  # (b) a go line BELOW the floor MUST fail. This is the drift the script exists
  # for, and the state every module was in before 2026-09-01.
  printf 'module b\n\ngo 1.25.0\n' > "$tmp/b/go.mod"
  if run >/dev/null 2>&1; then
    echo "go-toolchain-pinned: SELF-TEST FAIL -- a module below the floor was accepted."
    exit 1
  fi

  # (c) a toolchain line BELOW the floor MUST fail even when the go line is right.
  # That is the shape the original bug took: a version statement selecting an
  # older toolchain while reading as a pin.
  printf 'module b\n\ngo 1.25.13\n\ntoolchain go1.25.12\n' > "$tmp/b/go.mod"
  if run >/dev/null 2>&1; then
    echo "go-toolchain-pinned: SELF-TEST FAIL -- a toolchain line below the floor was accepted."
    exit 1
  fi

  # (d) a module with NO toolchain line MUST pass. `go mod tidy` deletes the
  # redundant line, so failing here would make this gate argue with the toolchain
  # on every tidy -- the way a gate earns a --no-verify.
  printf 'module b\n\ngo 1.25.13\n' > "$tmp/b/go.mod"
  if ! run >/dev/null 2>&1; then
    echo "go-toolchain-pinned: SELF-TEST FAIL -- a tidied module (no toolchain line) was rejected."
    echo "  This gate would fight 'go mod tidy' on every run."
    exit 1
  fi

  # (e) the REAL tree must still parse, with its own hardcoded floor so that
  # lowering the floor below cannot also disable this.
  real=$(GO_PINS_FLOOR=1 "$SELF" --count 2>/dev/null || echo 0)
  if [ "${real:-0}" -lt 7 ]; then
    echo "go-toolchain-pinned: SELF-TEST FAIL -- parsed $real pin site(s) from the real tree (expected >= 7)."
    echo "  The file layout moved and the parser went blind."
    exit 1
  fi

  echo "go-toolchain-pinned: self-test ok (rejects a low go line and a low toolchain line, accepts a tidied one; parses $real real sites)"
  exit 0
fi

if [ ! -f "$WORK" ]; then
  echo "FAIL  $WORK does not exist. It is where the floor is declared;"
  echo "      a floor this check cannot find is a floor it cannot verify."
  exit 1
fi

floor_go="$(grep -E '^go [0-9]' "$WORK" | head -1 | awk '{print $2}')"
floor_tc="$(grep -E '^toolchain ' "$WORK" | head -1 | awk '{print $2}')"

if [ -z "$floor_go" ] || [ -z "$floor_tc" ]; then
  echo "FAIL  $WORK is missing a 'go' or 'toolchain' line, so there is no floor to compare against."
  echo "      Expected both, e.g. 'go 1.25.13' and 'toolchain go1.25.13'."
  exit 1
fi

fail=0
found=1  # go.work itself

for m in $MODULES; do
  f="$m/go.mod"
  if [ ! -f "$f" ]; then
    echo "FAIL  $f does not exist, but $WORK lists the module."
    echo "      Either the module moved or GO_PINS_MODULES is stale."
    fail=1
    continue
  fi
  found=$((found + 1))

  mod_go="$(grep -E '^go [0-9]' "$f" | head -1 | awk '{print $2}')"
  mod_tc="$(grep -E '^toolchain ' "$f" | head -1 | awk '{print $2}')"

  # THE GO LINE IS THE ONE THAT ENFORCES. Checked first and described plainly,
  # because this is the difference the whole script turns on.
  if [ "$mod_go" != "$floor_go" ]; then
    echo "FAIL  $f -- go directive is '${mod_go:-missing}', $WORK says '$floor_go'"
    echo "      The go directive is the HARD floor: it is enforced under every"
    echo "      GOTOOLCHAIN setting, workspace or not. A module below it builds on"
    echo "      an older stdlib with no error. Set: go $floor_go"
    fail=1
  fi

  # A toolchain line is OPTIONAL (go mod tidy removes a redundant one), but one
  # that sits BELOW the floor is the original bug wearing a pin's clothes: it
  # reads as a version statement and selects something older.
  if [ -n "$mod_tc" ] && [ "$mod_tc" != "$floor_tc" ]; then
    lower="$(printf '%s\n%s\n' "${mod_tc#go}" "${floor_tc#go}" | sort -V | head -1)"
    if [ "$lower" = "${mod_tc#go}" ]; then
      echo "FAIL  $f -- toolchain is '$mod_tc', below the floor '$floor_tc'"
      echo "      Remove it (the go directive already requires $floor_go) or raise it."
      fail=1
    fi
  fi
done

# ANTI-VACUITY. A moved directory or a broken parse would print agreement over an
# empty set, which is the most convincing wrong answer this script could give.
if [ "$found" -lt "$FLOOR_MIN" ]; then
  echo "FAIL  found only $found pin site(s) (floor $FLOOR_MIN). This check parsed almost"
  echo "      nothing, so its verdict is meaningless. Fix the parser, do not lower the floor."
  exit 1
fi

if [ "${1:-}" = "--count" ]; then
  echo "$found"
  exit 0
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "go-toolchain-pinned: $found pin site(s) agree on $floor_tc (go directive $floor_go)"
