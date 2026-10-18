#!/bin/sh
# build-archive.sh <target> <binaries-dir> <out-dir>
#
# Packs one release target into the archive the installers download:
# mochiii-<target>.tar.gz, or mochiii-<target>.zip for a win32-* target. Used by
# release.yml and by anyone rehearsing an install locally, so both build the same
# thing.
#
# <binaries-dir> holds what the release's `binaries` job builds for the target:
# mochiii-tui, mochiii-daemon and mochiii-embedder-helper (.exe on Windows).
#
# The archive holds, at its top level:
#   mochiii                  the terminal app (mochiii-tui, renamed: it is the one
#                            command people type, and it starts the other two)
#   mochiii-daemon           the background service
#   mochiii-embedder-helper  runs the code-search model
#   models.json              the terminal's agent-mode config -- models.agent.json,
#                            the one run-tui.sh has always given the terminal. The
#                            daemon loads the models.json beside it, and the client
#                            passes that path explicitly, never the project's.
#   LICENSE, THIRD_PARTY_LICENSES.txt
set -eu

[ $# -eq 3 ] || {
	echo "usage: build-archive.sh <target> <binaries-dir> <out-dir>" >&2
	exit 2
}
target=$1
bins=$2
out=$3
root=$(cd "$(dirname "$0")/.." && pwd)

exe=""
case "$target" in
win32-*) exe=".exe" ;;
esac

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
for pair in "mochiii-tui:mochiii" "mochiii-daemon:mochiii-daemon" "mochiii-embedder-helper:mochiii-embedder-helper"; do
	from="$bins/${pair%%:*}$exe"
	[ -f "$from" ] || {
		echo "build-archive.sh: no ${pair%%:*}$exe in $bins for $target" >&2
		exit 1
	}
	cp "$from" "$stage/${pair#*:}$exe"
	chmod 755 "$stage/${pair#*:}$exe"
done
cp "$root/models.agent.json" "$stage/models.json"
cp "$root/LICENSE" "$root/THIRD_PARTY_LICENSES.txt" "$stage/"

mkdir -p "$out"
out=$(cd "$out" && pwd)
case "$target" in
win32-*)
	(cd "$stage" && zip -q -X "$out/mochiii-$target.zip" "mochiii$exe" "mochiii-daemon$exe" "mochiii-embedder-helper$exe" models.json LICENSE THIRD_PARTY_LICENSES.txt)
	echo "$out/mochiii-$target.zip"
	;;
*)
	tar -C "$stage" -czf "$out/mochiii-$target.tar.gz" mochiii mochiii-daemon mochiii-embedder-helper models.json LICENSE THIRD_PARTY_LICENSES.txt
	echo "$out/mochiii-$target.tar.gz"
	;;
esac
