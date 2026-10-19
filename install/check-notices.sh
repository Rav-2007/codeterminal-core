#!/bin/sh
# check-notices.sh <THIRD_PARTY_LICENSES.txt> <binary>...
#
# Fails when a Go module linked into any of the binaries has no entry in the
# notices file. It is the rule clients/vscode/scripts/verify-vsix.js enforces on
# the .vsix packages, applied to every target's binaries, including the
# terminal-only ones no .vsix carries: their licences must ship with them too.
#
# A module counts as listed when its path appears in the file, or when a "Name:"
# line names it by its last path element -- the two forms the file uses, and
# the same two verify-vsix.js accepts. Needs `go` (go version -m reads a binary
# built for any platform).
set -eu

[ $# -ge 2 ] || {
	echo "usage: check-notices.sh <THIRD_PARTY_LICENSES.txt> <binary>..." >&2
	exit 2
}
notices=$1
shift
[ -f "$notices" ] || {
	echo "check-notices.sh: no notices file at $notices" >&2
	exit 2
}

missing=0
checked=0
for bin in "$@"; do
	deps=$(go version -m "$bin" | awk '$1 == "dep" && $2 !~ /^mochiii\// {print $2}') || {
		echo "check-notices.sh: cannot read the module list of $bin" >&2
		exit 2
	}
	for dep in $deps; do
		checked=$((checked + 1))
		base=${dep##*/}
		if grep -qiF "$dep" "$notices" || grep -qiE "^Name:[[:space:]]*$base([^[:alnum:]_.-]|\$)" "$notices"; then
			continue
		fi
		echo "UNLICENSED $bin links $dep, which $notices does not list. Add its entry (verbatim licence text)." >&2
		missing=$((missing + 1))
	done
done

[ "$checked" -gt 0 ] || {
	echo "check-notices.sh: the binaries named no modules at all, so nothing was checked" >&2
	exit 2
}
if [ "$missing" -gt 0 ]; then
	echo "check-notices.sh: $missing module(s) without a notice" >&2
	exit 1
fi
echo "check-notices.sh: every module in $# binaries has a notice ($checked checked)"
