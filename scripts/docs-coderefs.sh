#!/usr/bin/env bash
# Every `file.go:LINE` in an ENFORCED document points at a line that exists.
#
# WHY THIS EXISTS. docs/RESIDUAL_RISKS.md's own header says a register whose
# rows have drifted from the code is worse than none. On 2026-09-04 two of its
# four code references had drifted: R1.7 cited clients/tui/slash.go:299-305 for
# a `git status` failure echo that now lives at 319, and R1.5 cited 396-397 for
# a CombinedOutput() at 411. Neither was wrong when written. Both were wrong by
# the time anyone would follow them, and a reader who follows a stale reference
# lands on unrelated code and concludes the row describes something else.
#
# OPT-IN, BY A MARKER IN THE DOCUMENT, and that is a deliberate shape rather
# than laziness. A repo-wide sweep found 22 references that this gate would
# reject, and most are not defects: methodology documents use `widget.go:42` and
# `foo.go:142` as ILLUSTRATIONS, which are not references to anything. The rest
# are bare basenames -- `server.go:687`, `main.go:1061` -- written where the
# module was obvious from context and which no longer resolve on their own.
# Those are real and are reported below as an unenforced-document count, so the
# gap is visible rather than quietly excluded.
#
# A document opts in with this line anywhere in it:
#
#     <!-- coderefs: enforced -->
#
# WHAT IT CAN AND CANNOT CATCH. It catches a reference to a file that no longer
# exists, one whose line is past the end of the file, and a bare basename that
# is ambiguous -- the shapes that make a reference actively misleading rather
# than merely imprecise. It CANNOT catch a line that moved inside a file that is
# still long enough, which is exactly what happened above. For that, cite the
# function: prose naming `runGitStatus` survives every edit that renumbering
# does not, and the register was rewritten to give both.
#
# So it is a floor, not a proof, and worth having as a floor: it makes the
# deletion case impossible and forces whoever renumbers a document to have
# opened the file.
set -uo pipefail
cd "$(dirname "$0")/.."

checked=0
failures=0
exts=""
enforced_docs=0
unenforced_refs=0
unenforced_docs=0

# RESOLVED AGAINST TRACKED FILES, NOT THE WORKING TREE.
#
# This used to be `find .`, which walks whatever happens to be on the machine
# running it. `.mochiii/` is a local runtime directory -- backups, index,
# logs -- that .gitignore excludes and a CI checkout never has. So a bare
# basename resolved to ONE file in CI and to TWO on any machine that had applied
# an edit, and the gate's VERDICT depended on the operator's working directory.
# Measured on 2026-09-15: copying one file into .mochiii/backups/ took this
# gate from exit 0 to exit 1 with no change to any document or any source file.
#
# That is the same family as an environment-dependent coverage floor, and this
# repository has already been burned by that once.
#
# EXCLUDING `.mochiii/` BY NAME WOULD FIX THE INSTANCE AND KEEP THE CLASS.
# `.vscode-test/` holds 6,200 files; there are build outputs, a daemon.exe, and
# whatever the next tool writes. Each would need its own -not -path, added after
# it had already produced a wrong answer once. Asking git what is in the
# repository closes all of them at once and makes a local run answer the same
# question CI's run answers -- which is the only reason to run it locally.
tracked_files=$(git ls-files)
if [ -z "$tracked_files" ]; then
  echo "docs-coderefs: FAIL -- git ls-files returned nothing; this gate cannot resolve anything"
  exit 1
fi

shopt -s nullglob
for doc in docs/*.md docs/**/*.md *.md; do
  [ -f "$doc" ] || continue
  # THE EXTENSION SET IS DERIVED FROM THE CITATIONS, NOT LISTED HERE.
  #
  # This used to end in `\.go:`, so it silently checked ONE file extension.
  # Measured on 2026-09-15: a document carrying references to line 99999 of the
  # extension's .ts, the build .yml and a .sh script passed at exit 0, while a
  # single bad .go reference failed. Every .ts, .yml, .js, .sh and .md citation
  # in this repository's enforced documents had never been checked -- including
  # TRUST_BOUNDARIES.md, whose enforcement was therefore partial and nobody knew.
  #
  # Naming the extensions to add would repeat the defect one generation later:
  # the next document to cite a .toml or a .sql would be unchecked again, and
  # the gap would be invisible exactly as this one was. So the pattern matches
  # ANY extension and the set that gets checked is whatever the documents
  # actually cite. The banner prints that set, so a surprise is visible rather
  # than silent.
  #
  # The extension must START WITH A LETTER, which is what keeps `10.0.0.1:8080`
  # and `v1.2.3:4` out. Resolution is unchanged: tracked files only.
  refs=$(grep -ohE '`[A-Za-z0-9_./-]+\.[A-Za-z][A-Za-z0-9]*:[0-9]+(-[0-9]+)?`' "$doc" 2>/dev/null | tr -d '`' | sort -u)
  [ -n "$refs" ] || continue

  if ! grep -q '<!-- coderefs: enforced -->' "$doc"; then
    unenforced_docs=$((unenforced_docs + 1))
    unenforced_refs=$((unenforced_refs + $(echo "$refs" | wc -l)))
    continue
  fi
  enforced_docs=$((enforced_docs + 1))

  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    path=${ref%%:*}
    lines=${ref#*:}
    first=${lines%%-*}

    if [[ "$path" == */* ]]; then
      file="$path"
      # A reference to an untracked file passes here and fails in CI, which is
      # the same working-directory dependence in its other form.
      if ! grep -qxF -- "$path" <<< "$tracked_files"; then
        echo "docs-coderefs: FAIL $doc -> $ref -- $path is not a tracked file; CI cannot resolve it"
        failures=$((failures + 1))
        continue
      fi
    else
      mapfile -t hits < <(awk -F/ -v b="$path" '$NF == b' <<< "$tracked_files" | sort)
      if [ "${#hits[@]}" -ne 1 ]; then
        echo "docs-coderefs: FAIL $doc -> $ref -- $path resolves to ${#hits[@]} files; write the path from the repo root"
        failures=$((failures + 1))
        continue
      fi
      file="${hits[0]}"
    fi

    if [ ! -f "$file" ]; then
      echo "docs-coderefs: FAIL $doc -> $ref -- $file does not exist"
      failures=$((failures + 1))
      continue
    fi
    n=$(wc -l < "$file")
    if [ "$first" -gt "$n" ]; then
      echo "docs-coderefs: FAIL $doc -> $ref -- $file has only $n lines"
      failures=$((failures + 1))
      continue
    fi
    checked=$((checked + 1))
    exts="$exts ${path##*.}"
  done <<< "$refs"
done

if [ "$enforced_docs" -eq 0 ]; then
  echo "docs-coderefs: FAIL -- no document carries the enforcement marker, so this gate inspected nothing"
  exit 1
fi
if [ "$checked" -eq 0 ] && [ "$failures" -eq 0 ]; then
  echo "docs-coderefs: FAIL -- $enforced_docs enforced document(s) but zero references resolved; the pattern has stopped matching"
  exit 1
fi
if [ "$failures" -gt 0 ]; then
  echo "docs-coderefs: $failures broken reference(s) of $((checked + failures)) checked in $enforced_docs enforced document(s)"
  exit 1
fi

ext_list=$(printf '%s\n' $exts | sort -u | paste -sd, -)
echo "docs-coderefs: $checked reference(s) resolve, across $enforced_docs enforced document(s); $unenforced_refs reference(s) in $unenforced_docs unenforced document(s) NOT checked"
# The extension set is DERIVED, so print it: a gate whose scope is invisible is
# how this one checked only .go for its whole life without anyone noticing.
echo "docs-coderefs: extensions checked (derived from the citations themselves): ${ext_list:-none}"
echo "docs-coderefs: NOT checked -- prose, anchors, section numbers, and any citation written without a :line suffix"
