#!/usr/bin/env bash
#
# run-proxy.sh — start the daemon in managed-proxy mode (the pilot path).
#
# Direct mode (the default everywhere else) sends your own OpenRouter key
# straight to OpenRouter. Proxy mode instead authenticates you to the managed
# proxy with a per-user Mochiii key; the proxy holds the OpenRouter key
# server-side, meters usage, and enforces the zero-data-retention routing flags
# (F1) before anything reaches OpenRouter.
#
# This script only sets up the environment and hands off. It never edits
# models.json, never writes to .env, and never prints your key.
#
# Usage:
#   ./run-proxy.sh                             # index the current dir's repo
#   ./run-proxy.sh --workspace ~/some/repo     # any daemon flag passes through
#   CODETERMINAL_API_BASE=http://localhost:8080/v1 ./run-proxy.sh   # local proxy

set -euo pipefail

# Run from the repo root regardless of where the caller invoked this from, so
# the daemon's relative defaults (./models.json) resolve the same way every time.
cd "$(dirname "${BASH_SOURCE[0]}")"

# The production proxy. Not a secret — it is a public HTTPS endpoint that
# refuses every unauthenticated request — so it is baked in as a DEFAULT rather
# than something each operator has to remember. Overridable: export
# CODETERMINAL_API_BASE before running to point at a local proxy instead.
readonly DEFAULT_PROXY_BASE="https://codeterminal-core-production.up.railway.app/v1"

readonly DAEMON="./daemon/codeterminal-daemon"

die() {
	printf 'run-proxy: %s\n' "$1" >&2
	shift
	for line in "$@"; do
		printf '  %s\n' "$line" >&2
	done
	exit 1
}

warn() {
	printf 'run-proxy: warning: %s\n' "$1" >&2
}

# Captured BEFORE .env is sourced. .env's CODETERMINAL_API_BASE is the
# direct-to-OpenRouter default and would silently defeat proxy mode, so in this
# script only an explicit value from the CALLER'S environment counts as an
# override. Everything else falls back to DEFAULT_PROXY_BASE below.
caller_api_base="${CODETERMINAL_API_BASE:-}"

if [[ -f .env ]]; then
	# set -a exports every assignment in .env; the daemon reads plain env vars
	# and does not parse .env itself.
	set -a
	# shellcheck disable=SC1091
	source .env
	set +a
else
	die ".env not found in $(pwd)" \
		"Create it from the template, then put your Mochiii key in it:" \
		"  cp .env.example .env" \
		"  \$EDITOR .env      # set CODETERMINAL_MOCHIII_KEY=mochi_..."
fi

if [[ -n "$caller_api_base" ]]; then
	export CODETERMINAL_API_BASE="$caller_api_base"
	printf 'run-proxy: using CODETERMINAL_API_BASE from your environment: %s\n' "$CODETERMINAL_API_BASE" >&2
else
	# Note when .env carried a different base, so "why is it not hitting the
	# host I configured?" is answered on screen instead of in a debug session.
	if [[ -n "${CODETERMINAL_API_BASE:-}" && "${CODETERMINAL_API_BASE}" != "$DEFAULT_PROXY_BASE" ]]; then
		warn "ignoring CODETERMINAL_API_BASE=${CODETERMINAL_API_BASE} from .env (that is the direct-to-OpenRouter setting; this script is proxy mode)"
		warn "to use a different proxy, export CODETERMINAL_API_BASE before running this script"
	fi
	export CODETERMINAL_API_BASE="$DEFAULT_PROXY_BASE"
fi

# The mode switch itself. The daemon reads this and takes its key from
# CODETERMINAL_MOCHIII_KEY instead of CODETERMINAL_API_KEY.
export CODETERMINAL_USE_PROXY=true

# Unset, not merely ignored. If both are set the daemon warns and still sends
# the OpenRouter key to the proxy, which does not want it and does not use it —
# so the key would leave this machine for no reason. Clearing it here means the
# pilot path cannot leak a direct-provider credential to the proxy.
if [[ -n "${CODETERMINAL_API_KEY:-}" ]]; then
	printf 'run-proxy: unsetting CODETERMINAL_API_KEY (proxy mode authenticates with the Mochiii key; the proxy holds its own OpenRouter key)\n' >&2
	unset CODETERMINAL_API_KEY
fi

# ---- preflight ------------------------------------------------------------

if [[ -z "${CODETERMINAL_MOCHIII_KEY:-}" ]]; then
	die "CODETERMINAL_MOCHIII_KEY is empty — the proxy authenticates every request with it" \
		"Add your key to .env (it is gitignored; never commit it):" \
		"  CODETERMINAL_MOCHIII_KEY=mochi_..." \
		"Then re-run this script."
fi

# Shape check only, never the value. A pasted OpenRouter key here is the most
# likely mistake and produces a bare 401 from the proxy, which reads like an
# outage rather than a wrong-credential.
if [[ "$CODETERMINAL_MOCHIII_KEY" == sk-or-* || "$CODETERMINAL_MOCHIII_KEY" == sk-* ]]; then
	die "CODETERMINAL_MOCHIII_KEY looks like an OpenRouter key (starts with sk-), not a Mochiii key" \
		"Proxy mode needs your per-user Mochiii key (mochi_...), not a provider key." \
		"The OpenRouter key belongs on the proxy's server, never on this machine."
fi

if [[ ! -x "$DAEMON" ]]; then
	die "daemon binary not found at $DAEMON" \
		"Build it:" \
		"  (cd daemon && go build -o codeterminal-daemon .)"
fi

if [[ ! -f models.json ]]; then
	die "models.json not found in $(pwd)" \
		"The daemon reads ./models.json by default for the model slug and ZDR settings." \
		"Pass --config /path/to/models.json if yours lives elsewhere."
fi

# Reachability is a warning, not a failure — deliberately matching the daemon,
# which validates CODETERMINAL_API_BASE structurally at startup but treats
# "can I reach it right now" as a runtime state rather than a startup error.
if command -v curl >/dev/null 2>&1; then
	health_url="${CODETERMINAL_API_BASE%/v1}/health"
	if ! health_status="$(curl -fsS -m 10 -o /dev/null -w '%{http_code}' "$health_url" 2>/dev/null)"; then
		warn "could not reach the proxy health endpoint at $health_url"
		warn "the daemon will still start; prompts will fail until the proxy is reachable"
		warn "if you meant to use a local proxy: CODETERMINAL_API_BASE=http://localhost:8080/v1 $0"
	elif [[ "$health_status" != "200" ]]; then
		warn "proxy health endpoint $health_url returned HTTP $health_status (expected 200)"
	fi
fi

printf 'run-proxy: proxy mode -> %s (Mochiii key set, %d chars)\n' \
	"$CODETERMINAL_API_BASE" "${#CODETERMINAL_MOCHIII_KEY}" >&2

# exec so the daemon replaces this shell: it keeps the daemon in the
# foreground, and Ctrl-C / SIGTERM reach it directly so it can clean up its
# socket and lockfile. All arguments pass through untouched.
exec "$DAEMON" "$@"
