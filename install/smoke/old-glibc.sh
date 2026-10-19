#!/bin/sh
# old-glibc.sh <dist-dir> <target>
#
# Code search on old Linux systems, measured rather than claimed. The embedder
# helper stopped being cgo on 2026-10-19 (helper/internal/ort), so it no longer
# carries the glibc of the machine that built it (2.34); what it needs is what
# onnxruntime itself needs, GLIBC_2.27. This runs the release's own programs in
# containers of older distributions:
#
#   ubuntu:18.04  glibc 2.27  `index` must embed a project: the floor.
#   centos:7      glibc 2.17  every program must start (`--version`), which
#                             shows the helper itself needs no newer glibc.
#
# Needs docker (GitHub's Linux runners have it). Exit status 0 only if every
# check passed.
set -eu

[ $# -eq 2 ] || {
	echo "usage: old-glibc.sh <dist-dir> <target>" >&2
	exit 2
}
dist=$(cd "$1" && pwd)
target=$2
here=$(cd "$(dirname "$0")" && pwd)

work=$(mktemp -d /tmp/mglc.XXXXXX)
trap 'rm -rf "$work"' EXIT
fail() {
	echo "OLD-GLIBC FAILED: $*" >&2
	exit 1
}
pass() { echo "ok - $*"; }

mkdir -p "$work/bin" "$work/home" "$work/r" "$work/project"
chmod 700 "$work/r"
tar -xzf "$dist/mochiii-$target.tar.gz" -C "$work/bin"
cp "$here/fixture/quokka.go" "$work/project/"

# The model is fetched once, on the runner, by the static daemon.
HOME="$work/home" "$work/bin/mochiii-daemon" download-model >/dev/null 2>&1 ||
	fail "downloading the code-search model on the runner failed"

# inside <image> <command...> runs a command in a container that sees the
# install, the model cache and the project at the same paths, as this runner's
# user, so every directory is owned by the uid that uses it. (Not `in`: that is
# a reserved word, and dash refuses the function -- found on the first CI run.)
inside() {
	image=$1
	shift
	docker run --rm --user "$(id -u):$(id -g)" -v "$work:$work" \
		-e HOME="$work/home" -e XDG_RUNTIME_DIR="$work/r" -e NO_COLOR=1 \
		"$image" "$@"
}

glibc_of() { inside "$1" ldd --version 2>&1 | head -n 1; }

for image in ubuntu:18.04 centos:7; do
	echo "== $image: $(glibc_of "$image")"
	for bin in mochiii mochiii-daemon mochiii-embedder-helper; do
		inside "$image" "$work/bin/$bin" --version >/dev/null 2>"$work/err" ||
			fail "$bin does not start on $image: $(cat "$work/err")"
	done
	pass "all three programs start on $image"
done

rm -rf "$work/project/.mochiii"
inside ubuntu:18.04 "$work/bin/mochiii-daemon" index "$work/project" >"$work/index.log" 2>&1 ||
	fail "indexing on ubuntu:18.04 (glibc 2.27) failed: $(tail -n 20 "$work/index.log")"
[ -f "$work/project/.mochiii/index/embedder_stamp.json" ] ||
	fail "indexing on ubuntu:18.04 finished without its stamp: $(tail -n 20 "$work/index.log")"
pass "code search runs on ubuntu:18.04 (glibc 2.27): the helper and onnxruntime embedded the project"
