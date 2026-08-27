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
# Stop the daemon it started with:  ./run-tui.sh --stop

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

readonly BIN="${TMPDIR:-/tmp}/mochiii-bin"
readonly LOG="${TMPDIR:-/tmp}/mochiii-daemon.log"

die() { printf 'run-tui: %s\n' "$*" >&2; exit 1; }

if [[ "${1:-}" == "--stop" ]]; then
	pkill -f "$BIN/codeterminal-daemon" && echo "daemon stopped" || echo "no daemon running"
	exit 0
fi

# AGENT MODE NEEDS AN "mcp" BLOCK, and models.json has none -- so a plain run
# has no tools, no specialists and nothing to orchestrate. models.agent.json is
# models.json plus that block: reads pre-approved, writes and sandbox_exec still
# ask. PLAIN=1 opts back out.
CONFIG="models.agent.json"
[[ -n "${PLAIN:-}" ]] && CONFIG="models.json"
[[ -f "$CONFIG" ]] || die "$CONFIG is missing"

mkdir -p "$BIN"
echo "building…"
(cd daemon && go build -o "$BIN/codeterminal-daemon" .)
(cd clients/tui && go build -o "$BIN/mochiii" .)

# The daemon reads its credential from the environment. .env is the documented
# place for it (see .env.example); sourcing it never echoes a value.
if [[ -f .env ]]; then
	set -a; . ./.env; set +a
fi
[[ -n "${CODETERMINAL_API_KEY:-}${CODETERMINAL_MOCHIII_KEY:-}" ]] || \
	echo "run-tui: warning: no API key in the environment; prompts will fail" >&2

# The workspace both halves must agree on. Anything the caller passes wins.
ARGS=("$@")
[[ " ${ARGS[*]} " == *" --workspace "* ]] || ARGS+=(--workspace .)

if pgrep -f "$BIN/codeterminal-daemon" >/dev/null 2>&1; then
	echo "daemon already running (./run-tui.sh --stop to restart it)"
else
	echo "starting daemon -> $LOG"
	nohup "$BIN/codeterminal-daemon" --config "$CONFIG" "${ARGS[@]}" >"$LOG" 2>&1 &
	for _ in $(seq 1 30); do
		compgen -G "${XDG_RUNTIME_DIR:-/tmp}/codeterminal/daemon-*.lock" >/dev/null && break
		sleep 1
	done
	compgen -G "${XDG_RUNTIME_DIR:-/tmp}/codeterminal/daemon-*.lock" >/dev/null \
		|| die "daemon did not come up; see $LOG"
fi

echo "config=$CONFIG   daemon log: tail -f $LOG"
echo
exec "$BIN/mochiii" "${ARGS[@]}"
