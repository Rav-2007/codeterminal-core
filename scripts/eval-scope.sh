#!/usr/bin/env bash
# eval-scope.sh -- does a change touch the code that decides what search finds?
#
# WHY. The locate eval is required on main (owner decision, 2026-11-20), but
# only where it measures something a change can be held to. Its corpus is this
# repository, so ANY change can move its score: a line added far above an
# answer moves every 40-line chunk window below it (PR #116 lost question 46
# that way, from one line in clients/tui/chat.go), and new code competes for
# the top ten (the six NET LOSS merges #77 #87 #100 #101 #106 #110). Blocking
# every feature on that would block work for something that is not a search
# regression. A change to search code is what the gate is for.
#
# So retrieval-eval.yml runs the eval as two roles of one job: the GATE
# (required) measures a change for which this prints `run`, and passes in
# seconds otherwise; the ADVISORY role measures every other pull request and
# lists what it cost, without blocking. The Monday run watches main for drift.
#
# THE LIST IS scripts/retrieval-paths.txt, and only there: the workflow kept
# its own copy in `push.paths` until 2026-11-20, and two hand-kept copies of
# one list drift (gate-parity.sh's whole subject). --self-test fails if the
# workflow grows a `paths:` list again, or if an entry names a file that no
# longer exists -- a rotted entry is a hole nobody would see.
#
# Usage:
#   eval-scope.sh <base> <head>   prints `run` or `skip`
#   eval-scope.sh --self-test
set -uo pipefail
cd "$(dirname "$0")/.."

LIST="${EVAL_SCOPE_LIST:-scripts/retrieval-paths.txt}"
WORKFLOW="${EVAL_SCOPE_WORKFLOW:-.github/workflows/retrieval-eval.yml}"

patterns() { sed 's/#.*//; s/[[:space:]]*$//; s/^[[:space:]]*//' "$LIST" | grep -v '^$'; }

# scope reads changed paths on stdin and prints run|skip. An exact entry
# matches that file; `dir/**` matches anything under dir/.
scope() {
	local -a pats
	mapfile -t pats < <(patterns)
	local f p
	while IFS= read -r f; do
		[ -z "$f" ] && continue
		for p in "${pats[@]}"; do
			case "$p" in
			*'/**') [[ "$f" == "${p%/\*\*}/"* ]] && { echo run; return; } ;;
			*) [[ "$f" == "$p" ]] && { echo run; return; } ;;
			esac
		done
	done
	echo skip
}

if [ "${1:-}" = "--self-test" ]; then
	fails=0
	expect() { # expect <want> <name> <paths...>
		local want=$1 name=$2 got
		shift 2
		got=$(printf '%s\n' "$@" | scope)
		[ "$got" = "$want" ] || { echo "self-test FAIL [$name]: got $got, want $want"; fails=$((fails + 1)); }
	}
	expect run listed-file daemon/chunkexpand.go
	expect run under-a-dir helper/internal/ort/session.go
	expect run among-others docs/README.md daemon/rerank.go clients/tui/chat.go
	expect run the-list-itself scripts/retrieval-paths.txt
	expect run the-workflow .github/workflows/retrieval-eval.yml
	expect skip a-line-above-an-answer clients/tui/chat.go
	expect skip docs-only docs/RELEASE_NOTES.md README.md
	expect skip a-prefix-is-not-a-dir helperx/main.go
	expect skip a-near-name daemon/chunkexpand_test.go
	expect skip nothing-changed

	# One list: the workflow must not carry its own.
	if grep -nE '^[[:space:]]+paths:' "$WORKFLOW" >/dev/null; then
		echo "self-test FAIL [one-list]: $WORKFLOW has a paths: filter again; the list lives in $LIST"
		fails=$((fails + 1))
	fi
	grep -q 'eval-scope.sh' "$WORKFLOW" || { echo "self-test FAIL [wired]: $WORKFLOW does not call eval-scope.sh"; fails=$((fails + 1)); }

	# No rotted entries.
	while IFS= read -r p; do
		case "$p" in
		*'/**') [ -d "${p%/\*\*}" ] || { echo "self-test FAIL [stale]: $LIST names ${p%/\*\*}/, which does not exist"; fails=$((fails + 1)); } ;;
		*) [ -e "$p" ] || { echo "self-test FAIL [stale]: $LIST names $p, which does not exist"; fails=$((fails + 1)); } ;;
		esac
	done < <(patterns)

	[ "$fails" -eq 0 ] || exit 1
	echo "eval-scope: self-test passed ($(patterns | wc -l | tr -d ' ') entries, one list, none stale)"
	exit 0
fi

[ $# -eq 2 ] || { echo "usage: $0 <base> <head> | --self-test" >&2; exit 2; }
git diff --name-only --no-renames "$1" "$2" | scope
