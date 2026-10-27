#!/bin/sh
# baseline_tree.sh <commit> <dir> -- the tree Round 6's arm A runs: Mochiii at <commit>, the main that one
# view merged onto, as it was -- plus the eval and the two files it measures with, from this checkout
# (daemon/turnevidence.go and daemon/shellline.go, which nothing in <commit> calls). So arm A builds the
# eval at all, and is measured by the same definitions as the other arms; Mochiii's record under the
# answer, which <commit> did not write, stays out (daemon/realrepo_record_eval_test.go is not copied).
#
#   ./baseline_tree.sh <main before one view> ~/Neww-main
#
# and in the arms file:  A REALREPO_SRC=/home/u/Neww-main
set -eu
[ $# -eq 2 ] || { echo "usage: $0 <commit> <new dir>" >&2; exit 2; }
src=$(cd "$(dirname "$0")/../.." && pwd)
[ ! -e "$2" ] || { echo "$2 already exists: give a new folder" >&2; exit 2; }
if git -C "$src" cat-file -e "$1:daemon/runplace.go" 2>/dev/null; then
  echo "$1 already has one view (daemon/runplace.go): arm A is the main it merged onto" >&2
  exit 2
fi
mkdir -p "$2"
git -C "$src" archive "$1" | tar -x -C "$2"
for f in daemon/turnevidence.go daemon/shellline.go daemon/realrepo_eval_test.go daemon/tasksuccess_eval_test.go; do
  cp "$src/$f" "$2/$f"
done
# Before anything is spent: the eval must build in that tree.
(cd "$2/daemon" && go vet -tags eval .)
echo "arm A: $2 is Mochiii $(git -C "$src" rev-parse --short "$1") with the eval"
