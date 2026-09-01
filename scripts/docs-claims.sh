#!/usr/bin/env bash
# Cross-register claim checker: BACKLOG.md's summary of which OPEN_ITEMS remain
# must agree with OPEN_ITEMS.md's own Status column.
#
# WHY THIS EXISTS. Two files answer "what is open": BACKLOG.md's tiers and
# docs/OPEN_ITEMS.md's numbered register, and they cross-reference each other by
# number. On 2026-08-11 `ce96997` fixed L4, L5 and L7, updated OPEN_ITEMS.md by
# six lines, and left item 12's cell asserting all three still open -- while
# BACKLOG's Tier 2 struck the same three through as completed. Two registers,
# one tree, opposite answers, for nineteen days. Nothing detected it.
#
# That is not a first offence. BACKLOG.md records its own staleness at least
# eight times, including "docs index created after seven stale claims in one
# day" and, at :756, "one of those stale entries was hiding a live defect".
# Every other load-bearing invariant in this repo is machine-checked -- coverage
# floors, errcheck ceilings, doc links, evalguard, the language-table AST
# registry, socket-auth coverage. Documentation claims were the only ones left
# to human diligence, in the file people read to decide what to work on.
#
# BOTH DIRECTIONS FAIL. A register claiming OPEN about something fixed wastes a
# reader's time; a register claiming FIXED about something open hides a live
# defect. This does not care which way round the disagreement is.
#
# WHAT IT DELIBERATELY DOES NOT CHECK, for the reason docs-links.sh already
# records -- a gate people learn to ignore is worse than no gate:
#   * §3 onward in OPEN_ITEMS.md. Those tables have no Status column, so there
#     is nothing to compare; parsing them would invent noise.
#   * Prose anywhere else, line citations, or whether an item is ACTUALLY fixed
#     in the code. This checks that the two registers AGREE, not that either is
#     right. Agreement is mechanically decidable; correctness is not.
set -uo pipefail
cd "$(dirname "$0")/.."

OPEN_ITEMS="${DOCS_CLAIMS_OPEN_ITEMS:-docs/OPEN_ITEMS.md}"
BACKLOG="${DOCS_CLAIMS_BACKLOG:-BACKLOG.md}"
FLOOR="${DOCS_CLAIMS_FLOOR:-10}"

# --self-test proves THIS SCRIPT still detects a disagreement, using fixtures
# whose answer is known. It exists because the neuter matrix found two ways to
# make this checker lie and have nothing notice: replace the set comparison with
# `true`, and break the table parser so it reads zero items and calls {} == {}
# agreement. Neither is visible from a run against a tree that already agrees --
# a checker that only ever sees passing input cannot demonstrate it can fail.
if [ "${1:-}" = "--self-test" ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  {
    echo '| # | Status | Item | Where | Sev | Evidence |'
    echo '|---|---|---|---|---|---|'
    echo '| 1 | **FIXED** | a | b | Low | c |'
    echo '| 2 | **OPEN** | a | b | Low | c |'
  } > "$tmp/items.md"
  echo '| x | index — **open: 2** |' > "$tmp/readme.md"

  # (a) a disagreeing pair MUST fail
  echo "| x | ($tmp/items.md) - read the Status column; open items: none |" > "$tmp/backlog.md"
  if DOCS_CLAIMS_OPEN_ITEMS="$tmp/items.md" DOCS_CLAIMS_BACKLOG="$tmp/backlog.md" DOCS_CLAIMS_README="$tmp/readme.md" DOCS_CLAIMS_FLOOR=2 \
       "$0" >/dev/null 2>&1; then
    echo "docs-claims: SELF-TEST FAIL -- a disagreeing pair was reported as agreeing."
    echo "  The comparison is not doing its job; this checker cannot be trusted until it is fixed."
    exit 1
  fi

  # (b) an agreeing pair MUST pass, or the checker is merely always-red
  echo "| x | ($tmp/items.md) - read the Status column; open items: 2 |" > "$tmp/backlog.md"
  if ! DOCS_CLAIMS_OPEN_ITEMS="$tmp/items.md" DOCS_CLAIMS_BACKLOG="$tmp/backlog.md" DOCS_CLAIMS_README="$tmp/readme.md" DOCS_CLAIMS_FLOOR=2 \
       "$0" >/dev/null 2>&1; then
    echo "docs-claims: SELF-TEST FAIL -- an agreeing pair was reported as disagreeing."
    exit 1
  fi

  # (b2) the THIRD register must be compared too, not just carried along. This
  # is the case that was missing entirely: docs/README.md named the open items
  # and nothing checked it, which is how it came to say 22 was open after 22 was
  # fixed.
  echo '| x | index — **open: 1, 2** |' > "$tmp/readme-bad.md"
  if DOCS_CLAIMS_OPEN_ITEMS="$tmp/items.md" DOCS_CLAIMS_BACKLOG="$tmp/backlog.md" DOCS_CLAIMS_README="$tmp/readme-bad.md" DOCS_CLAIMS_FLOOR=2 \
       "$0" >/dev/null 2>&1; then
    echo "docs-claims: SELF-TEST FAIL -- a disagreeing docs/README.md was accepted."
    exit 1
  fi

  # (c) a duplicate id MUST fail. Without this the check above is a line of
  # code with nothing demonstrating it runs.
  {
    echo '| # | Status | Item | Where | Sev | Evidence |'
    echo '|---|---|---|---|---|---|'
    echo '| 1 | **FIXED** | a | b | Low | c |'
    echo '| 1 | **OPEN** | a | b | Low | c |'
  } > "$tmp/dupes.md"
  echo "| x | ($tmp/dupes.md) - read the Status column; open items: 1 |" > "$tmp/backlog.md"
  if DOCS_CLAIMS_OPEN_ITEMS="$tmp/dupes.md" DOCS_CLAIMS_BACKLOG="$tmp/backlog.md" DOCS_CLAIMS_README="$tmp/readme.md" DOCS_CLAIMS_FLOOR=2 \
       "$0" >/dev/null 2>&1; then
    echo "docs-claims: SELF-TEST FAIL -- a duplicated item number was accepted."
    exit 1
  fi

  # (d) the REAL register must still parse. Checked here with its own hardcoded
  # floor so that deleting the floor below cannot also disable this.
  real=$(DOCS_CLAIMS_FLOOR=1 "$0" --count 2>/dev/null || echo 0)
  if [ "${real:-0}" -lt 10 ]; then
    echo "docs-claims: SELF-TEST FAIL -- parsed $real items from the real register (expected >= 10)."
    echo "  The table format moved and the parser went blind."
    exit 1
  fi

  echo "docs-claims: self-test ok (detects disagreement, accepts agreement, parses $real real items)"
  exit 0
fi

# The two status tables, found by their exact header signature rather than by
# line number, so inserting a section above them does not silently change what
# is parsed.
HEADER='| # | Status | Item | Where | Sev | Evidence |'

parsed=$(awk -v header="$HEADER" '
  index($0, header) == 1 { intable = 1; next }
  intable && $0 !~ /^\|/ { intable = 0 }
  intable && $0 ~ /^\|---/ { next }
  intable {
    n = $0; s = $0
    # strip markdown emphasis/strikethrough/code so "~~11~~" and "**FIXED**" parse
    gsub(/[~*`]/, "", n); gsub(/[~*`]/, "", s)
    split(n, cells, "|")
    num = cells[2]; status = cells[3]
    gsub(/^[ \t]+|[ \t]+$/, "", num); gsub(/^[ \t]+|[ \t]+$/, "", status)
    if (num ~ /^[0-9]+$/) {
      state = (status ~ /^FIXED/) ? "closed" : "open"
      print num, state
    }
  }
' "$OPEN_ITEMS")

total=$(printf '%s\n' "$parsed" | grep -c . || true)

if [ "${1:-}" = "--count" ]; then
  echo "$total"
  exit 0
fi

# ANTI-VACUITY FLOOR. An empty or broken parse must never read as agreement:
# {} == {} is the most convincing wrong answer this script could give. There are
# 13 numbered items across the two tables today.
if [ "$total" -lt "$FLOOR" ]; then
  echo "docs-claims: FAIL -- parsed only $total numbered items from $OPEN_ITEMS (floor $FLOOR)."
  echo "  The table format changed and this checker went blind. Fix the parser, do not lower the floor."
  exit 1
fi

# ITEM NUMBERS MUST BE UNIQUE ACROSS THE PARSED TABLES.
#
# They were not. Number 20 named a FIXED watcher bug in section 1 and an OPEN
# prompt-injection channel in section 2 -- two unrelated items, one id, opposite
# statuses. The parser emitted both, the open-set filter kept the open one, and
# the output read "open: 20" while the file contained two of them. A reader
# following that number could land on either row, and if section 2's item 20
# had ever been closed the arithmetic would still have been self-consistent.
#
# Checked here rather than left to care because an ambiguous id defeats the
# whole point of the register: the numbers are how BACKLOG, docs/README and the
# audit report refer to these rows across files.
dupes=$(printf '%s\n' "$parsed" | awk '{ print $1 }' | sort -n | uniq -d | tr '\n' ' ' | sed 's/ $//')
if [ -n "$dupes" ]; then
  echo "docs-claims: FAIL -- item number(s) used more than once in $OPEN_ITEMS: $dupes"
  echo "  An id that names two rows makes every cross-file reference to it ambiguous."
  echo "  Renumber one of them to a value no section uses."
  exit 1
fi

actual_open=$(printf '%s\n' "$parsed" | awk '$2 == "open" { print $1 }' | sort -n | tr '\n' ' ' | sed 's/ $//')

# BACKLOG's restatement. Anchored on the row that points at OPEN_ITEMS.md and
# names the Status column. If that row is reworded away, this FAILS rather than
# silently passing -- a claim this script cannot find is a claim it cannot check.
claim_line=$(grep -n "$OPEN_ITEMS" "$BACKLOG" | grep "Status column" | head -1 || true)
if [ -z "$claim_line" ]; then
  echo "docs-claims: FAIL -- no row in $BACKLOG points at $OPEN_ITEMS and names the Status column."
  echo "  That row is the claim this checker exists to verify. Restore it, or delete this checker deliberately."
  exit 1
fi

lineno=${claim_line%%:*}
claimed=$(printf '%s' "$claim_line" | sed 's/^[0-9]*://' | sed 's/.*Status column;//' | grep -oE '[0-9]+' | sort -n | tr '\n' ' ' | sed 's/ $//')

# THE THIRD REGISTER. docs/README.md's index names the open items too, and was
# exempt -- a fact it stated in its own cell, one clause after the claim the
# exemption let go stale. On 2026-09-01 it read "20, 21, 22 and 24" while the
# register had item 22 FIXED. Two files were being compared; three were making
# the claim.
#
# Anchored on the same `**open: N, N**` marker BACKLOG uses, so both registers
# state the set the same way and neither can drift into a prose form this
# script silently stops finding. A missing marker FAILS: a claim that cannot be
# located is not a claim that has been checked.
README="${DOCS_CLAIMS_README:-docs/README.md}"
if [ -f "$README" ]; then
  readme_line=$(grep -n '\*\*open: ' "$README" | head -1 || true)
  if [ -z "$readme_line" ]; then
    echo "docs-claims: FAIL -- no '**open: ...**' marker in $README."
    echo "  That index names the open items; without the marker this checker cannot compare it."
    exit 1
  fi
  readme_no=${readme_line%%:*}
  readme_claimed=$(printf '%s' "$readme_line" | grep -oE '\*\*open: [^*]+\*\*' | grep -oE '[0-9]+' | sort -n | tr '\n' ' ' | sed 's/ $//')
  if [ "$readme_claimed" != "$actual_open" ]; then
    echo "docs-claims: FAIL -- $README:$readme_no and $OPEN_ITEMS disagree."
    echo "  $README says open: ${readme_claimed:-none}"
    echo "  $OPEN_ITEMS says open: ${actual_open:-none}"
    exit 1
  fi
fi

if [ "$claimed" = "$actual_open" ]; then
  echo "docs-claims: BACKLOG.md:$lineno, $README:${readme_no:-?} and $OPEN_ITEMS agree (open: ${actual_open:-none}, $total items checked)"
  exit 0
fi

echo "docs-claims: FAIL -- the two registers disagree about what is open."
echo "  $BACKLOG:$lineno  claims open: ${claimed:-none}"
echo "  $OPEN_ITEMS       Status column: ${actual_open:-none}"
for n in $claimed; do
  case " $actual_open " in *" $n "*) ;; *)
    echo "  -> item $n: BACKLOG says OPEN, $OPEN_ITEMS says FIXED (a reader is sent to work already done)";;
  esac
done
for n in $actual_open; do
  case " $claimed " in *" $n "*) ;; *)
    echo "  -> item $n: $OPEN_ITEMS says OPEN, BACKLOG does not list it (THE DANGEROUS DIRECTION -- a live item is invisible)";;
  esac
done
exit 1
