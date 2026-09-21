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
ext=0
anchor=0

while IFS= read -r doc; do
  dir=$(dirname "$doc")

  # Extract the target of every inline markdown link. -o prints one per line.
  while IFS= read -r target; do
    [ -z "$target" ] && continue

    # External, protocol-relative, mail, and pure-anchor links are out of scope.
    # COUNTED, NOT JUST SKIPPED. The scope of this gate was stated only in the
    # comment above, where nobody reading a CI log sees it -- so "N links
    # resolve" read as though every link had been checked. These counters make
    # the reported scope derived from the run rather than asserted by a header.
    case "$target" in
      http://*|https://*|//*|mailto:*) ext=$((ext + 1)); continue ;;
      \#*)                             anchor=$((anchor + 1)); continue ;;
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

# SCOPE FIRST, RESULT LAST, and the order is load-bearing rather than stylistic:
# .githooks/pre-push:158 summarises this gate as `tail -1` of its output. A
# multi-line note printed after the count therefore replaced "334 links resolve"
# with whatever fragment happened to land last -- observed once, as
# `pre-push: ok docs (            claims it says.)`. Every line is also prefixed
# with the gate name so that no line of it is orphaned by a tail, a grep or a
# log excerpt.
echo "docs-links: NOT checked -- ${ext} external URL(s) (http, https, protocol-relative, mailto)"
echo "docs-links: NOT checked -- ${anchor} pure-anchor link(s), and the #anchor on every link below"
echo "docs-links: NOT checked -- the :NN of a line citation, nor whether any target says"
echo "docs-links:                what the link claims it says. This gate proves a FILE exists."
echo "docs-links: ${checked} links resolve"
