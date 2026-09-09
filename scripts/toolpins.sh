#!/usr/bin/env bash
# Sourced helper. NOT a gate -- it has no exit status of its own and runs
# nothing on its own. It exists so lint.sh, errcheck-ceiling.sh and
# govulncheck.sh read their pinned versions from scripts/tool-pins.txt instead
# of each carrying a copy.
#
# PRESENCE IS NOT CAPABILITY, for the third time in this repository. bwrap on
# Ubuntu 24.04 exists and cannot unshare; a tokenizer accepts bytes and panics;
# and a linter can be on PATH and be a different tool from the one CI runs.
# `command -v` answered yes to all three. On 2026-09-09 `make check` linted with
# staticcheck v0.7.0 while CI pinned v0.8.1, and the gate reported green.

_toolpins_file() { echo "$(dirname "${BASH_SOURCE[0]}")/tool-pins.txt"; }

# pin_spec <binary> -> "module@version", empty if unpinned
pin_spec() { awk -v n="$1" '$1==n{print $2}' "$(_toolpins_file)"; }

# pin_toolchain -> the GOTOOLCHAIN value the install commands need
pin_toolchain() { awk '$1=="GOTOOLCHAIN"{print $2}' "$(_toolpins_file)"; }

# pin_version <binary> -> the version half of the pin
pin_version() { pin_spec "$1" | sed 's/.*@//'; }

# installed_version <binary> -> the version the binary on PATH was built from
installed_version() {
  local p; p="$(command -v "$1" 2>/dev/null)" || return 1
  [ -n "$p" ] || return 1
  go version -m "$p" 2>/dev/null | awk '$1=="mod"{print $3; exit}'
}

# tool_install_cmd <binary> -> the exact command that installs the pinned version
tool_install_cmd() { echo "GOTOOLCHAIN=$(pin_toolchain) go install $(pin_spec "$1")"; }

# check_tool <binary> -> 0 ok, 1 missing, 2 wrong version. Prints the reason and
# the remedy, because a gate red for a reason nobody can act on gets waived.
check_tool() {
  local name="$1" want got
  want="$(pin_version "$name")"
  if ! command -v "$name" >/dev/null 2>&1; then
    echo "lint: $name not found on PATH" >&2
    return 1
  fi
  [ -z "$want" ] && return 0
  got="$(installed_version "$name")"
  if [ -z "$got" ]; then
    # Not fatal: a binary built outside the module cache has no mod line. Say so
    # rather than guessing, so an unverifiable version is never read as a match.
    echo "lint: $name version could not be determined (not a Go-module build?); pinned is $want" >&2
    return 0
  fi
  if [ "$got" != "$want" ]; then
    echo "lint: $name is $got, but scripts/tool-pins.txt pins $want" >&2
    echo "      CI runs the pinned version, so this gate is not the one that will run on your push." >&2
    return 2
  fi
  return 0
}
