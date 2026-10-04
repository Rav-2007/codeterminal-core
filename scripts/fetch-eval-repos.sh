#!/usr/bin/env bash
# Fetch the outside repositories the external retrieval eval scores against,
# each at the commit pinned in daemon/testdata/evalrepos/repos.txt.
#
#   scripts/fetch-eval-repos.sh            # into ~/.cache/mochiii-eval-repos
#   MOCHIII_EVAL_REPOS=/some/dir scripts/fetch-eval-repos.sh
#
# One commit each, depth 1, verified after checkout: the eval's anchors name
# lines of exactly these trees. A repository already at its pin is left alone.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
manifest="$root/daemon/testdata/evalrepos/repos.txt"
dest="${MOCHIII_EVAL_REPOS:-$HOME/.cache/mochiii-eval-repos}"
mkdir -p "$dest"

while read -r name _lang url sha; do
  case "$name" in '' | '#'*) continue ;; esac
  if ! [[ "$name" =~ ^[a-z0-9-]+$ && "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    echo "fetch-eval-repos: bad manifest line for '$name'" >&2
    exit 1
  fi
  dir="$dest/$name"
  if [ "$(git -C "$dir" rev-parse HEAD 2>/dev/null || true)" = "$sha" ]; then
    echo "ok       $name @ ${sha:0:12} (already fetched)"
    continue
  fi
  rm -rf -- "$dir"
  git init -q "$dir"
  git -C "$dir" fetch -q --depth 1 "$url" "$sha"
  git -C "$dir" -c advice.detachedHead=false checkout -q FETCH_HEAD
  got="$(git -C "$dir" rev-parse HEAD)"
  if [ "$got" != "$sha" ]; then
    echo "fetch-eval-repos: $name is at $got, pinned $sha" >&2
    exit 1
  fi
  echo "fetched  $name @ ${sha:0:12}"
done <"$manifest"
