#!/usr/bin/env bash
# ------------------------------------------------------------------------------
# WHAT THIS CHANGE DID TO THE LOCATE EVAL, QUESTION BY QUESTION.
#
# WHY. The locate eval's corpus is the whole repository, so its absolute floors
# move with every commit, not only with retrieval code. On 2026-10-16 main sat
# at 29/49 retrieved against a floor of 30 -- lines added above one answer had
# moved it across a chunk boundary -- and every PR that ran the eval went red
# for a loss it did not cause. A gate that is red for everyone is a gate nobody
# reads.
#
# WHAT. retrieval-eval.yml runs the eval twice on ONE runner: the base tree
# (its own ranker, its own questions, its own corpus), then the change. On one
# machine identical text embeds to identical vectors (TestEmbeddingIsDeterministic,
# TestAChunksVectorDoesNotDependOnItsBatch), so every flip between the two logs
# was caused by the change -- none of the up-to-three-question noise between
# two runners (see evalFusionLossSlack). This script reads both logs and:
#
#   - lists every question that flipped, retrieved or delivered, gated or
#     held-out, by its text, so the author sees WHICH answer moved;
#   - fails on a NET LOSS in the gated set (the HYBRID pass): losing more
#     retrieved, or more delivered, than the change gained. Churn that nets out
#     stays green, by the owner's decision (2026-10-17), and is still listed.
#
# Questions are matched by their exact quoted text. One that exists on only one
# side (added, removed or reworded) is listed and never counted.
#
#   eval-delta.sh <base.log> <head.log>   markdown on stdout; exit 1 = net loss,
#                                         exit 2 = a log the eval did not finish
#   eval-delta.sh --self-test
# ------------------------------------------------------------------------------
set -uo pipefail

# extract prints one line per question: section, retrieved (1/0), delivered
# (1/0), number, quoted text. Sections: gated (HYBRID) and heldout (HELD-OUT);
# the SEMANTIC-ONLY pass is a diagnostic and is skipped.
extract() {
  awk '
    /^=== Retrieval ranking eval: / {
      sec = ""
      if (index($0, ": HYBRID")) sec = "gated"
      else if (index($0, ": HELD-OUT")) sec = "heldout"
      q = ""
      next
    }
    sec != "" && /^[0-9]+\. \[[^]]*\] query="/ {
      n = $0; sub(/\..*/, "", n)
      q = $0; sub(/^[0-9]+\. \[[^]]*\] query=/, "", q)
      next
    }
    sec != "" && q != "" && /^   chunk-level=/ {
      r = $0; sub(/^   chunk-level=/, "", r); sub(/ .*/, "", r)
      d = $0; sub(/.*DELIVERED=/, "", d); sub(/ .*/, "", d)
      printf "%s\t%d\t%d\t%s\t%s\n", sec, (r == "hit"), (d == "true"), n, q
      q = ""
    }
  ' "$1"
}

compare() { # compare <base.log> <head.log>
  local base head side log
  for side in base head; do
    log=$1
    [ "$side" = head ] && log=$2
    if [ ! -f "$log" ]; then
      echo "eval-delta: there is no $side log at $log"
      return 2
    fi
    if ! grep -q '^EVALTREND ' "$log"; then
      echo "eval-delta: the $side log has no EVALTREND line -- that eval did not finish, so there is nothing to compare"
      return 2
    fi
  done
  base="$(extract "$1")" head="$(extract "$2")"
  if ! grep -q '^gated' <<<"$base" || ! grep -q '^gated' <<<"$head"; then
    echo "eval-delta: a log has no HYBRID questions -- the per-question output this reads has changed shape"
    return 2
  fi

  awk -F'\t' '
    FNR == 1 { side++ }
    {
      k = $1 SUBSEP $5
      if (side == 1) { br[k] = $2; bd[k] = $3; bseen[k] = 1; if ($1 == "gated") { bt++; bR += $2; bD += $3 } }
      else { hr[k] = $2; hd[k] = $3; hn[k] = $4; hseen[k] = 1; order[++m] = k
             if ($1 == "gated") { ht++; hR += $2; hD += $3 } }
    }
    function arrow(a, b) { return (a ? "yes" : "no") " -> " (b ? "yes" : "no") }
    END {
      printf "| | retrieved | delivered |\n|---|---|---|\n"
      printf "| base | %d/%d | %d/%d |\n| this change | %d/%d | %d/%d |\n\n", bR, bt, bD, bt, hR, ht, hD, ht
      rows = ""
      for (i = 1; i <= m; i++) {
        k = order[i]; split(k, p, SUBSEP)
        if (!(k in bseen)) { rows = rows sprintf("| %s | %s | %s | new question, not counted | |\n", p[1], hn[k], p[2]); continue }
        if (br[k] == hr[k] && bd[k] == hd[k]) continue
        rows = rows sprintf("| %s | %s | %s | %s | %s |\n", p[1], hn[k], p[2], arrow(br[k], hr[k]), arrow(bd[k], hd[k]))
        if (p[1] != "gated") continue
        if (br[k] && !hr[k]) lostR++; if (!br[k] && hr[k]) gainR++
        if (bd[k] && !hd[k]) lostD++; if (!bd[k] && hd[k]) gainD++
      }
      for (k in bseen) if (!(k in hseen)) { split(k, p, SUBSEP); rows = rows sprintf("| %s | | %s | removed, not counted | |\n", p[1], p[2]) }
      if (rows == "") print "No question changed."
      else printf "| set | # | question | retrieved | delivered |\n|---|---|---|---|---|\n%s", rows
      netR = gainR - lostR; netD = gainD - lostD
      printf "\nGated set: retrieved %+d (lost %d, gained %d), delivered %+d (lost %d, gained %d).\n", netR, lostR, gainR, netD, lostD, gainD
      if (netR < 0 || netD < 0) { print "NET LOSS -- this change answers fewer of the gated questions than its base did."; exit 1 }
      print "No net loss."
    }
  ' <(printf '%s\n' "$base") <(printf '%s\n' "$head")
}

# ---------------------------------------------------------------- self-test --

# fixture prints a log in the eval's own shape. Arguments are triples of
# "question" retrieved delivered for the gated pass, then -- and held-out triples.
# The questions are invented: an indexed file that quoted a real eval question
# would hand the corpus its own answer key (TestNoIndexedFileEchoesAnEvalQuery).
fixture() {
  local n=0 sec=HYBRID
  echo "=== Retrieval ranking eval: SEMANTIC-ONLY (lexicalStore=nil, today's behavior) ==="
  echo
  echo '1. [impl] query="where is the alpha widget wound"'
  echo "   chunk-level=MISS file-level=false merged-path=false DELIVERED=false"
  echo "=== Retrieval ranking eval: HYBRID (semantic + lexical, fused via RRF) ==="
  while [ $# -gt 0 ]; do
    if [ "$1" = -- ]; then
      echo "=== Retrieval ranking eval: HELD-OUT (hybrid; never used to choose a policy) ==="
      sec=HELD-OUT n=0; shift; continue
    fi
    n=$((n + 1))
    local mark=MISS; [ "$2" = 1 ] && mark=hit
    local del=false; [ "$3" = 1 ] && del=true
    echo
    echo "$n. [impl] query=\"$1\""
    echo "   expected files=[x.go] exact chunks=[x.go:1-40]"
    echo "   chunk-level=$mark file-level=true merged-path=true DELIVERED=$del"
    echo "   1. x.go:1-40                               class=code   raw=0.5000 weighted=0.5000"
    shift 3
  done
  echo "EVALTREND chunks=1 files=1 semantic=0 retrieved=0 budgeted_out=0 delivered=0 total=0 fingerprint=x"
}

self_test() {
  local dir fails=0
  dir="$(mktemp -d)"
  trap 'rm -rf "$dir"' RETURN
  expect() { # expect <name> <want-exit> <want-text> <base-args...> == <head-args...>
    local name=$1 want=$2 text=$3; shift 3
    local b=() h=() into=b
    for a in "$@"; do
      if [ "$a" = == ]; then into=h; continue; fi
      if [ $into = b ]; then b+=("$a"); else h+=("$a"); fi
    done
    fixture "${b[@]}" >"$dir/base.log"
    fixture "${h[@]}" >"$dir/head.log"
    local out rc
    out="$(compare "$dir/base.log" "$dir/head.log")"; rc=$?
    if [ "$rc" != "$want" ] || ! grep -qF -- "$text" <<<"$out"; then
      echo "self-test FAIL [$name]: exit $rc (want $want), want text: $text"
      echo "$out" | sed 's/^/    /'
      fails=$((fails + 1))
    fi
  }
  local A="how is the beta gasket sealed" B="where does the gamma pump idle" C="what clears the delta latch"
  expect identical 0 "No question changed."            "$A" 1 1 "$B" 0 1 == "$A" 1 1 "$B" 0 1
  expect lost-retrieved 1 "NET LOSS"                   "$A" 1 1 "$B" 0 1 == "$A" 0 1 "$B" 0 1
  expect lost-shows-which 1 "| gated | 1 | \"$A\" | yes -> no | yes -> yes |" \
                                                       "$A" 1 1 "$B" 0 1 == "$A" 0 1 "$B" 0 1
  expect churn-nets-out 0 "retrieved +0 (lost 1, gained 1)" \
                                                       "$A" 1 1 "$B" 0 1 == "$A" 0 1 "$B" 1 1
  expect lost-delivered 1 "delivered -1"               "$A" 1 1 "$B" 0 1 == "$A" 1 0 "$B" 0 1
  expect gain-only 0 "retrieved +1"                    "$A" 0 0 "$B" 0 1 == "$A" 1 1 "$B" 0 1
  expect heldout-not-gated 0 "| heldout | 1 | \"$C\" | yes -> no |" \
                                                       "$A" 1 1 -- "$C" 1 1 == "$A" 1 1 -- "$C" 0 0
  expect new-not-counted 0 "new question, not counted" "$A" 1 1 == "$A" 1 1 "$C" 0 0
  expect removed-not-counted 0 "removed, not counted"  "$A" 1 1 "$C" 1 1 == "$A" 1 1

  # A log the eval did not finish must never read as "no net loss".
  fixture "$A" 1 1 >"$dir/base.log"
  fixture "$A" 1 1 | grep -v '^EVALTREND' >"$dir/head.log"
  if compare "$dir/base.log" "$dir/head.log" >/dev/null; then
    echo "self-test FAIL [unfinished-head]: a head log with no EVALTREND line passed"; fails=$((fails + 1))
  elif [ "$(compare "$dir/base.log" "$dir/head.log" >/dev/null; echo $?)" != 2 ]; then
    echo "self-test FAIL [unfinished-head]: want exit 2"; fails=$((fails + 1))
  fi
  # The semantic-only pass is a diagnostic: a flip there is not this script's business.
  fixture "$A" 1 1 | sed 's/^   chunk-level=MISS file-level=false merged-path=false DELIVERED=false$/   chunk-level=hit file-level=true merged-path=true DELIVERED=true/' >"$dir/head.log"
  fixture "$A" 1 1 >"$dir/base.log"
  if ! compare "$dir/base.log" "$dir/head.log" | grep -q "No question changed."; then
    echo "self-test FAIL [semantic-only-ignored]: a SEMANTIC-ONLY flip was counted"; fails=$((fails + 1))
  fi

  if [ "$fails" -ne 0 ]; then echo "eval-delta self-test: $fails case(s) failed"; return 1; fi
  echo "eval-delta self-test: 11 cases ok"
}

case "${1:-}" in
  --self-test) self_test ;;
  *)
    if [ $# -ne 2 ]; then
      echo "usage: eval-delta.sh <base.log> <head.log> | --self-test" >&2
      exit 2
    fi
    compare "$1" "$2"
    ;;
esac
