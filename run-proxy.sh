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
#   CODETERMINAL_PROXY_BASE=http://localhost:8080/v1 ./run-proxy.sh  # local proxy

set -euo pipefail

# Run from the repo root regardless of where the caller invoked this from, so
# the daemon's relative defaults (./models.json) resolve the same way every time.
cd "$(dirname "${BASH_SOURCE[0]}")"

# The production proxy. Not a secret — it is a public HTTPS endpoint that
# refuses every unauthenticated request — so it is baked in as a DEFAULT rather
# than something each operator has to remember. Overridable only via
# CODETERMINAL_PROXY_BASE (see below), never via CODETERMINAL_API_BASE.
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

# CODETERMINAL_PROXY_BASE is the ONE way to aim this script at a different
# proxy. Captured BEFORE .env is sourced so an explicitly exported value wins
# over an .env line, then re-applied after the source below.
#
# It exists as a separate variable because CODETERMINAL_API_BASE cannot do this
# job safely. This script used to treat a caller-supplied CODETERMINAL_API_BASE
# as a deliberate override, which could not distinguish "the operator exported a
# proxy URL on purpose" from "the operator did what the README says" — step 1 of
# "Run it" is `set -a && source .env && set +a`, and .env's CODETERMINAL_API_BASE
# is the direct-to-OpenRouter value. Running this script from that shell sent
# `Authorization: Bearer mochi_...` to OpenRouter: proxy mode silently defeated,
# the Mochiii key handed to a third party that cannot use it, and a bare 401 that
# reads like an outage. A proxy-only variable removes the ambiguity entirely —
# no direct-mode setting can decide which base a Mochiii key is sent to.
caller_proxy_base="${CODETERMINAL_PROXY_BASE:-}"

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

# Resolve which proxy to use: an explicitly exported CODETERMINAL_PROXY_BASE
# first, then one set in .env, then the production default. Nothing else is
# consulted.
if [[ -n "$caller_proxy_base" ]]; then
	proxy_base="$caller_proxy_base"
elif [[ -n "${CODETERMINAL_PROXY_BASE:-}" ]]; then
	proxy_base="${CODETERMINAL_PROXY_BASE}"
else
	proxy_base="$DEFAULT_PROXY_BASE"
fi

# A Mochiii key is only ever valid at the proxy, so refuse to point at a known
# direct provider even when asked explicitly. This is reachable only by setting
# CODETERMINAL_PROXY_BASE to it deliberately — but that is still a credential
# leaving for a host that will reject it, and the failure it produces (401) does
# not name its own cause.
if [[ "$proxy_base" == *"openrouter.ai"* ]]; then
	die "CODETERMINAL_PROXY_BASE points at OpenRouter ($proxy_base), which is a provider, not the proxy" \
		"Proxy mode sends your Mochiii key to this URL; OpenRouter would reject it with a bare 401." \
		"For direct-to-OpenRouter use, run the daemon without this script (see \"Run it\" in README.md)."
fi

# Set AUTHORITATIVELY, overriding whatever the caller's shell or .env carried:
# in proxy mode the base is decided by this script, never by a direct-mode
# setting that happens to be in the environment. See caller_proxy_base above for
# the failure this prevents.
inherited_api_base="${CODETERMINAL_API_BASE:-}"
export CODETERMINAL_API_BASE="$proxy_base"

# Said plainly, once, when a base was actually displaced — so "why is it not
# hitting the host I configured?" is answered on screen rather than in a debug
# session. Not a warning: for anyone who followed the README this is the normal,
# correct outcome of sourcing .env, and nothing is wrong.
if [[ -n "$inherited_api_base" && "$inherited_api_base" != "$proxy_base" ]]; then
	printf 'run-proxy: note: CODETERMINAL_API_BASE=%s in your environment is the direct-mode setting and is not used here; set CODETERMINAL_PROXY_BASE to point at a different proxy\n' \
		"$inherited_api_base" >&2
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
		warn "if you meant to use a local proxy: CODETERMINAL_PROXY_BASE=http://localhost:8080/v1 $0"
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
