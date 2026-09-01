#!/usr/bin/env bash
# Reachable vulnerabilities, in the gate a developer actually runs.
#
# WHY THIS IS NOT ONLY A CI JOB. build.yml has had a govulncheck job per module
# since `baad9f9` and it has been green -- because GitHub's runner installs
# whatever `1.25.x` resolves to today (go1.25.14 on 2026-08-30) and finds
# nothing. On this repository's own developer machine the same scan found TEN
# reachable standard-library vulnerabilities on the same commit.
#
# Both were true at once. `toolchain` names a MINIMUM, not a version: a newer
# installed toolchain is used as-is, an older one is upgraded to the minimum. CI
# floats above the line and a laptop sits exactly on it, so the two were
# scanning different standard libraries and only one of them was clean.
#
# A gate that passes in CI and would fail locally is not a gate, it is a
# geography lottery. This runs where the person changing the code is.
#
# WHAT IT SCANS. Every module in go.work. `./...` from the repo root does not
# work here -- there is no root module -- which is the same reason every Go step
# in build.yml names its module.
set -uo pipefail
cd "$(dirname "$0")/.."

MODULES=(daemon proxy helper editapply protocol clients/tui)

if ! command -v govulncheck >/dev/null 2>&1; then
  echo "FAIL  govulncheck is not on PATH."
  echo "      go install golang.org/x/vuln/cmd/govulncheck@latest"
  echo "      (and make sure \$(go env GOPATH)/bin is on your PATH -- scripts/lint.sh"
  echo "       records the same trap, which hid a real lint failure once)"
  exit 1
fi

fail=0
scanned=0
offline=0

# GOWORK=off -- SCAN THE WEAKER CONFIGURATION, not the stronger one.
#
# This loop used to run with the workspace active, which is the BEST case: the
# one arrangement in which go.work's floor covers every module at once. A module
# built on its own -- by the release job, by a Docker build that copies only its
# go.mod, by anyone consuming it -- gets whatever its own go.mod demands, and
# that is what this now measures.
#
# It is why item 22 could be closed with a measurement that was true. Scanning
# the configuration where the fix is guaranteed to work cannot discover that the
# fix does not work anywhere else.
for m in "${MODULES[@]}"; do
  out="$(cd "$m" && GOWORK=off govulncheck ./... 2>&1)"
  status=$?
  scanned=$((scanned + 1))

  # A network failure is NOT a clean scan and must never be reported as one.
  # govulncheck cannot reach https://vuln.go.dev offline, and the distinction
  # between "nothing found" and "could not look" is the whole point of this file.
  if printf '%s' "$out" | grep -qiE 'no such host|connection refused|dial tcp|i/o timeout|TLS handshake'; then
    echo "SKIP  $m -- could not reach the vulnerability database"
    offline=1
    continue
  fi

  n=$(printf '%s' "$out" | grep -cE '^Vulnerability #')
  if [ "$status" -ne 0 ] && [ "$n" -eq 0 ]; then
    echo "FAIL  $m -- govulncheck exited $status without reporting a vulnerability:"
    printf '%s\n' "$out" | tail -5
    fail=1
    continue
  fi
  if [ "$n" -gt 0 ]; then
    echo "FAIL  $m -- $n REACHABLE vulnerabilit(y/ies):"
    printf '%s' "$out" | grep -E '^Vulnerability #|Fixed in:' | sed 's/^/      /'
    fail=1
  fi
done

# ANTI-VACUITY. A renamed directory or an edited MODULES list would make this
# print a pass over an empty scan, which is the failure mode every other gate in
# this repo carries a floor against.
if [ "$scanned" -lt 6 ]; then
  echo "FAIL  scanned only $scanned module(s); go.work has 6. This verdict is meaningless."
  exit 1
fi

if [ "$offline" -eq 1 ]; then
  if [ "${VULN_OFFLINE_OK:-}" = "1" ]; then
    echo "govulncheck: OFFLINE, and VULN_OFFLINE_OK=1 was set -- NOTHING WAS CHECKED."
    echo "             CI never sets this. Re-run with a network before you trust a green build."
    exit 0
  fi
  echo "FAIL  the vulnerability database was unreachable, so nothing was verified."
  echo "      Set VULN_OFFLINE_OK=1 to continue anyway; it prints a warning rather than"
  echo "      pretending, and CI never sets it."
  exit 1
fi

if [ "$fail" -ne 0 ]; then
  echo
  echo "Reachable means govulncheck traced a call path from this code to the flaw."
  echo "For a standard-library finding the fix is the go directive in that module's own"
  echo "go.mod. This scan runs with GOWORK=off, so go.work is not in play and"
  echo "raising it changes nothing here. The go directive is the hard floor; the"
  echo "toolchain line is only a selection hint and GOTOOLCHAIN=local ignores it."
  echo "scripts/go-toolchain-pinned.sh keeps all seven files agreeing."
  exit 1
fi

echo "govulncheck: $scanned modules, 0 reachable vulnerabilities"
