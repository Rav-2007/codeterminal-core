#!/usr/bin/env bash
#
# lint.sh — static analysis gate.
#
# Usage:
#   scripts/lint.sh                 # all six modules
#   scripts/lint.sh proxy daemon    # named modules only (CI does this)
#
# Tools, and why each earns its place rather than being here for completeness:
#
#   staticcheck  — the broad correctness set (SA*), which found a test that
#                  appended to a slice nothing ever read.
#   ineffassign  — assignments whose value is never used; the cheapest possible
#                  detector for "this line does nothing", which is the exact
#                  shape of the inert assertions this program keeps finding.
#   bodyclose    — unclosed HTTP response bodies. Named specifically because it
#                  would have found P1.5's leak on its own, without the
#                  connection-counting test that actually caught it.
#
# errcheck is NOT here, deliberately, and it is not an oversight -- see the note
# at the bottom of this file.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# No root Go module -- only go.work -- so `./...` from the root fails outright.
# Every module is entered explicitly. Do not "simplify" this.
ALL_MODULES=(daemon editapply proxy helper protocol clients/tui)

if [ $# -gt 0 ]; then
  MODULES=("$@")
else
  MODULES=("${ALL_MODULES[@]}")
fi

# staticcheck's default check set, minus ST1005 (error-string style).
#
# Spelled out rather than left implicit so that a staticcheck upgrade cannot
# silently widen or narrow the gate. The defaults are:
#   all,-ST1000,-ST1003,-ST1016,-ST1020,-ST1021,-ST1022,-ST1023
#
# ST1005 is dropped because all four of its hits on this tree are on text that
# is CORRECT as written: two are error strings opening with the proper noun
# "Intel Mac (darwin/amd64) is not supported...", and two are conventional CLI
# usage strings ending in "...". Satisfying the check would mean degrading four
# good operator-facing messages, so the check goes instead of the messages.
STATICCHECK_CHECKS='all,-ST1000,-ST1003,-ST1016,-ST1020,-ST1021,-ST1022,-ST1023,-ST1005'

# GOTOOLCHAIN is deliberately NOT set here -- the environment's choice stands.
#
# Installing bodyclose can need a pin, but only on a machine whose BASE toolchain
# is older than 1.25: bodyclose's go.mod carries no directive forcing an upgrade,
# so `go install` builds it with the base toolchain, and its dependency
# golang.org/x/sys requires >= 1.25. On a 1.23.4 base that fails. CI installs
# 1.25.x as the base, so it is unaffected. Pinning a version here would instead
# force CI to download a toolchain it does not need, so the pin lives in the
# install hint below, where the problem actually is.

missing=0
for tool in staticcheck ineffassign bodyclose; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "lint: $tool not found on PATH" >&2
    missing=1
  fi
done
if [ $missing -ne 0 ]; then
  echo "" >&2
  echo "Install them with:" >&2
  echo "  go install honnef.co/go/tools/cmd/staticcheck@latest" >&2
  echo "  go install github.com/gordonklaus/ineffassign@latest" >&2
  echo "  GOTOOLCHAIN=go1.25.12 go install github.com/timakin/bodyclose@latest" >&2
  exit 2
fi

bodyclose_bin="$(command -v bodyclose)"
status=0

for module in "${MODULES[@]}"; do
  module_dir="$repo_root/$module"
  if [ ! -d "$module_dir" ]; then
    echo "lint: no such module directory: $module" >&2
    status=1
    continue
  fi

  for check in staticcheck ineffassign bodyclose; do
    case "$check" in
      staticcheck) out="$(cd "$module_dir" && staticcheck -checks "$STATICCHECK_CHECKS" ./... 2>&1)" ;;
      ineffassign) out="$(cd "$module_dir" && ineffassign ./... 2>&1)" ;;
      # bodyclose is an analysis pass, so it runs through go vet's vettool hook.
      bodyclose)   out="$(cd "$module_dir" && go vet -vettool="$bodyclose_bin" ./... 2>&1)" ;;
    esac
    rc=$?

    if [ $rc -ne 0 ] || [ -n "$out" ]; then
      echo "FAIL  $module: $check" >&2
      [ -n "$out" ] && echo "$out" >&2
      status=1
    else
      echo "ok    $module: $check"
    fi
  done
done

if [ $status -ne 0 ]; then
  echo "" >&2
  echo "lint: FAILED — see scripts/lint.sh for what each tool gates and why." >&2
fi

exit $status

# ---------------------------------------------------------------------------
# On errcheck, which P3.5 named and this script does not run
#
# Measured on this tree: 329 findings, 157 of them outside tests. The
# distribution is the reason it is not wired up as a gate today --
#
#   17 os.Remove   13 fmt.Fprintf   10 db.Close   9 conn.Close   7 resp.Body.Close
#
# -- cleanup-path Closes, and writes to stdout/stderr. After an exclusion list
# covering the conventional cases, ~60 remain, and they are still dominated by
# `defer x.Close()` on concrete types and `enc.Encode` on a socket write whose
# error genuinely cannot be acted on (the peer is already gone).
#
# Turning that green means either ~60 `_ =` assignments, which is noise that
# makes real unchecked errors HARDER to see, or an exclusion list long enough
# that the gate becomes arbitrary. Either way it is a deliberate triage pass
# over 60 call sites -- its own piece of work, with its own judgement calls --
# not something to bolt onto a CI-wiring commit.
#
# Recorded in BACKLOG.md rather than dropped. The three tools above are adopted
# as HARD gates now, which is worth more than four tools adopted softly: this
# repo has already learned that a check nobody must pass is a check that drifts.
