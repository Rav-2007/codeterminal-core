"""Side by side: the arms of a real-repo round, by the metrics registered before it ran.

    python3 -I compare.py <arm> [<arm> ...]

An arm's trials are every results file $REALREPO_HOME/<arm>*.json (each try of each task, and the
batches a run was split into, are separate files with the arm's name in front: base.try1.json,
base.try2.s3.json, ...). Each trial counts once: tries are not merged, and an attempt the provider cut
was already run again inside the eval (REALREPO_PROVIDER_RETRIES), so it is reported, never counted.

Per arm (docs/AGENT_WORKFLOW_EVAL.md, Round 6):
  pass rate                 graded by the fix's own hidden tests
  tested on the final       a build or test command passed in the working copy after the last edit
  unsupported claims        the answer said the tests pass; the record did not support it
  changed existing tests    trials whose changes altered existing lines of a test
  changed outside review    trials where a command changed the real project
  $ / tokens per solved     the bill (provider's, or priced: marked ~) over the trials that passed
  provider-ended            trials the provider ended after every retry: reported apart, not failures
"""
import glob, json, os, sys

HOME = os.environ.get("REALREPO_HOME", os.path.expanduser("~/.cache/mochiii-realrepo"))


def trials(arm):
    out = []
    for p in sorted(glob.glob(os.path.join(HOME, arm + "*.json"))):
        try:
            out.extend(json.load(open(p)))
        except (OSError, ValueError):
            print(f"  (skipped {os.path.basename(p)}: not a results file)")
    return out


def summary(arm, ts):
    n = len(ts)
    ended = [t for t in ts if t["outcome"] in ("provider-error", "rate-limited")]
    counted = [t for t in ts if t not in ended]
    passed = [t for t in counted if t["pass"]]
    usd = sum(t.get("usd", 0) for t in ts)
    tokens = sum(t["tokens"] for t in ts)
    est = any(t.get("usd_estimated") for t in ts)
    print(f"\n=== {arm}: {n} trial(s), {len(ended)} provider-ended ===")
    rate = f" ({100 * len(passed) / len(counted):.0f}%)" if counted else ""
    print(f"  pass rate               {len(passed)}/{len(counted)}{rate}")
    print(f"  tested on the final     {sum(bool(t.get('tested_final')) for t in counted)}/{len(counted)}")
    print(f"  unsupported claims      {sum(bool(t.get('claim_unsupported')) for t in counted)}/{len(counted)}")
    print(f"  changed existing tests  {sum(bool(t.get('tests_changed')) for t in counted)}/{len(counted)}")
    print(f"  changed outside review  {sum(bool(t.get('outside_review')) for t in counted)}/{len(counted)}")
    if passed:
        print(f"  $ per solved            {'~' if est else ''}{usd / len(passed):.3f}")
        print(f"  tokens per solved       {tokens / len(passed):,.0f}")
    print(f"  in all                  ${usd:.2f}{' ~' if est else ''}, {tokens:,} tokens,"
          f" {sum(t['seconds'] for t in ts) / 60 / max(n, 1):.1f} min a trial")


def table(ts):
    print(f"\n  {'task':34} {'outcome':14} {'turns':>5} {'calls':>5} {'tokens':>9} {'$':>7} {'final':>5}"
          f" {'claim':>5} {'tests':>5} {'outside':>7}")
    for t in sorted(ts, key=lambda t: t["task"]):
        flag = lambda b, word: word if b else "-"
        print(f"  {t['task']:34} {t['outcome']:14} {t['turns']:>5} {t['model_calls']:>5} {t['tokens']:>9,}"
              f" {t.get('usd', 0):>7.3f} {flag(t.get('tested_final'), 'yes'):>5}"
              f" {flag(t.get('claim_unsupported'), 'BAD'):>5} {len(t.get('tests_changed') or []):>5}"
              f" {len(t.get('outside_review') or []):>7}")
        if t.get("provider_cuts"):
            print(f"  {'':34} ^ the provider cut {len(t['provider_cuts'])} earlier attempt(s), run again")


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    for arm in sys.argv[1:]:
        ts = trials(arm)
        if not ts:
            print(f"\n=== {arm}: no results under {HOME} ===")
            continue
        summary(arm, ts)
        table(ts)


if __name__ == "__main__":
    main()
