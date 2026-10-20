#!/bin/sh
# build-archive.sh <target> <binaries-dir> <out-dir>
#
# Packs one release target into the archive the installers download:
# mochiii-<target>.tar.gz, or mochiii-<target>.zip for a win32-* target. Used by
# release.yml and by anyone rehearsing an install locally, so both build the same
# thing.
#
# <binaries-dir> holds what the release's `binaries` job builds for the target:
# mochiii (.exe on Windows) -- since 1.2.0 the one binary that is the terminal
# app, the background service and the code-search helper, with its default
# models.json built in.
#
# The archive holds, at its top level:
#   mochiii                  the program
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
from="$bins/mochiii$exe"
[ -f "$from" ] || {
	echo "build-archive.sh: no mochiii$exe in $bins for $target" >&2
	exit 1
}
cp "$from" "$stage/mochiii$exe"
chmod 755 "$stage/mochiii$exe"
cp "$root/LICENSE" "$root/THIRD_PARTY_LICENSES.txt" "$stage/"

mkdir -p "$out"
out=$(cd "$out" && pwd)
case "$target" in
win32-*)
	(cd "$stage" && zip -q -X "$out/mochiii-$target.zip" "mochiii$exe" LICENSE THIRD_PARTY_LICENSES.txt)
	echo "$out/mochiii-$target.zip"
	;;
*)
	tar -C "$stage" -czf "$out/mochiii-$target.tar.gz" mochiii LICENSE THIRD_PARTY_LICENSES.txt
	echo "$out/mochiii-$target.tar.gz"
	;;
esac
