#!/bin/sh
# run_round.sh <arms-file> <tries> [task ...] -- a round: every task, <tries> times, every arm in turn on
# each task before the next one starts, so the arms meet the same provider at the same hour. One systemd
# unit, which outlives the session that starts it, and a second that holds sleep off until it ends (a
# closed laptop lid still suspends the machine: keep it open).
#
# <arms-file> has one arm a line: its name, then the environment it runs with, e.g.
#   A REALREPO_SRC=/home/u/Neww-main            (built by baseline_tree.sh)
#   B REALREPO_SRC=/home/u/Neww-oneview
#   D REALREPO_SRC=/home/u/Neww-oneview REALREPO_MODE=fix
# Every arm also gets this shell's REALREPO_MODEL and REALREPO_USD_PER_MTOK_* (prices, for a provider that
# reports no cost). REALREPO_ROUND_USD is the round's hard cap: before each run the round's bill so far
# (spent.py) is added up, and the round stops once it reaches the cap -- so it can overrun by at most one
# trial, which the pilot measures. Results: $REALREPO_HOME/<arm>.try<k>.<task>.json, read by compare.py.
set -e
here=$(dirname "$(readlink -f "$0")")
arms=$(readlink -f "$1") tries=$2; shift 2
home=${REALREPO_HOME:-$HOME/.cache/mochiii-realrepo}
tasks=$*
[ -n "$tasks" ] || tasks=$(ls "$home/tasks" | tr '\n' ' ')  # one line: the loop below is one script
: "${REALREPO_MODEL:?set REALREPO_MODEL to the provider's model id}"
shared="REALREPO_MODEL=$REALREPO_MODEL REALREPO_HOME=$home"
cap=${REALREPO_ROUND_USD:-0}
for v in REALREPO_USD_PER_MTOK_IN REALREPO_USD_PER_MTOK_OUT REALREPO_USD_PER_MTOK_CACHED; do
  eval val=\${$v:-}
  [ -n "$val" ] && shared="$shared $v=$val"
done
script="for k in \$(seq 1 $tries); do for t in $tasks; do grep -v '^#' $arms | while read -r arm env; do"
script="$script [ -n \"\$arm\" ] || continue;"
script="$script if [ \"\$(python3 -I $here/spent.py $arms | awk -v c=$cap 'c>0 && \$1>=c {print 1}')\" = 1 ]; then"
script="$script echo \"round stopped: the cap of $cap dollars is spent\" >>$home/round.log; exit 0; fi;"
script="$script env $shared \$env python3 -I $here/run_eval.py \$arm.try\$k.\$t \$t"
script="$script </dev/null >>$home/\$arm.try\$k.\$t.log 2>&1; done; done; done"  # stdin: the arms list
systemd-run --user --unit=realrepo-round --collect --quiet sh -c "$script"
systemd-run --user --unit=realrepo-keepawake --collect --quiet systemd-inhibit --what=sleep:idle \
  --who="Mochiii real-repo eval" --why="a round runs until every arm has done every task" --mode=block \
  sh -c 'while systemctl --user is-active --quiet realrepo-round; do sleep 30; done' 2>/dev/null || true
echo "started: $(grep -cv '^#' "$arms") arm(s) x $(echo $tasks | wc -w) task(s) x $tries tries"
