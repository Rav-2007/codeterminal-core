#!/usr/bin/env bash
# Index completeness: every document in the corpus must be reachable from
# docs/README.md.
#
# WHY THIS EXISTS. docs/README.md carried this warning for weeks:
#
#   "This index is INCOMPLETE ... Nothing caught it because nothing can:
#    scripts/docs-links.sh verifies that links which EXIST resolve, and a file
#    that was never linked has no link to check."
#
# The second sentence is true of docs-links.sh and false as a general claim, and
# this gate is the counter-example. A link checker walks the index and asks
# whether each member is real. This walks the DIRECTORY and asks whether each
# member is listed. The two are not the same question, and only the second one
# can see an omission.
#
# It is the R1.16 shape stated in that same warning -- "a gate whose expected
# set is derived from the list it validates cannot detect a member that was
# never added" -- and the fix is the one this repository keeps arriving at:
# derive the expected set from the TREE, which nobody curates, rather than from
# the list, which is the thing under test.
#
# When it fired for real, the index named 33 of 69 documents while claiming
# "roughly a third" was missing. It was more than half.
#
# WHAT IT DELIBERATELY DOES NOT CHECK: whether an entry DESCRIBES its document
# correctly. A row can point at the right file and say something false about it,
# exactly as docs-coderefs can prove a line exists without proving it says what
# the citation claims. Listedness is what rots silently when a file is added;
# accuracy rots loudly, when someone reads it.
set -uo pipefail
cd "$(dirname "$0")/.."

INDEX=docs/README.md
[ -r "$INDEX" ] || { echo "docs-index: no $INDEX to check" >&2; exit 1; }

# The links the index actually offers, normalised to repo-relative paths.
# `../X.md` from docs/ is `X.md`; a bare `X.md` is `docs/X.md`.
linked=$(grep -oE '\]\([^)]+\)' "$INDEX" \
  | sed -E 's/^\]\(//; s/\)$//; s/[[:space:]]+"[^"]*"$//; s/#.*$//' \
  | grep -E '\.md$' \
  | sed -E 's#^\.\./##; t root; s#^#docs/#; :root' \
  | sort -u)

# The corpus, derived from the tree rather than from the index under test.
#
# SCOPE: docs/ plus the root-level design documents. A README that sits beside
# the code it describes -- clients/vscode/, proxy/, mcp-servers/ -- is correct
# structure rather than an unindexed document, and so are the design notes kept
# with their subsystem. Those are listed in the index for discoverability and
# are deliberately NOT required to be, because the gate would then be an opinion
# about where a file should live rather than a check that the index is whole.
#
# Tracked files only: an untracked draft is not yet part of the corpus, and
# failing on it would make the gate fire at work in progress.
corpus=$(git ls-files ':(glob)*.md' ':(glob)docs/**/*.md' | sort -u)

# ANTI-VACUITY. If the enumeration breaks, every document is trivially listed
# and this gate reports clean forever -- the exact failure it exists to prevent,
# one level up.
n_corpus=$(printf '%s\n' "$corpus" | grep -c . || true)
n_linked=$(printf '%s\n' "$linked" | grep -c . || true)
if [ "$n_corpus" -lt 50 ]; then
  echo "docs-index: found only ${n_corpus} document(s); the enumeration is broken and a" >&2
  echo "            pass here would mean nothing. Failing rather than reporting clean." >&2
  exit 1
fi
if [ "$n_linked" -lt 20 ]; then
  echo "docs-index: extracted only ${n_linked} link(s) from ${INDEX}; the extractor is broken." >&2
  exit 1
fi

missing=0
while IFS= read -r doc; do
  [ -z "$doc" ] && continue
  # The index does not list itself, and the front door is linked FROM it rather
  # than being an entry in it.
  [ "$doc" = "$INDEX" ] && continue
  if ! printf '%s\n' "$linked" | grep -qxF "$doc"; then
    echo "  unlisted: ${doc}"
    missing=$((missing + 1))
  fi
done <<< "$corpus"

if [ "$missing" -gt 0 ]; then
  echo "docs-index: ${missing} document(s) above are in the corpus and not reachable from"
  echo "            ${INDEX}. A document nobody links is a document nobody reads, and it"
  echo "            is the one that goes stale. Add a row for it, or delete the file."
  exit 1
fi

# THE COUNT IN THE HEADER IS A CLAIM, SO IT IS CHECKED.
#
# This one page has carried a wrong count four times: "44 markdown files" while
# the directory held 68; the correction written as 67 without re-counting, in
# the commit that corrected it; then "roughly a third is missing" when it was
# more than half. Every one of them was typed from memory, and nothing could
# disagree with them because nothing else knew the number.
#
# Now something does. The header states a total; this gate counts one; they must
# match. That is ENGINEERING_METHOD's "a stated count is not a counted count"
# turned into a build failure instead of a maxim.
stated=$(grep -oE '^\*\*[0-9]+ documents:' "$INDEX" | head -1 | grep -oE '[0-9]+' || true)
if [ -z "$stated" ]; then
  echo "docs-index: ${INDEX} states no document count." >&2
  echo "            Its header must read '**N documents: ...**' so the number can be" >&2
  echo "            checked. A page that states no count cannot be caught miscounting." >&2
  exit 1
fi
if [ "$stated" -ne "$n_corpus" ]; then
  echo "docs-index: ${INDEX} says ${stated} document(s); the tree holds ${n_corpus}." >&2
  echo "            A stated count is not a counted count. Re-derive it, do not adjust it." >&2
  exit 1
fi

# One self-contained line first: .githooks/pre-push summarises this gate as
# `head -1`, and docs-links.sh has already been through the version of this
# mistake where the summary was a fragment.
echo "docs-index: ${n_corpus} document(s), all reachable from ${INDEX}, which states ${stated}"
echo "docs-index: NOT checked -- whether any row DESCRIBES its document correctly, the"
echo "docs-index:                section it sits under, or any .md kept beside its own code"
echo "docs-index:                (clients/, proxy/, daemon/, mcp-servers/, .cursor/)."
