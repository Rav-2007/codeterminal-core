#!/usr/bin/env bash
# ------------------------------------------------------------------------------
# THE COVERAGE GATE HAS A BLIND SPOT. THIS ONE KEEPS IT FROM GROWING.
#
# daemon/mcp's sandbox helper runs in a re-exec'd subprocess and ends in
# execve(2). execve replaces the process image, so Go never writes the coverage
# counters for anything the helper did, and `go test -cover` reports those
# functions at 0.0% no matter how thoroughly they are exercised. The ratchet
# cannot tell that apart from code nobody tested at all.
#
# Chasing the number was considered and rejected: crediting subprocess coverage
# means flushing counters from inside the confined helper and rewriting
# coverage-ratchet.sh to measure from merged profiles, which re-baselines every
# floor in scripts/coverage-floors.txt -- a file whose own rule is that floors
# may only be raised. That is a large, security-adjacent change for ~40
# statements in one package.
#
# So instead of hiding the blind spot behind a percentage, this NAMES it.
# scripts/subprocess-only-funcs.txt lists every function the gate cannot see
# beside the cross-process proof that does exercise it, and this script fails
# when:
#
#   1. a 0.0% function appears in the sandbox files and is NOT in the ledger
#      -- the case that matters: new helper code with nothing proving it;
#   2. a ledger entry is no longer 0.0% -- it became coverable, so the line is
#      now a lie and should be deleted;
#   3. a ledger entry names a function that no longer exists -- rot;
#   4. a ledger entry's named proof does not exist in the package -- a name
#      invented to quiet the gate is not a proof.
#
# WHY IT IS NOT A CI GATE (see scripts/gate-parity.sh's manifest). On a runner
# where bwrap cannot unshare, the sandbox tests skip and EVERY sandbox function
# reads 0.0%. The ledger comparison then has nothing to say, and a gate that
# passes by being irrelevant is worse than no gate. The same reasoning applies
# on a developer machine that cannot run the sandbox, which is why this reports
# NOT RUN there rather than failing: that is host policy, not a defect. The
# sentinels below are what tell the two apart.
# ------------------------------------------------------------------------------
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ledger="$repo_root/scripts/subprocess-only-funcs.txt"
module_dir="$repo_root/daemon"
pkg="./mcp/"

# The files whose 0.0% functions this gate governs: everything that can run
# inside the helper subprocess.
scoped_re='sandbox_landlock_linux\.go|sandbox_egress.*_linux\.go'

# SENTINELS: functions covered if and only if the cross-process sandbox probes
# really ran. If these are 0.0% the host could not exercise the sandbox at all,
# so every sandbox function reads 0.0% and the ledger cannot be judged. Without
# this check the gate would "pass" loudest exactly where it knows least.
sentinels='handle|connectOnBehalf|egressRefused'

if [ ! -f "$ledger" ]; then
  echo "subprocess-coverage-ledger: FAIL -- missing $ledger" >&2
  exit 1
fi

profile="$(mktemp)"
funcs="$(mktemp)"
trap 'rm -f "$profile" "$funcs"' EXIT

if ! (cd "$module_dir" && go test -coverprofile="$profile" -count=1 "$pkg" >/dev/null 2>&1); then
  echo "subprocess-coverage-ledger: FAIL -- the daemon/mcp tests did not pass, so coverage means nothing" >&2
  exit 1
fi
if ! (cd "$module_dir" && go tool cover -func="$profile" > "$funcs" 2>/dev/null); then
  echo "subprocess-coverage-ledger: FAIL -- could not read the coverage profile" >&2
  exit 1
fi

# $1 = path:line: $2 = function $3 = percent
covered_sentinels="$(awk -v s="$sentinels" '$3 != "0.0%" && $2 ~ "^("s")$" { n++ } END { print n+0 }' "$funcs")"
if [ "$covered_sentinels" -eq 0 ]; then
  echo "subprocess-coverage-ledger: NOT RUN -- the sandbox did not execute on this host"
  echo "  (none of the supervisor sentinels [$sentinels] is covered, so every sandbox"
  echo "   function reads 0.0% and the ledger cannot be judged. This is host policy,"
  echo "   not a defect: the same condition skips the sandbox tests themselves.)"
  exit 0
fi

# The measured set: "<file>:<function>" for every 0.0% function in scope.
measured="$(awk -v re="$scoped_re" '$3 == "0.0%" && $1 ~ re {
  split($1, p, "/"); split(p[length(p)], f, ":"); print f[1] ":" $2
}' "$funcs" | sort -u)"

# The ledger set, and the proof each entry claims.
declare -A proof_of=()
listed=""
while read -r entry proofs; do
  [ -z "$entry" ] && continue
  case "$entry" in \#*) continue ;; esac
  proof_of["$entry"]="$proofs"
  listed="$listed$entry"$'\n'
done < <(sed 's/#.*//' "$ledger" | awk 'NF { print $1, substr($0, index($0,$2)) }')
listed="$(printf '%s' "$listed" | sort -u)"

fail=0

# 1. A 0.0% function in scope that nobody wrote down.
while read -r m; do
  [ -z "$m" ] && continue
  if ! printf '%s\n' "$listed" | grep -qxF "$m"; then
    echo "subprocess-coverage-ledger: FAIL -- $m is 0.0% covered and is not in the ledger." >&2
    echo "  Either cover it, or add it to scripts/subprocess-only-funcs.txt with the" >&2
    echo "  cross-process test or probe that actually exercises it. A helper-only" >&2
    echo "  function with neither is untested code the coverage gate cannot see." >&2
    fail=1
  fi
done <<< "$measured"

# 2/3. A ledger entry that is covered now, or gone.
while read -r l; do
  [ -z "$l" ] && continue
  if printf '%s\n' "$measured" | grep -qxF "$l"; then
    continue
  fi
  file="${l%%:*}"
  fn="${l##*:}"
  if awk -v f="$file" -v n="$fn" '$1 ~ f":" && $2 == n { found=1 } END { exit !found }' "$funcs"; then
    echo "subprocess-coverage-ledger: FAIL -- $l is no longer 0.0% covered." >&2
    echo "  It is coverable now, so its ledger line claims a blind spot that is gone." >&2
    echo "  Delete the line from scripts/subprocess-only-funcs.txt." >&2
  else
    echo "subprocess-coverage-ledger: FAIL -- $l is in the ledger but no such function exists." >&2
    echo "  It was renamed or removed; update scripts/subprocess-only-funcs.txt." >&2
  fi
  fail=1
done <<< "$listed"

# 4. A proof that does not exist is not a proof.
for entry in "${!proof_of[@]}"; do
  IFS=',' read -ra names <<< "${proof_of[$entry]}"
  for raw in "${names[@]}"; do
    name="$(printf '%s' "$raw" | tr -d '[:space:]')"
    [ -z "$name" ] && continue
    if ! grep -rqE "(func|var) $name\b" "$module_dir/mcp/" 2>/dev/null; then
      echo "subprocess-coverage-ledger: FAIL -- $entry names the proof '$name', which does not exist in daemon/mcp." >&2
      echo "  A proof that cannot be found is not one. Name the real test, probe or nothing." >&2
      fail=1
    fi
  done
done

if [ "$fail" -ne 0 ]; then
  exit 1
fi

n="$(printf '%s' "$listed" | grep -c . || true)"
echo "subprocess-coverage-ledger: ok -- $n subprocess-only function(s), each with a named cross-process proof"
