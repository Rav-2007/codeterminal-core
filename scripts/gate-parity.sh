#!/usr/bin/env bash
#
# gate-parity.sh — `make check` and CI must not silently disagree about what is checked.
#
# WHY THIS EXISTS
#
# On 2026-09-09 four CI failures were diagnosed one at a time. Three of them
# (gofmt, crossvet, race) were ALREADY in `make check` and had simply not been
# run. The fourth was a gate that could not run at all. Underneath both sat a
# structural fact nobody had named: `make check` and .github/workflows/*.yml are
# TWO INDEPENDENTLY MAINTAINED ENUMERATIONS of what to check, and nothing
# compared them.
#
# That is R1.16's exact form -- "a gate whose expected set is derived from the
# list it validates" -- for the sixth time in this repository. debt-markers.sh
# ran only in CI with no make target, so it could not be run before pushing;
# supply-chain.sh the same; the coverage ratchet's vanished-floor sweep ran only
# locally, so it could not gate a merge. None of those were decisions. They were
# drift, and each was invisible from either side alone.
#
# WHY NOT JUST RUN `make check` IN CI
#
# Considered and rejected, and the reasons are worth keeping because "CI should
# run the same command" sounds obviously right:
#
#   - CI is a MATRIX. Six modules run in parallel with per-module attribution.
#     `make check` is serial and whole-repo, so running it per matrix job would
#     do everything six times and lose the attribution that makes a red job
#     readable.
#   - CI has runners a laptop does not: windows-latest, macos-latest, a docker
#     daemon, a VS Code Extension Development Host.
#   - `hookcheck` asserts the DEVELOPER's core.hooksPath. On a runner it is
#     meaningless -- it would be a check that passes by being irrelevant.
#   - Some exclusions are costed decisions: macOS bills at 10x, fuzzing takes
#     minutes, the retrieval eval downloads a model.
#
# So full parity is the wrong target. An ENUMERATED, CODE-STATED difference is
# acceptable; an undocumented one is not. This gate is the difference between
# the two.
#
# HOW IT AVOIDS BEING THE CLASS IT FIXES
#
# The two sides are DERIVED, never hand-listed:
#   local — `make -n check`, the actual dry-run expansion of the real target
#   ci    — every scripts/*.sh named in .github/workflows/
#
# The manifest below records only the EXPECTED RELATIONSHIP, and it is checked
# in both directions:
#
#   (a) a script that exists but has no manifest entry            -> RED
#   (b) a manifest entry naming a script that no longer exists    -> RED
#   (c) observed side != manifest side                            -> RED
#
# (b) is the anti-R1.16 property and the reason this is not just another list.
# Deleting a check from either side leaves its manifest entry pointing at
# nothing, or moves it to a side the manifest does not expect, and both are red.
# A gate that can only detect ADDITIONS is the exact defect being fixed here.

set -uo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# --- the manifest: expected state, with a reason for every asymmetry ----------
#
# STATES (M5 -- two states meaning different things never share a phrase):
#   both      in `make check` AND in a workflow
#   local     in `make check` only, deliberately
#   ci        in a workflow only, deliberately
#   manual    in NEITHER: a make target or tool run on purpose, not a gate
#   lib       in NEITHER because it is SOURCED, not executed. Distinct from
#             'manual' on purpose (M5): "nobody runs it as a gate" and "it is
#             not a program" are different facts and must not share a phrase.
#
# A blank reason is not allowed for local/ci/manual.
manifest() {
  cat <<'MANIFEST'
actions-pinned.sh|both|
coverage-ratchet.sh|both|
docs-claims.sh|both|
docs-coderefs.sh|both|
docs-links.sh|both|
errcheck-ceiling.sh|both|
go-toolchain-pinned.sh|both|
lint.sh|both|
release-signing-guard.sh|both|
debt-markers.sh|both|
supply-chain.sh|both|
govulncheck.sh|local|CI runs govulncheck inline per module instead. Deliberate and NOT a duplicate: the script's own header records why -- CI installs whatever 1.25.x resolves to and finds nothing, while the same commit scanned on a developer machine sitting exactly on the toolchain floor found TEN reachable stdlib vulnerabilities. The two scan different standard libraries and both answers are wanted.
fuzz.sh|ci|Excluded from `make check` on runtime: 30s per target is too slow for the gate people run before every push, and a gate they route around is worse than one that runs in CI. `make fuzz` exists for running it deliberately.
macos-sign-and-notarize.sh|ci|Release-only, and needs Apple credentials plus a macOS runner. Nothing a developer can run.
reach.sh|local|In `make check` and deliberately NOT in CI, and the reason is not cost. A CI runner's clone has no local branches, so checks 1 and 2 would examine nothing and pass by being irrelevant -- the exact failure hookcheck's exclusion names, and a gate that passes by being irrelevant is worse than no gate. The person who has an undelivered fix is at a terminal. It was `manual` for one commit while its ten findings were unresolved; each now carries an allowlist entry with a reason and a trigger, so it is green on facts rather than on silence.
soak.sh|manual|A 30-minute sustained-load run. `make soak` exists; it is a measurement, not a gate.
sigterm-drill.sh|manual|`make drill`. Drives real signals at a real daemon; deliberate, not per-push.
agent-cost-bench.sh|manual|Benchmark. Costs real model tokens.
latency-bench.sh|manual|Benchmark. Reports numbers, asserts nothing.
wire-drill.sh|manual|Diagnostic for the wire protocol, run by hand when something is wrong.
gate-parity.sh|both|
install-tools.sh|ci|Installs the pinned analysis tools. CI runs it; locally `make check` does not, because it would reinstall four binaries on every gate run. scripts/lint.sh CHECKS the versions instead and prints this script as the remedy.
toolpins.sh|lib|Sourced by lint.sh, errcheck-ceiling.sh and govulncheck.sh to read scripts/tool-pins.txt. Not executable as a gate and has no exit status of its own.
MANIFEST
}

# --- --what-ci-adds: the banner `make check` prints when it finishes ----------
#
# THE DURABLE ANSWER TO "IT WAS COVERED AND NOBODY RAN IT". Three of the four CI
# failures on 2026-09-09 were already in `make check`; the developer ran a
# subset. The other half of that problem is the opposite: finishing `make check`
# green and not knowing what it did NOT cover. A line at the moment it matters
# beats a document nobody opens -- the same reasoning as the platform-coverage
# banner, which exists because `go test` prints "ok" for files it never built.
#
# The SCRIPT half is derived from the manifest below, so a gate that moves to
# CI-only starts appearing here with no edit. The CAPABILITY half cannot be
# derived -- "this laptop has no Windows runner" is not written anywhere in the
# repo -- and is listed, with that limitation stated rather than hidden.
if [ "${1:-}" = "--what-ci-adds" ]; then
  echo ""
  echo "make check is green. CI STILL CHECKS THINGS THIS RUN DID NOT:"
  echo ""
  echo "  Steps and gates that run only in CI (derived from this script's manifest):"
  manifest | while IFS='|' read -r name want why; do
    [ "${want:-}" = "ci" ] || continue
    echo "    $name"
    echo "        ${why%%.*}."
  done
  echo ""
  echo "  Capabilities a developer machine does not have (NOT derived -- see above):"
  echo "    real Windows execution    cross (windows-latest) BUILDS, VETS and RUNS the tests."
  echo "                              \`make crossvet\` only COMPILES. Every runtime"
  echo "                              platform defect is invisible here, by construction."
  echo "    real macOS execution      macos-latest, and only on main."
  echo "    the VS Code extension     tsc + a real Extension Development Host."
  echo "    the proxy container       docker build."
  echo "    the 2000-turn soak        run unraced at full length in CI; the local"
  echo "                              gate runs the reduced raced version."
  echo ""
  echo "  Nothing above is a reason not to push. It is what a green local run does"
  echo "  NOT promise, stated where it is cheap to act on."
  exit 0
fi

# --- derive the two sides, never hand-list them -------------------------------

local_set="$(make -n check 2>/dev/null | grep -oE "scripts/[a-z0-9-]+\.sh" | sed 's|scripts/||' | sort -u)"
ci_set="$(grep -rhoE "scripts/[a-z0-9-]+\.sh" .github/workflows/ 2>/dev/null | sed 's|scripts/||' | sort -u)"
all_set="$(ls scripts/*.sh 2>/dev/null | sed 's|scripts/||' | sort -u)"

# VACUITY FLOORS. Each of these has a failure mode that would otherwise report
# perfect agreement: `make -n` failing leaves local_set empty and every `both`
# entry then reads as "moved to ci"; a renamed workflow directory does the same
# from the other side. An empty comparison is not a passing comparison.
fail=0
if [ -z "$local_set" ]; then
  echo "gate-parity: \`make -n check\` produced no script invocations -- refusing to compare nothing." >&2
  echo "             This is a broken derivation, not agreement." >&2
  exit 2
fi
if [ -z "$ci_set" ]; then
  echo "gate-parity: no scripts/*.sh found in .github/workflows/ -- refusing to compare nothing." >&2
  exit 2
fi
if [ -z "$all_set" ]; then
  echo "gate-parity: scripts/ contains no .sh files -- refusing to compare nothing." >&2
  exit 2
fi

in_set() { printf '%s\n' "$2" | grep -qxF "$1"; }

# --- (a) every script that exists must be accounted for -----------------------

declare -A expect reason
while IFS='|' read -r name want why; do
  [ -z "${name:-}" ] && continue
  expect["$name"]="$want"
  reason["$name"]="$why"
done < <(manifest)

for s in $all_set; do
  if [ -z "${expect[$s]:-}" ]; then
    echo "FAIL  $s exists in scripts/ but has no entry in gate-parity.sh's manifest." >&2
    echo "      Add one: both | local <reason> | ci <reason> | manual <reason>." >&2
    echo "      A new check that nobody decided the placement of is how the two sides drifted." >&2
    fail=1
  fi
done

# --- (b) THE ANTI-R1.16 CHECK: a manifest entry for a script that is gone -----

for s in "${!expect[@]}"; do
  if ! in_set "$s" "$all_set"; then
    echo "FAIL  the manifest lists $s, which no longer exists in scripts/." >&2
    echo "      Either the script was deleted and its entry was not, or it was renamed." >&2
    echo "      This is the check that makes this gate able to see a DELETION -- a list that" >&2
    echo "      only detects additions is the defect this file was written to fix." >&2
    fail=1
  fi
done

# --- (c) observed side must equal the declared side ---------------------------

for s in $all_set; do
  want="${expect[$s]:-}"
  [ -z "$want" ] && continue

  in_local=no; in_ci=no
  in_set "$s" "$local_set" && in_local=yes
  in_set "$s" "$ci_set" && in_ci=yes

  case "$in_local/$in_ci" in
    yes/yes) got=both ;;
    yes/no)  got=local ;;
    no/yes)  got=ci ;;
    no/no)   got=manual; [ "$want" = "lib" ] && got=lib ;;
  esac

  if [ "$got" != "$want" ]; then
    echo "FAIL  $s is declared '$want' but is actually '$got'" >&2
    echo "        make check: $in_local        workflows: $in_ci" >&2
    case "$want/$got" in
      both/ci)     echo "      It left \`make check\`. A developer can no longer run it before pushing." >&2 ;;
      both/local)  echo "      It left CI. It can no longer gate a merge." >&2 ;;
      both/manual) echo "      It left BOTH sides and is now running nowhere." >&2 ;;
      */both)      echo "      It gained a side. If that is intended, change the manifest to 'both'." >&2 ;;
      *)           echo "      Update the manifest if this is intended, and say why." >&2 ;;
    esac
    fail=1
  fi

  # A reason is mandatory for every asymmetry. "It is ci-only" without a reason
  # is the undocumented difference this gate exists to prevent.
  if [ "$want" != "both" ] && [ -z "${reason[$s]:-}" ]; then
    echo "FAIL  $s is declared '$want' with no reason. An asymmetry without a stated reason is drift." >&2
    fail=1
  fi
done

if [ $fail -ne 0 ]; then
  echo "" >&2
  echo "gate-parity: FAILED. \`make check\` and CI disagree about what is checked." >&2
  exit 1
fi

n_all=$(printf '%s\n' "$all_set" | grep -c .)
n_both=0; n_local=0; n_ci=0; n_manual=0
for s in $all_set; do
  case "${expect[$s]}" in
    both) n_both=$((n_both+1)) ;; local) n_local=$((n_local+1)) ;;
    ci) n_ci=$((n_ci+1)) ;; manual|lib) n_manual=$((n_manual+1)) ;;
  esac
done
echo "gate-parity: $n_all script(s) accounted for -- $n_both both, $n_local local-only, $n_ci CI-only, $n_manual manual (each asymmetry carries a reason)"
