#!/bin/sh
# alpine.sh <dist-dir> <npm-dir> <version>
#
# Mochiii on musl Linux, measured in Alpine containers on this runner (1.2.0).
# The regular Linux build needs glibc's dynamic loader to start, so a musl
# system gets the static -musl build, which has no code search. Both ways in:
#
#   alpine:3.20     the one-line installer, against this run's archives served
#                   from the runner. It must choose the -musl build and skip the
#                   model; every role reports <version>; `mochiii index` says
#                   code search isn't in this build; `mochiii` starts its
#                   background service and answers through the stand-in
#                   provider; `mochiii --stop` ends it.
#   node:20-alpine  npm-smoke.js, unchanged: npm must install the
#                   @mochiii/<arch>-musl package (its libc field), and the same
#                   install, answer and stop checks run through npm.
#
# Each container adds curl and python3 (for the stand-in provider) with apk,
# then runs the checks as a user with this runner's uid, so the files it writes
# here are the runner's. Needs docker (GitHub's Linux runners have it). Exit
# status 0 only if every check passed.
set -eu

[ $# -eq 3 ] || {
	echo "usage: alpine.sh <dist-dir> <npm-dir> <version>" >&2
	exit 2
}
dist=$(cd "$1" && pwd)
npmdir=$(cd "$2" && pwd)
want=$3
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)

work=$(mktemp -d /tmp/malp.XXXXXX)
chmod 755 "$work"
python3 "$here/serve.py" "$dist" 18481 >"$work/serve.log" 2>&1 &
served=$!
trap 'kill "$served" 2>/dev/null || true; rm -rf "$work"' EXIT
fail() {
	echo "ALPINE FAILED: $*" >&2
	exit 1
}
pass() { echo "ok - $*"; }

# as_user <image> <script> runs the shell script <script> in <image> as a user
# with this runner's uid (node:20-alpine's own `node` user when the uid is
# taken), after apk has added what the checks need. The container shares this
# runner's network, to reach the archives served on 127.0.0.1.
as_user() {
	docker run --rm --network host -e RUNNER_UID="$(id -u)" \
		-v "$work:$work" -v "$repo:$repo:ro" -v "$dist:$dist:ro" -v "$npmdir:$npmdir:ro" \
		"$1" sh -c '
			set -e
			apk add --no-cache curl python3 >/dev/null
			user=$(getent passwd "$RUNNER_UID" | cut -d: -f1)
			if [ -z "$user" ]; then
				adduser -D -u "$RUNNER_UID" smoke
				user=smoke
			fi
			exec su -s /bin/sh "$user" -c "sh $0"' "$2"
}

# 1. The one-line installer on alpine:3.20.
cat >"$work/installer.sh" <<EOF
set -eu
export HOME=$work/home XDG_RUNTIME_DIR=$work/r NO_COLOR=1
mkdir -p "\$HOME" $work/project
mkdir -m 700 \$XDG_RUNTIME_DIR
cp $here/fixture/quokka.go $work/project/
fail() { echo "ALPINE FAILED: \$*"; exit 1; }
pass() { echo "ok - \$*"; }

MOCHIII_DOWNLOAD_BASE=http://127.0.0.1:18481 sh $dist/install.sh --no-vscode >$work/install.log 2>&1 ||
	fail "the installer failed: \$(tail -n 20 $work/install.log)"
grep -q musl $work/install.log || fail "the installer did not say it chose the musl build: \$(cat $work/install.log)"
! grep -q 'Downloading the code-search model' $work/install.log || fail "the installer downloaded a model the musl build cannot use"
pass "install.sh chose the musl build and skipped the model"

m=\$HOME/.mochiii/bin/mochiii
for role in "" daemon __embedder-helper; do
	case "\$role" in "") name=mochiii ;; daemon) name=mochiii-daemon ;; *) name=mochiii-embedder-helper ;; esac
	got=\$("\$m" \$role --version 2>&1) || fail "mochiii \$role does not start: \$got"
	[ "\$got" = "\$name $want" ] || fail "mochiii \$role --version said '\$got', want '\$name $want'"
done
pass "all three roles start and report $want"

if out=\$("\$m" index $work/project 2>&1); then
	fail "mochiii index succeeded in a build without code search: \$out"
fi
case "\$out" in *"code search isn't in this build"*) ;; *) fail "mochiii index did not say why: \$out" ;; esac
pass "mochiii index says code search isn't in this build"

python3 $here/stub_provider.py 18482 $work/requests.jsonl >$work/stub.log 2>&1 &
stub=\$!
answered=
for _ in 1 2 3 4 5 6 7 8 9 10; do
	out=\$(MOCHIII_API_BASE=http://127.0.0.1:18482/v1 "\$m" --workspace $work/project --prompt "hello from alpine" 2>&1) || true
	case "\$out" in *SMOKE-ANSWER-OK*) answered=1; break ;; esac
	sleep 1
done
[ -n "\$answered" ] || { kill \$stub; fail "no answer on alpine: \$out"; }
pass "mochiii started its background service on alpine and answered"

"\$m" --workspace $work/project --stop >$work/stop.log 2>&1 || { kill \$stub; fail "mochiii --stop failed: \$(cat $work/stop.log)"; }
kill \$stub
pass "mochiii --stop ended the background service"
EOF
echo "== alpine:3.20, install.sh"
as_user alpine:3.20 "$work/installer.sh" || fail "the installer checks on alpine:3.20 (above)"

# 2. npm on node:20-alpine, by the same script every other runner runs.
cat >"$work/npm.sh" <<EOF
set -eu
export HOME=$work/npm-home
mkdir -p "\$HOME"
node $here/npm-smoke.js $npmdir $want
EOF
echo "== node:20-alpine, npm install -g mochiii"
as_user node:20-alpine "$work/npm.sh" || fail "the npm checks on node:20-alpine (above)"

echo "ALPINE PASSED ($want, $(uname -m))"
