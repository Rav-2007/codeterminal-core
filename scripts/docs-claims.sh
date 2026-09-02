#!/usr/bin/env bash
# Cross-register claim checker. Two invariants, both cross-file:
#   1. BACKLOG.md and docs/README.md's summary of which OPEN_ITEMS remain must
#      agree with docs/OPEN_ITEMS.md's own Status column.
#   2. All three registers' summary of which founder decisions are taken must
#      agree with docs/DECISION_PACK.md's Status column.
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

  # Every fixture register carries BOTH markers, because the real ones do and a
  # fixture that omits one would make the checks below pass for the wrong reason.
  {
    echo '| # | Status | Item | Where | Sev | Evidence |'
    echo '|---|---|---|---|---|---|'
    echo '| 1 | **FIXED** | a | b | Low | c |'
    echo '| 2 | **OPEN** | a | b | Low | c |'
    echo ''
    echo '**taken: D1**'
  } > "$tmp/items.md"
  echo '| x | index — **open: 2** · **taken: D1** |' > "$tmp/readme.md"

  # The decision pack fixture: one taken, one not.
  {
    echo '| # | Decision | Status | Ruling / Recommendation | Blocks |'
    echo '|---|---|---|---|---|'
    echo '| D1 | a | **TAKEN 2026-01-01** | x | nothing |'
    echo '| D2 | b | pending | x | everything |'
  } > "$tmp/pack.md"

  # Every invocation below runs through this, so a new env knob cannot be added
  # to the checker and silently left out of half the cases.
  st() {
    DOCS_CLAIMS_OPEN_ITEMS="${1}" DOCS_CLAIMS_BACKLOG="${2}" DOCS_CLAIMS_README="${3}" \
    DOCS_CLAIMS_PACK="${4}" DOCS_CLAIMS_FLOOR=2 DOCS_CLAIMS_PACK_FLOOR=2 \
      "$0" >/dev/null 2>&1
  }

  # (a) a disagreeing pair MUST fail
  echo "| x | ($tmp/items.md) - read the Status column; **open: none** · **taken: D1** |" > "$tmp/backlog.md"
  if st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a disagreeing pair was reported as agreeing."
    echo "  The comparison is not doing its job; this checker cannot be trusted until it is fixed."
    exit 1
  fi

  # (b) an agreeing pair MUST pass, or the checker is merely always-red
  echo "| x | ($tmp/items.md) - read the Status column; **open: 2** · **taken: D1** |" > "$tmp/backlog.md"
  if ! st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- an agreeing pair was reported as disagreeing."
    exit 1
  fi

  # (b2) the THIRD register must be compared too, not just carried along. This
  # is the case that was missing entirely: docs/README.md named the open items
  # and nothing checked it, which is how it came to say 22 was open after 22 was
  # fixed.
  echo '| x | index — **open: 1, 2** · **taken: D1** |' > "$tmp/readme-bad.md"
  if st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme-bad.md" "$tmp/pack.md"; then
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
    echo ''
    echo '**taken: D1**'
  } > "$tmp/dupes.md"
  echo "| x | ($tmp/dupes.md) - read the Status column; **open: 1** · **taken: D1** |" > "$tmp/backlog.md"
  if st "$tmp/dupes.md" "$tmp/backlog.md" "$tmp/readme.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a duplicated item number was accepted."
    exit 1
  fi

  # (e) A REGISTER CLAIMING A DECISION THE PACK HAS NOT TAKEN must fail --
  # someone acting on a ruling nobody made.
  echo "| x | ($tmp/items.md) - read the Status column; **open: 2** · **taken: D1 D2** |" > "$tmp/backlog-d.md"
  if st "$tmp/items.md" "$tmp/backlog-d.md" "$tmp/readme.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a register claiming an untaken decision was accepted."
    exit 1
  fi

  # (f) AND THE OTHER DIRECTION, which is the one that actually happened: the
  # pack has taken D1, the register still lists nothing as taken. Tier 0 said
  # the project was blocked for three weeks after it was not.
  echo '| x | index — **open: 2** · **taken: none** |' > "$tmp/readme-stale.md"
  echo "| x | ($tmp/items.md) - read the Status column; **open: 2** · **taken: D1** |" > "$tmp/backlog.md"
  if st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme-stale.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a register that had not caught up with a TAKEN ruling was accepted."
    exit 1
  fi

  # (g) a MISSING marker must fail rather than skip. A claim that cannot be
  # located is not a claim that has been checked -- the rule this script already
  # applies to BACKLOG's claim row and to docs/README.md's existence.
  echo '| x | index — **open: 2** |' > "$tmp/readme-nomarker.md"
  if st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme-nomarker.md" "$tmp/pack.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a register with no '**taken:**' marker was silently skipped."
    exit 1
  fi

  # (h) the pack parser must not go blind. A table format change that reads zero
  # decisions would otherwise compare {} to {} and print agreement.
  {
    echo '| # | Ruling | State |'
    echo '|---|---|---|'
    echo '| D1 | a | TAKEN |'
  } > "$tmp/pack-moved.md"
  if st "$tmp/items.md" "$tmp/backlog.md" "$tmp/readme.md" "$tmp/pack-moved.md"; then
    echo "docs-claims: SELF-TEST FAIL -- a decision table the parser cannot read was accepted."
    exit 1
  fi

  # (d) the REAL registers must still parse. Checked here with their own
  # hardcoded floors so that deleting the floors above cannot also disable this.
  real=$(DOCS_CLAIMS_FLOOR=1 "$0" --count 2>/dev/null || echo 0)
  if [ "${real:-0}" -lt 10 ]; then
    echo "docs-claims: SELF-TEST FAIL -- parsed $real items from the real register (expected >= 10)."
    echo "  The table format moved and the parser went blind."
    exit 1
  fi

  echo "docs-claims: self-test ok (8 cases: disagreement, agreement, third register, duplicate ids, both decision directions, missing marker, blind pack parser; $real real items)"
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
# COLLECTED FROM EVERY DEFINING TABLE, not just the two with a Status column.
#
# The first version of this check parsed only the Status tables, so it saw §1 and
# §2 and reported "no duplicates" over a file where §4 reused 20, 21, 22, 23 and
# 24 for unrelated measurement items. That is not hypothetical drift: it had
# already reached other documents, and DECISION_PACK.md:244 had to write "item 23
# in the register's §4" to say which one it meant. A reader following a bare
# number could land on either row.
#
# §6's tables are deliberately NOT here. They are a closure LOG and reference ids
# defined above; requiring uniqueness there would forbid the cross-reference they
# exist to make.
defining_ids=$(awk '
  /^\| # \| Status \| Item \| Where \| Sev \| Evidence \|/ { intable = 1; next }
  /^\| # \| Item \| Where \| Evidence \|/                     { intable = 1; next }
  /^\| # \| Item \| What a number closes \|/                   { intable = 1; next }
  /^\| # \| Item \| Commit \|/                                 { intable = 0; next }
  intable && $0 !~ /^\|/ { next }
  intable && $0 ~ /^\|---/ { next }
  intable {
    n = $0; gsub(/[~*`]/, "", n); split(n, cells, "|")
    num = cells[2]; gsub(/^[ \t]+|[ \t]+$/, "", num)
    if (num ~ /^[0-9]+$/) print num
  }
' "$OPEN_ITEMS")

dupes=$(printf '%s\n' "$defining_ids" | grep -c . >/dev/null && printf '%s\n' "$defining_ids" | sort -n | uniq -d | tr '\n' ' ' | sed 's/ $//')
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
# ANCHORED ON THE `**open: ...**` MARKER, the same convention docs/README.md
# uses -- not on "every digit after the words Status column".
#
# That looser form was live until 2026-09-02 and it silently ate anything else
# numeric in the cell. Adding the founder-decision marker to this row made it
# read `**taken: D1 D2**` as items 1 and 2 being open, and the failure was a
# confident, specific, entirely wrong disagreement report. A parser whose input
# is a whole prose cell will eventually be handed prose.
if ! printf '%s' "$claim_line" | grep -q '\*\*open: '; then
  echo "docs-claims: FAIL -- $BACKLOG:${claim_line%%:*} has no '**open: ...**' marker."
  echo "  That marker is how this checker reads the claim. Other text in the cell is"
  echo "  prose and is deliberately not parsed. Add the marker, or delete this checker"
  echo "  deliberately."
  exit 1
fi
claimed=$(printf '%s' "$claim_line" | grep -oE '\*\*open: [^*]+\*\*' | grep -oE '[0-9]+' | sort -n | tr '\n' ' ' | sed 's/ $//')

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
# REQUIRED, not "checked if present". Guarding this with `[ -f ]` meant deleting
# the file skipped the comparison and still printed agreement -- the gate
# asserting it agrees with something it never opened, which is the inverse of the
# rule applied to BACKLOG's claim row three lines up.
if [ ! -f "$README" ]; then
  echo "docs-claims: FAIL -- $README does not exist."
  echo "  It is one of the three registers this checker compares. Restore it, or"
  echo "  remove it from this script deliberately."
  exit 1
fi
if true; then
  readme_line=$(grep -n '\*\*open: ' "$README" | head -1 || true)
  markers=$(grep -c '\*\*open: ' "$README" || true)
  # A11: more than one marker means the ones after the first are never compared
  # and can go stale indefinitely -- the failure this check was added for.
  if [ "$markers" -gt 1 ]; then
    echo "docs-claims: FAIL -- $README has $markers '**open: ...**' markers; only the first is compared."
    echo "  The others would drift unchecked. Keep exactly one."
    exit 1
  fi
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

# ============================================================================
# THE FOURTH CLAIM: which founder decisions are taken.
#
# Added 2026-09-02, after the same failure this whole script exists for turned
# up on a claim it was not watching. `docs/DECISION_PACK.md` stamped all eight
# rulings TAKEN in its Status column on 2026-08-12. Three weeks later:
#
#   docs/README.md      "Seven open, D4 taken"
#   docs/OPEN_ITEMS.md  "None is taken on the founder's behalf"
#   BACKLOG.md Tier 0   B2 and B4 still listed as blockers, under a heading
#                       reading "nothing else moves until these do"
#
# while BACKLOG.md's own reading-order table, sixty lines below Tier 0, said
# "all eight taken". Four restatements of one fact, three of them wrong, one
# file disagreeing with itself.
#
# This direction is the expensive one and it is the opposite of the bug-register
# case. An OPEN item wrongly listed as fixed hides a defect; a TAKEN decision
# wrongly listed as open reports the PROJECT BLOCKED ON A PERSON when it is not,
# in the table someone opens to decide what to work on. Nobody works on anything
# while they believe Tier 0.
#
# DECISION_PACK.md's Status column is the authority -- it is where the rulings
# are recorded -- and the three registers restate it. Same shape as the block
# above, same marker convention, and required in all three for the same reason:
# a claim that cannot be located is not a claim that has been checked.
PACK="${DOCS_CLAIMS_PACK:-docs/DECISION_PACK.md}"
PACK_FLOOR="${DOCS_CLAIMS_PACK_FLOOR:-8}"

if [ ! -f "$PACK" ]; then
  echo "docs-claims: FAIL -- $PACK does not exist."
  echo "  It is the authority for which founder decisions are taken. Restore it,"
  echo "  or remove this section deliberately."
  exit 1
fi

pack_parsed=$(awk '
  /^\| # \| Decision \| Status \|/ { intable = 1; next }
  intable && $0 !~ /^\|/ { intable = 0 }
  intable && $0 ~ /^\|---/ { next }
  intable {
    n = $0; gsub(/[~*`]/, "", n); split(n, cells, "|")
    id = cells[2]; status = cells[4]
    gsub(/^[ \t]+|[ \t]+$/, "", id); gsub(/^[ \t]+|[ \t]+$/, "", status)
    if (id ~ /^D[0-9]+$/) print id, (status ~ /^TAKEN/) ? "taken" : "open"
  }
' "$PACK")

pack_total=$(printf '%s\n' "$pack_parsed" | grep -c . || true)

# Same anti-vacuity rule as the register above: zero parsed decisions must never
# read as "the empty set agrees with the empty set".
if [ "$pack_total" -lt "$PACK_FLOOR" ]; then
  echo "docs-claims: FAIL -- parsed only $pack_total decisions from $PACK (floor $PACK_FLOOR)."
  echo "  The decision table moved and this checker went blind. Fix the parser, do not lower the floor."
  exit 1
fi

pack_taken=$(printf '%s\n' "$pack_parsed" | awk '$2 == "taken" { print $1 }' | sort -V | tr '\n' ' ' | sed 's/ $//')

for reg in "$BACKLOG" "$README" "$OPEN_ITEMS"; do
  n_markers=$(grep -c '\*\*taken: ' "$reg" || true)
  if [ "$n_markers" -eq 0 ]; then
    echo "docs-claims: FAIL -- no '**taken: ...**' marker in $reg."
    echo "  All three registers restate which founder decisions are taken; without the"
    echo "  marker this checker cannot compare $reg against $PACK."
    exit 1
  fi
  if [ "$n_markers" -gt 1 ]; then
    echo "docs-claims: FAIL -- $reg has $n_markers '**taken: ...**' markers; only the first is compared."
    echo "  The others would drift unchecked. Keep exactly one."
    exit 1
  fi
  marker_line=$(grep -n '\*\*taken: ' "$reg" | head -1)
  marker_no=${marker_line%%:*}
  reg_taken=$(printf '%s' "$marker_line" | grep -oE '\*\*taken: [^*]+\*\*' | grep -oE 'D[0-9]+' | sort -V | tr '\n' ' ' | sed 's/ $//')
  if [ "$reg_taken" != "$pack_taken" ]; then
    echo "docs-claims: FAIL -- $reg:$marker_no and $PACK disagree about the founder decisions."
    echo "  $reg says taken: ${reg_taken:-none}"
    echo "  $PACK Status column: ${pack_taken:-none}"
    for d in $pack_taken; do
      case " $reg_taken " in *" $d "*) ;; *)
        echo "  -> $d: $PACK says TAKEN, $reg does not (THE EXPENSIVE DIRECTION -- a settled question is reported as blocking work)";;
      esac
    done
    for d in $reg_taken; do
      case " $pack_taken " in *" $d "*) ;; *)
        echo "  -> $d: $reg says taken, $PACK does not (a ruling is being assumed that nobody made)";;
      esac
    done
    exit 1
  fi
done

if [ "$claimed" = "$actual_open" ]; then
  echo "docs-claims: BACKLOG.md:$lineno, $README:${readme_no:-?} and $OPEN_ITEMS agree (open: ${actual_open:-none}, $total items checked)"
  echo "docs-claims: all three registers agree with $PACK (taken: ${pack_taken:-none}, $pack_total decisions checked)"
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
