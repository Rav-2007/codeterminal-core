#!/usr/bin/env bash
# No TODO / FIXME / HACK / XXX in non-test Go code.
#
# WHY THIS FILE EXISTS AT ALL. "Zero debt markers in non-test code" was one of
# the standing baseline invariants for this repository, and NOTHING ENFORCED IT.
# It was a grep somebody ran by hand, over a hardcoded `clients/tui/*.go`, and
# every report that claimed it was reporting a manual step. That glob covered
# one module out of six and would have silently found nothing if a file moved
# into a subpackage -- the same shape as the fuzz gate that reported green with
# no targets registered.
#
# MATCHES ANNOTATIONS, NOT WORDS. The first version of this check flagged
# daemon/groundingnudge.go:51 -- "THREE PROPERTIES KEEP IT FROM BEING A HACK:"
# -- which is prose explaining why something is NOT a hack. A gate that fails on
# its own documentation gets deleted. So the marker must open the comment, which
# is the Go convention (`// TODO(name): ...`) and is what a real annotation
# looks like.
#
# COUNTS WHAT IT INSPECTED. A path expression that stops matching finds zero
# markers and exits clean, which is indistinguishable from success. This one
# fails if it did not read any files.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
modules="${*:-daemon editapply proxy protocol helper clients/tui}"

# The marker must be the first word of a comment: `// TODO`, `/* FIXME`,
# `//TODO(bob):`. Prose that merely contains the word does not match.
pattern='(//|/\*)[[:space:]]*(TODO|FIXME|HACK|XXX)\b'

status=0
scanned=0
found=0

for m in $modules; do
  dir="$repo_root/$m"
  if [ ! -d "$dir" ]; then
    echo "FAIL  $m — no such module directory. This gate cannot inspect it."
    status=1
    continue
  fi

  # Non-test Go files only. Test files may carry markers; production code is
  # what this is about.
  files="$(find "$dir" -name '*.go' -not -name '*_test.go' -type f)"
  n="$(printf '%s\n' "$files" | grep -c . || true)"
  if [ "$n" -eq 0 ]; then
    echo "FAIL  $m — no non-test Go files found. Either the module moved or this"
    echo "      gate's path expression stopped matching; both are failures, because"
    echo "      finding zero markers in zero files is not a clean result."
    status=1
    continue
  fi
  scanned=$((scanned + n))

  hits="$(printf '%s\n' "$files" | xargs grep -nE "$pattern" 2>/dev/null || true)"
  if [ -n "$hits" ]; then
    c="$(printf '%s\n' "$hits" | grep -c .)"
    found=$((found + c))
    echo "FAIL  $m — $c debt marker(s) in non-test code:"
    printf '%s\n' "$hits" | sed "s|$repo_root/||" | sed 's/^/      /'
    status=1
  else
    echo "ok    $m — $n non-test file(s), no debt markers"
  fi
done

if [ "$scanned" -eq 0 ]; then
  echo "FAIL  inspected 0 files. This gate verified nothing."
  exit 1
fi

if [ "$status" -ne 0 ]; then
  echo
  echo "debt-markers: FAILED — $found marker(s) across $scanned non-test file(s)."
  echo "  A marker in shipped code is work someone decided not to do and did not"
  echo "  record anywhere a person would look. Put it in docs/RESIDUAL_RISKS.md"
  echo "  or docs/OPEN_ITEMS.md, where it has an owner and a trigger."
  exit 1
fi

echo "debt-markers: $scanned non-test file(s) across $(printf '%s' "$modules" | wc -w) module(s), 0 markers"
