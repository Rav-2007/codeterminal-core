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

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

readonly BIN="${TMPDIR:-/tmp}/mochiii-bin"
readonly LOG="${TMPDIR:-/tmp}/mochiii-daemon.log"

die() { printf 'run-tui: %s\n' "$*" >&2; exit 1; }

if [[ "${1:-}" == "--stop" ]]; then
	pkill -f "$BIN/mochiii-daemon" && echo "daemon stopped" || echo "no daemon running"
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

mkdir -p "$BIN"
echo "building…"
(cd daemon && go build -o "$BIN/mochiii-daemon" .)
(cd clients/tui && go build -o "$BIN/mochiii" .)

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
	# NO KEY ANYWHERE, AND THIS SCRIPT DOES NOT ASK FOR ONE. Opening the client is
	# not the moment: a new user should meet the prompt, not a credential form. The
	# client asks for the key when it is actually needed -- when a question is
	# submitted and there is nothing to authenticate it with -- and `/connect` is
	# there before that for anyone who wants to set it up first.
	echo "run-tui: no API key in the environment, and none stored -- the client will ask" >&2
	echo "run-tui: for one when you send your first question, or run /connect before then." >&2
fi

# The workspace both halves must agree on. Anything the caller passes wins.
ARGS=("$@")
[[ " ${ARGS[*]} " == *" --workspace "* ]] || ARGS+=(--workspace .)

if pgrep -f "$BIN/mochiii-daemon" >/dev/null 2>&1; then
	echo "daemon already running (./run-tui.sh --stop to restart it)"
else
	echo "starting daemon -> $LOG"
	nohup "$BIN/mochiii-daemon" --config "$CONFIG" "${ARGS[@]}" >"$LOG" 2>&1 &
	for _ in $(seq 1 30); do
		compgen -G "${XDG_RUNTIME_DIR:-/tmp}/mochiii/daemon-*.lock" >/dev/null && break
		sleep 1
	done
	compgen -G "${XDG_RUNTIME_DIR:-/tmp}/mochiii/daemon-*.lock" >/dev/null \
		|| die "daemon did not come up; see $LOG"
fi

echo "config=$CONFIG   daemon log: tail -f $LOG"
echo
exec "$BIN/mochiii" "${ARGS[@]}"
