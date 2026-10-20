#!/usr/bin/env bash
#
# run-tui.sh — build and launch Mochiii's terminal client against a local daemon.
#
# The daemon and the client are two processes, and the client finds the daemon
# through a lockfile named from the WORKSPACE (protocol.LockPathFor). So both
# must be pointed at the same directory or they will not meet -- which is
# exactly the bug that made every terminal run fail with "daemon not found"
# until clients/tui adopted the per-workspace name.
#
# This script never edits models.json, never writes to .env, and never prints
# your key.
#
# Usage:
#   ./run-tui.sh                          # agent mode ON, this repo as workspace
#   ./run-tui.sh --workspace ~/some/repo  # any daemon flag passes through
#   PLAIN=1 ./run-tui.sh                  # models.json instead: no agent mode
#
# The key can come from the environment (.env) or be stored once with
#   mochiii-daemon connect
# The environment wins; this script says which one it found.
#
# Stop the daemon it started with:  ./run-tui.sh --stop
# All three programs are built here: the daemon, the client and the embedder
# helper. A daemon left running from an older build -- its own, or its helper's
# -- is restarted automatically (Linux), so what runs is what was just built.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# WHERE THE PROGRAMS AND THE LOG LIVE: in folders that are this user's alone.
#
# FOUND 2026-10-12: they were ${TMPDIR:-/tmp}/mochiii-bin and
# ${TMPDIR:-/tmp}/mochiii-daemon.log. /tmp is shared, and the names were fixed,
# so on a machine with other accounts whoever made that folder first owned it:
# `mkdir -p` was satisfied, the build wrote into it, and the daemon was then
# started from a folder somebody else could swap a file in. The log beside it
# was readable by everyone, and it records the command line of every MCP server
# the user adds. (A reboot also emptied it, which is how it was noticed twice.)
readonly BIN="${XDG_CACHE_HOME:-$HOME/.cache}/mochiii/bin"
readonly STATE="${XDG_STATE_HOME:-$HOME/.local/state}/mochiii"
readonly LOG="$STATE/daemon.log"
# Where builds went before. Nothing is ever run from it again; a daemon still
# running from it is found so that it can be stopped.
readonly OLD_BIN="${TMPDIR:-/tmp}/mochiii-bin"

die() { printf 'run-tui: %s\n' "$*" >&2; exit 1; }

# private_dir makes a folder that is this user's and nobody else's, or stops.
# A folder that already exists is checked rather than trusted: not a link, and
# owned by whoever is running this. Then it is closed to everyone else.
private_dir() {
	mkdir -p "$1"
	[[ -d "$1" && ! -L "$1" && -O "$1" ]] ||
		die "$1 is not a folder of your own, so nothing is built or run in it. Remove it, or set XDG_CACHE_HOME / XDG_STATE_HOME."
	chmod 700 "$1"
}

if [[ "${1:-}" == "--stop" ]]; then
	stopped=""
	pkill -f "$BIN/mochiii-daemon" && stopped=1
	pkill -f "$OLD_BIN/mochiii-daemon" && stopped=1
	[[ -n "$stopped" ]] && echo "daemon stopped" || echo "no daemon running"
	exit 0
fi

# AGENT MODE NEEDS AN "mcp" BLOCK, and models.json has none -- so a plain run
# has no tools, no specialists and nothing to orchestrate. models.agent.json is
# models.json plus that block: reads pre-approved, writes and sandbox_exec still
# ask. PLAIN=1 opts back out.
CONFIG="models.agent.json"
[[ -n "${PLAIN:-}" ]] && CONFIG="models.json"
[[ -f "$CONFIG" ]] || die "$CONFIG is missing"

# The Go toolchain lives outside the default PATH on this machine; find it
# rather than make every run start with an export.
command -v go >/dev/null 2>&1 || PATH="$HOME/.local/go/bin:$PATH"
command -v go >/dev/null 2>&1 || die "go not found; install it or add it to PATH"

private_dir "$BIN"
private_dir "$STATE"
echo "building…"
(cd daemon && go build -o "$BIN/mochiii-daemon" .)
(cd clients/tui && go build -o "$BIN/mochiii" .)

# THE EMBEDDER HELPER IS BUILT TOO, next to the daemon -- the first place the
# daemon looks for it (daemon/helperpath.go).
#
# FOUND 2026-10-08: this script built two of the product's three programs. The
# third, the helper that computes embeddings, was whatever binary happened to
# sit in helper/ -- on the machine this was found on, one built seventeen days
# earlier. So a fix to the helper (that day's: its memory, which had reached
# 6 GB) was in the source, passed every test, and would never have run here.
#
# Built like the other two, with no C compiler: the helper stopped needing cgo
# on 2026-10-19. A failed build still says so and carries on with whatever
# helper is already there, since everything but local code search works
# without it.
readonly HELPER="$BIN/mochiii-embedder-helper"
if ! (cd helper && CGO_ENABLED=0 go build -o "$HELPER" .) 2>"$BIN/helper-build.log"; then
	echo "run-tui: could not build the embedder helper; see $BIN/helper-build.log" >&2
	echo "run-tui: carrying on with the helper that is already there, if there is one" >&2
fi

# The daemon takes its credential from the environment, or from the one
# `mochiii-daemon connect` stored. .env is the documented place for the
# environment form (see .env.example); sourcing it never echoes a value.
if [[ -f .env ]]; then
	set -a; . ./.env; set +a
fi

# WHICH KEY IS IN FORCE, SAID OUT LOUD. The environment wins over a stored
# credential, and .env above is part of the environment -- so someone who has just
# run `connect` and still has a key in .env would otherwise watch the daemon use
# the old one with nothing saying so. Naming it is the difference between a
# surprising result and an explained one. No value is ever printed.
readonly STORED="${HOME}/.mochiii/credentials.json"
if [[ -n "${MOCHIII_API_KEY:-}${MOCHIII_PROXY_KEY:-}" ]]; then
	if [[ -f "$STORED" ]]; then
		echo "run-tui: a key is set in the environment (.env?), so it is used and the key stored by" >&2
		echo "run-tui: \`mochiii-daemon connect\` is NOT. Unset MOCHIII_API_KEY to use the stored one." >&2
	fi
elif [[ -f "$STORED" ]]; then
	echo "run-tui: no key in the environment; using the one stored by \`mochiii-daemon connect\`"
else
	# NO KEY ANYWHERE, AND THIS SCRIPT DOES NOT ASK FOR ONE. The client does: with
	# no key in force it opens on its setup -- choose a provider, paste its key,
	# choose a model (clients/tui/setup.go, owner 2026-10-11). Esc there skips to
	# the chat, and the key is then asked for when a question is submitted.
	echo "run-tui: no API key in the environment, and none stored -- the client opens on its" >&2
	echo "run-tui: setup (provider, key, model). Esc skips it; /connect opens it again." >&2
fi

# The workspace both halves must agree on. Anything the caller passes wins.
ARGS=("$@")
[[ " ${ARGS[*]} " == *" --workspace "* ]] || ARGS+=(--workspace .)

# A DAEMON OLDER THAN THIS BUILD IS RESTARTED, NOT REUSED. The build above
# replaced the binary, but a daemon that is already running keeps executing the
# old one -- and a new client on an old daemon is two versions of the product:
# a daemon older than the client may not understand what it asks (/history,
# for one). `go build` rewrites the file only when the code
# changed (measured: same source, same inode; changed source, a new one), so the
# running executable being a different inode from the file on disk means exactly
# "built from older code". Linux only (/proc); elsewhere nothing changes here,
# and the client itself says "restart it".
#
# Restarting between turns is safe for a client in another terminal: it opens a
# connection per prompt and finds the new daemon through the lock file. A turn
# in flight there when this runs would end.
#
# AND A DAEMON WHOSE HELPER IS OLDER THAN THIS BUILD, likewise. The daemon starts
# its helper once and keeps it, so a helper built from older code goes on
# running under a daemon that has not itself changed. Only the helpers of the
# daemons this script started are looked at -- never another daemon's.
stale=()
# A daemon started from where builds used to go is older than this one by
# definition, and the client must not be left talking to it.
for pid in $(pgrep -f "$OLD_BIN/mochiii-daemon" || true); do
	stale+=("$pid")
done
for pid in $(pgrep -f "$BIN/mochiii-daemon" || true); do
	running=$(stat -L -c %i "/proc/$pid/exe" 2>/dev/null) || continue
	if [[ "$running" != "$(stat -c %i "$BIN/mochiii-daemon")" ]]; then
		stale+=("$pid")
		continue
	fi
	[[ -x "$HELPER" ]] || continue
	for child in $(pgrep -P "$pid" || true); do
		[[ "$(readlink "/proc/$child/exe" 2>/dev/null)" == *mochiii-embedder-helper* ]] || continue
		helper_running=$(stat -L -c %i "/proc/$child/exe" 2>/dev/null) || continue
		if [[ "$helper_running" != "$(stat -c %i "$HELPER")" ]]; then
			stale+=("$pid")
			break
		fi
	done
done
if (( ${#stale[@]} )); then
	echo "restarting the daemon: it, or its embedder helper, is running an older build than the one just made"
	kill "${stale[@]}" 2>/dev/null || true
	for _ in $(seq 1 50); do
		alive=()
		for pid in "${stale[@]}"; do kill -0 "$pid" 2>/dev/null && alive+=("$pid"); done
		(( ${#alive[@]} )) || break
		sleep 0.1
	done
	(( ${#alive[@]} == 0 )) || die "the old daemon (pid ${alive[*]}) did not stop; run ./run-tui.sh --stop, then this again"
fi

if pgrep -f "$BIN/mochiii-daemon" >/dev/null 2>&1; then
	echo "daemon already running (./run-tui.sh --stop to restart it)"
else
	echo "starting daemon -> $LOG"
	# Readable by this user only, whatever it was before. Done here and not with
	# a umask for the whole script: the daemon inherits that, and the files it
	# creates in the user's project must keep the modes they would normally get.
	: >"$LOG"
	chmod 600 "$LOG"
	nohup "$BIN/mochiii-daemon" --config "$CONFIG" "${ARGS[@]}" >"$LOG" 2>&1 &
	dpid=$!
	# Wait for THIS daemon's lock -- one naming its pid -- not any daemon's: a
	# lock left by another workspace's daemon, or by the one just stopped, used
	# to end the wait before this one was listening.
	up=""
	for _ in $(seq 1 30); do
		if grep -Eqs "\"pid\": *$dpid[,} ]" "${XDG_RUNTIME_DIR:-/tmp}"/mochiii/daemon-*.lock; then
			up=1
			break
		fi
		if ! kill -0 "$dpid" 2>/dev/null; then
			code=0
			wait "$dpid" || code=$?
			# exitAlreadyRunning (daemon/exitcodes.go): another daemon already
			# serves this workspace -- one root, one daemon -- and the client
			# will use it.
			(( code == 3 )) && { echo "another daemon already serves this workspace; using it"; up=1; break; }
			die "daemon exited at startup (code $code); see $LOG"
		fi
		sleep 1
	done
	[[ -n "$up" ]] || die "daemon did not come up; see $LOG"
fi

echo "config=$CONFIG   daemon log: tail -f $LOG"
echo
exec "$BIN/mochiii" "${ARGS[@]}"
