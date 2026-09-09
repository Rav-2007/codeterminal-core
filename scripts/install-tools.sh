#!/usr/bin/env bash
# Install every pinned analysis tool, from the one list that defines them.
#
# CI calls this instead of spelling out `go install` lines, so the versions CI
# builds with and the versions lint.sh CHECKS FOR cannot drift apart. Before
# 2026-09-09 they were separate lists and one of them was `@latest`, which is
# how six lint jobs died without running a linter.
set -uo pipefail
cd "$(dirname "$0")/.."
pins="scripts/tool-pins.txt"

if [ ! -f "$pins" ]; then
  echo "install-tools: missing $pins -- there is nothing to install and that is not success." >&2
  exit 2
fi

toolchain="$(awk '$1=="GOTOOLCHAIN"{print $2}' "$pins")"
if [ -z "$toolchain" ]; then
  echo "install-tools: $pins declares no GOTOOLCHAIN line." >&2
  exit 2
fi

installed=0
status=0
while read -r name spec; do
  case "${name:-}" in ""|\#*|GOTOOLCHAIN) continue ;; esac
  [ -z "${spec:-}" ] && continue
  echo "install-tools: $name <- $spec (GOTOOLCHAIN=$toolchain)"
  if GOTOOLCHAIN="$toolchain" go install "$spec"; then
    installed=$((installed + 1))
  else
    echo "install-tools: FAILED to install $name from $spec" >&2
    status=1
  fi
done < "$pins"

# A loop that installed nothing must not report success -- the same vacuity
# floor every other gate here carries.
if [ "$installed" -eq 0 ]; then
  echo "install-tools: parsed no tools from $pins -- refusing to report success." >&2
  exit 2
fi

[ $status -eq 0 ] && echo "install-tools: $installed tool(s) installed at their pinned versions"
exit $status
