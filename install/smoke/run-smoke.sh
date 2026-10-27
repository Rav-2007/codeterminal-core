#!/bin/sh
# run-smoke.sh <dist-dir> <version>
#
# The install smoke test for Linux and macOS: everything a user does after
# pasting the one-line install command, against the release files in <dist-dir>
# (served on 127.0.0.1, so the real installer runs unmodified). In an isolated
# HOME, it checks that:
#
#   1. the previous release (1.1.0, from GitHub) installs and runs, as a user's
#      machine has it today;
#   2. this release installs OVER it while its background service runs: the old
#      service is stopped, the old programs are gone, and the one binary's three
#      roles (client, daemon, helper) report <version>; and it installs the
#      terminal app only, never running VS Code's `code` though one is on PATH;
#   3. the code-search model downloads (real network, pinned checksums);
#   4. `mochiii index` indexes a small project -- the embedder helper and
#      onnxruntime really run on this OS;
#   5. `mochiii --prompt` starts its own background service and answers through a
#      stand-in provider, and the project's code reached the request;
#   6. a tampered archive is refused and the installed version keeps working;
#   7. re-installing over a running service stops it and works;
#   8. `mochiii --stop` ends the service and its embedder helper, and
#      --uninstall removes the program and keeps ~/.mochiii;
#   9. with ~/.local/bin not on PATH, as on a Mac, the installer ends on the one
#      line the terminal it ran in needs, and a startup file or a ~/.local it
#      cannot change does not stop it part-way (path-cases.sh).
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

# The release users have before this one. Installed from GitHub, as they did.
previous=1.1.0
previous_installer="https://github.com/Rav-2007/codeterminal-core/releases/download/v$previous/install.sh"

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
# 1.1.0's installer adds the VS Code extension unless told not to.
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
	"$python" "$here/serve.py" "$1" "$2" >"$work/serve-$2.log" 2>&1 &
	pids="$pids $!"
	i=0
	until curl -fsS "http://127.0.0.1:$2/" >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt 50 ] || fail "the file server on port $2 did not start"
		sleep 0.2
	done
}

# running <program and role> prints the process IDs started from the install as
# that program -- "mochiii daemon", "mochiii __embedder-helper", or 1.1.0's
# "mochiii-daemon" -- matched by full path in both spellings of its directory.
running() {
	realbin=$(cd "$HOME/.mochiii/bin" 2>/dev/null && pwd -P) || realbin="$HOME/.mochiii/bin"
	for dir in "$HOME/.mochiii/bin" "$realbin"; do
		pgrep -f "^$dir/$1( |\$)" 2>/dev/null || true
	done | sort -u
}

ask() { # ask <question>: one answer through the stand-in provider
	MOCHIII_API_BASE=http://127.0.0.1:18472/v1 mochiii --workspace "$work/project" --prompt "$1" 2>"$work/client.err"
}

mkdir -p "$work/project"
cp "$here/fixture/quokka.go" "$work/project/"
"$python" "$here/stub_provider.py" 18472 "$work/requests.jsonl" 2>"$work/stub.log" &
pids="$pids $!"
serve "$dist" 18471

# 1. the previous release, from GitHub, running
curl -fsSL "$previous_installer" | env -u MOCHIII_DOWNLOAD_BASE MOCHIII_VERSION="$previous" sh -s -- --no-model >"$work/previous.log" 2>&1 ||
	fail "installing $previous from GitHub failed: $(tail -n 20 "$work/previous.log")"
[ "$("$HOME/.mochiii/bin/mochiii" --version)" = "mochiii $previous" ] || fail "the $previous install does not report $previous"
out=$(ask "hello from $previous") || fail "$previous did not answer: $(cat "$work/client.err")"
case "$out" in *SMOKE-ANSWER-OK*) ;; *) fail "$previous did not answer: $out" ;; esac
old_service=$(running mochiii-daemon)
[ -n "$old_service" ] || fail "no $previous background service was running, so the upgrade would prove nothing"
pass "$previous installed from GitHub, and its background service is running"

# 2. this release, over it. With a `code` on PATH and MOCHIII_NO_VSCODE empty, an
# installer that still added the VS Code extension would be seen doing it.
mkdir -p "$work/fakebin"
printf '#!/bin/sh\necho "$*" >>"%s"\n' "$work/code-calls" >"$work/fakebin/code"
chmod +x "$work/fakebin/code"
export MOCHIII_DOWNLOAD_BASE=http://127.0.0.1:18471
MOCHIII_NO_VSCODE='' PATH="$work/fakebin:$PATH" sh "$installer" || fail "the installer failed"
[ ! -e "$work/code-calls" ] || fail "the installer ran \`code $(cat "$work/code-calls")\`; it must install the terminal app only"
for pid in $old_service; do
	if kill -0 "$pid" 2>/dev/null; then fail "upgrading left the $previous background service (process $pid) running"; fi
done
for gone in mochiii-daemon mochiii-embedder-helper models.json; do
	[ ! -e "$HOME/.mochiii/bin/$gone" ] || fail "upgrading left $previous's $gone in ~/.mochiii/bin"
done
for role in "mochiii" "mochiii-daemon:daemon" "mochiii-embedder-helper:__embedder-helper"; do
	name=${role%%:*}
	arg=${role#*:}
	[ "$arg" = "$role" ] && arg=""
	# shellcheck disable=SC2086 # no role word for the client: an empty $arg is no argument
	got=$("$HOME/.mochiii/bin/mochiii" $arg --version) || fail "mochiii $arg --version failed"
	[ "$got" = "$name $want" ] || fail "mochiii $arg --version said '$got', want '$name $want'"
done
[ "$(command -v mochiii)" = "$HOME/.local/bin/mochiii" ] || fail "mochiii is not on PATH at ~/.local/bin"
pass "upgraded $previous to $want while it ran: its service stopped, its programs gone, and all three roles report $want"

# 3. the model (the installer fetched it; --check must agree)
check=$(mochiii download-model --check | tail -n 1) || true
case "$check" in
*'"cached":true'*) pass "code-search model downloaded and verified" ;;
*) fail "the model is not in place after install: $check" ;;
esac

# 4. index a project
mochiii index "$work/project" || fail "indexing the fixture project failed"
[ -f "$work/project/.mochiii/index/embedder_stamp.json" ] || fail "the index finished without its stamp"
pass "indexed a project: the embedder helper and onnxruntime run here"

# 5. the whole chain: mochiii starts its daemon, retrieval, an answer
: >"$work/requests.jsonl"
out=$(ask "where is the burrow survey folded into one number?") ||
	fail "mochiii --prompt failed: $(cat "$work/client.err")"
case "$out" in
*SMOKE-ANSWER-OK*) ;;
*) fail "the answer did not come back; got: $out $(cat "$work/client.err")" ;;
esac
grep -q quokkaChecksum "$work/requests.jsonl" || fail "the project's code never reached the provider: code search did not run"
[ -n "$(running "mochiii daemon")" ] || fail "the answer came, but not from a 'mochiii daemon' started from the install"
pass "mochiii started itself as its background service, searched the project and answered"

# 6. a tampered download is refused, and the installed version keeps working
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

# 7. re-install while the background service is running. The installer must stop
#    it, so the next mochiii starts the new version; an old one left running
#    would still answer, which is why its process is checked, not the answer.
before=$(running "mochiii daemon")
[ -n "$before" ] || fail "no background service was running before re-installing, so this would prove nothing"
sh "$installer" --no-model || fail "re-installing over a running background service failed"
now=" $(running "mochiii daemon" | tr '\n' ' ') "
for pid in $before; do
	case "$now" in *" $pid "*) fail "re-installing left the old background service (process $pid) running" ;; esac
done
out=$(ask "again?") || fail "mochiii did not work after re-installing"
case "$out" in *SMOKE-ANSWER-OK*) ;; *) fail "no answer after re-installing: $out" ;; esac
pass "re-installed over a running background service: the old one was stopped, and the new one answers"

# 8. stop, then uninstall. The embedder helper must end with its background
#    service rather than linger (here by SIGTERM; on Windows every stop is
#    TerminateProcess, see daemon/helperlifetime_windows.go).
[ -n "$(running "mochiii __embedder-helper")" ] ||
	fail "no embedder helper was running before --stop, so the check after it would prove nothing"
mochiii --workspace "$work/project" --stop >/dev/null 2>&1 || fail "mochiii --stop failed"
i=0
while [ -n "$(running "mochiii __embedder-helper")" ]; do
	i=$((i + 1))
	[ $i -le 10 ] || fail "mochiii --stop left the embedder helper running (process $(running "mochiii __embedder-helper"))"
	sleep 1
done
[ -z "$(running "mochiii daemon")" ] || fail "mochiii --stop left the background service running"
pass "mochiii --stop ended the background service and its embedder helper"
sh "$installer" --uninstall || fail "--uninstall failed"
[ ! -e "$HOME/.mochiii/bin" ] || fail "--uninstall left ~/.mochiii/bin"
[ ! -e "$HOME/.local/bin/mochiii" ] || fail "--uninstall left the mochiii link"
[ -d "$HOME/.mochiii/models" ] || fail "--uninstall removed the downloaded models, which it must keep"
pass "uninstalled; models and settings kept"

# 9. PATH, in the cases users have hit, each in a home of its own
sh "$here/path-cases.sh" "$installer" http://127.0.0.1:18471 "$want" || fail "the installer's PATH cases above"
pass "PATH: the installer ends on the line this terminal needs, and an unwritable startup file or ~/.local does not stop it"

echo "SMOKE PASSED ($want on $(uname -s) $(uname -m))"
