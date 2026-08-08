#!/usr/bin/env bash
# Markdown link checker: every relative link in a tracked .md file must resolve
# to a file that exists.
#
# WHY THIS EXISTS. A documentation pass on 2026-08-07 found and hand-fixed 32
# broken links and 6 dead line citations across the docs tree. Nothing prevented
# them, and nothing prevents the next 32: a link rots the moment a file is
# renamed, and no test, build or review step reads them. It is the same shape as
# the cross-client "keep these in sync" comments -- an invariant maintained by
# hope. This makes it fail a build instead.
#
# WHAT IT DELIBERATELY DOES NOT CHECK: the `path/file.go:123` line citations
# this codebase uses heavily. Those drift on every edit above the cited line, so
# a gate on them would fire constantly for changes that broke nothing, and a
# gate people learn to ignore is worse than no gate. Existence of the FILE is
# stable and is what actually rots when something is renamed or deleted.
#
# Anchors (#section) are stripped, not verified -- heading text changes are
# cosmetic and checking them would reintroduce the noise problem above.
set -uo pipefail
cd "$(dirname "$0")/.."

broken=0
checked=0

while IFS= read -r doc; do
  dir=$(dirname "$doc")

  # Extract the target of every inline markdown link. -o prints one per line.
  while IFS= read -r target; do
    [ -z "$target" ] && continue

    # External, protocol-relative, mail, and pure-anchor links are out of scope.
    case "$target" in
      http://*|https://*|//*|mailto:*|\#*) continue ;;
    esac

    # Strip the anchor, then a trailing :NN line citation.
    path=${target%%#*}
    path=$(printf '%s' "$path" | sed -E 's/:[0-9]+(-[0-9]+)?$//')
    [ -z "$path" ] && continue

    # Resolve relative to the file's own directory, as a renderer would.
    case "$path" in
      /*) resolved=".${path}" ;;
      *)  resolved="${dir}/${path}" ;;
    esac

    checked=$((checked + 1))
    if [ ! -e "$resolved" ]; then
      echo "  ${doc}: broken link -> ${target}"
      broken=$((broken + 1))
    fi
  done < <(grep -oE '\]\([^)]+\)' "$doc" 2>/dev/null | sed -E 's/^\]\(//; s/\)$//' | sed -E 's/[[:space:]]+"[^"]*"$//')

done < <(git ls-files '*.md')

# ANTI-VACUITY. A checker whose extraction silently matches nothing reports a
# clean tree forever. This repo has thousands of links; if we examined almost
# none, the regex broke and the result is meaningless.
if [ "$checked" -lt 100 ]; then
  echo "docs-links: examined only ${checked} links -- the extractor is broken, and a" >&2
  echo "            pass here would mean nothing. Failing rather than reporting clean." >&2
  exit 1
fi

if [ "$broken" -gt 0 ]; then
  echo "docs-links: ${broken} broken link(s) out of ${checked} checked"
  exit 1
fi

echo "docs-links: ${checked} links resolve"
