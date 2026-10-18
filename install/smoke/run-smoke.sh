#!/bin/sh
# run-smoke.sh <dist-dir> <version>
#
# The install smoke test for Linux and macOS: everything a user does after
# pasting the one-line install command, against the release files in <dist-dir>
# (served on 127.0.0.1, so the real installer runs unmodified). In an isolated
# HOME, it checks that:
#
#   1. the installer installs, and all three programs report <version>;
#   2. the code-search model downloads (real network, pinned checksums);
#   3. `mochiii-daemon index` indexes a small project -- the embedder helper and
#      onnxruntime really run on this OS;
#   4. `mochiii --prompt` starts its own background service and answers through a
#      stand-in provider, and the project's code reached the request;
#   5. a tampered archive is refused and the installed version keeps working;
#   6. re-installing over a running service stops it and works;
#   7. `mochiii --stop` ends the service and its embedder helper, and
#      --uninstall removes the programs and keeps ~/.mochiii.
#
# Exit status 0 only if every check passed.
set -eu

[ $# -eq 2 ] || {
	echo "usage: run-smoke.sh <dist-dir> <version>" >&2
	exit 2
}
dist=$(cd "$1" && pwd)
want=$2
here=$(cd "$(dirname "$0")" && pwd)
installer="$here/../install.sh"
python=$(command -v python3 || command -v python)

# Short on purpose: a Unix socket path must fit in about 100 bytes.
work=$(mktemp -d /tmp/msmk.XXXXXX)
# HOME is reached through a symlink, as every home under /tmp is on macOS (/tmp
# is /private/tmp). The programs then run under a path spelled differently from
# $HOME, which the installer has to recognise to stop them.
mkdir -p "$work/home"
ln -s home "$work/h"
export HOME="$work/h"
export XDG_RUNTIME_DIR="$work/r"
mkdir -p "$HOME" "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"
unset XDG_STATE_HOME XDG_CONFIG_HOME XDG_CACHE_HOME MOCHIII_API_KEY MOCHIII_HOME
export PATH="$HOME/.local/bin:$PATH"
export MOCHIII_NO_VSCODE=1 NO_COLOR=1

pids=""
cleanup() {
	"$HOME/.mochiii/bin/mochiii" --workspace "$work/project" --stop >/dev/null 2>&1 || true
	# shellcheck disable=SC2086 # one pid per word
	[ -z "$pids" ] || kill $pids 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

fail() {
	echo "SMOKE FAILED: $*" >&2
	exit 1
}
pass() { echo "ok - $*"; }

serve() { # serve <dir> <port>
	(cd "$1" && exec "$python" -m http.server "$2" --bind 127.0.0.1) >"$work/serve-$2.log" 2>&1 &
	pids="$pids $!"
	i=0
	until curl -fsS "http://127.0.0.1:$2/" >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt 50 ] || fail "the file server on port $2 did not start"
		sleep 0.2
	done
}

serve "$dist" 18471
export MOCHIII_DOWNLOAD_BASE=http://127.0.0.1:18471

# 1. install
sh "$installer" || fail "the installer failed"
for bin in mochiii mochiii-daemon mochiii-embedder-helper; do
	got=$("$HOME/.mochiii/bin/$bin" --version) || fail "$bin --version failed"
	[ "$got" = "$bin $want" ] || fail "$bin --version said '$got', want '$bin $want'"
done
[ "$(command -v mochiii)" = "$HOME/.local/bin/mochiii" ] || fail "mochiii is not on PATH at ~/.local/bin"
pass "installed $want; all three programs report it"

# running <program> prints the process IDs of <program> started from the
# install, matched by full path in both spellings of its directory.
realbin=$(cd "$HOME/.mochiii/bin" && pwd -P)
running() {
	for dir in "$HOME/.mochiii/bin" "$realbin"; do
		pgrep -f "^$dir/$1( |\$)" 2>/dev/null || true
	done | sort -u
}

# 2. the model (the installer already fetched it; --check must agree)
check=$("$HOME/.mochiii/bin/mochiii-daemon" download-model --check | tail -n 1) || true
case "$check" in
*'"cached":true'*) pass "code-search model downloaded and verified" ;;
*) fail "the model is not in place after install: $check" ;;
esac

# 3. index a project
mkdir -p "$work/project"
cp "$here/fixture/quokka.go" "$work/project/"
"$HOME/.mochiii/bin/mochiii-daemon" index "$work/project" || fail "indexing the fixture project failed"
[ -f "$work/project/.mochiii/index/embedder_stamp.json" ] || fail "the index finished without its stamp"
pass "indexed a project: the embedder helper and onnxruntime run here"

# 4. the whole chain: mochiii starts its daemon, retrieval, an answer
"$python" "$here/stub_provider.py" 18472 "$work/requests.jsonl" 2>"$work/stub.log" &
pids="$pids $!"
sleep 1
out=$(MOCHIII_API_BASE=http://127.0.0.1:18472/v1 mochiii --workspace "$work/project" \
	--prompt "where is the burrow survey folded into one number?" 2>"$work/client.err") ||
	fail "mochiii --prompt failed: $(cat "$work/client.err")"
case "$out" in
*SMOKE-ANSWER-OK*) ;;
*) fail "the answer did not come back; got: $out $(cat "$work/client.err")" ;;
esac
grep -q quokkaChecksum "$work/requests.jsonl" || fail "the project's code never reached the provider: code search did not run"
pass "mochiii started its own background service, searched the project and answered"

# 5. a tampered download is refused, and the installed version keeps working
mkdir -p "$work/bad"
cp "$dist"/* "$work/bad/"
for a in "$work"/bad/mochiii-*.tar.gz; do printf 'x' >>"$a"; done
serve "$work/bad" 18473
if MOCHIII_DOWNLOAD_BASE=http://127.0.0.1:18473 sh "$installer" --no-model >"$work/bad.log" 2>&1; then
	fail "the installer accepted a tampered archive"
fi
grep -q "checksum mismatch" "$work/bad.log" || fail "the tampered archive was refused, but not for its checksum: $(cat "$work/bad.log")"
[ "$("$HOME/.mochiii/bin/mochiii" --version)" = "mochiii $want" ] || fail "a refused install broke the installed version"
pass "a tampered archive was refused and the installed version still runs"

# 6. re-install while the background service is running. The installer must stop
#    it, so the next mochiii starts the new version; an old one left running
#    would still answer, which is why its process is checked, not the answer.
before=$(running mochiii-daemon)
[ -n "$before" ] || fail "no background service was running before re-installing, so this would prove nothing"
sh "$installer" --no-model || fail "re-installing over a running background service failed"
now=" $(running mochiii-daemon | tr '\n' ' ') "
for pid in $before; do
	case "$now" in *" $pid "*) fail "re-installing left the old background service (process $pid) running" ;; esac
done
out=$(MOCHIII_API_BASE=http://127.0.0.1:18472/v1 mochiii --workspace "$work/project" --prompt "again?" 2>/dev/null) ||
	fail "mochiii did not work after re-installing"
case "$out" in *SMOKE-ANSWER-OK*) ;; *) fail "no answer after re-installing: $out" ;; esac
pass "re-installed over a running background service: the old one was stopped, and the new one answers"

# 7. stop, then uninstall. The embedder helper must end with its background
#    service rather than linger (here by SIGTERM; on Windows every stop is
#    TerminateProcess, see daemon/helperlifetime_windows.go).
[ -n "$(running mochiii-embedder-helper)" ] ||
	fail "no embedder helper was running before --stop, so the check after it would prove nothing"
mochiii --workspace "$work/project" --stop >/dev/null 2>&1 || fail "mochiii --stop failed"
i=0
while [ -n "$(running mochiii-embedder-helper)" ]; do
	i=$((i + 1))
	[ $i -le 10 ] || fail "mochiii --stop left the embedder helper running (process $(running mochiii-embedder-helper))"
	sleep 1
done
[ -z "$(running mochiii-daemon)" ] || fail "mochiii --stop left the background service running"
pass "mochiii --stop ended the background service and its embedder helper"
sh "$installer" --uninstall || fail "--uninstall failed"
[ ! -e "$HOME/.mochiii/bin" ] || fail "--uninstall left ~/.mochiii/bin"
[ ! -e "$HOME/.local/bin/mochiii" ] || fail "--uninstall left the mochiii link"
[ -d "$HOME/.mochiii/models" ] || fail "--uninstall removed the downloaded models, which it must keep"
pass "uninstalled; models and settings kept"

echo "SMOKE PASSED ($want on $(uname -s) $(uname -m))"
