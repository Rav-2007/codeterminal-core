"""What a round has spent so far: the sum of every trial's bill in the results of the arms named.

    python3 -I spent.py <arms-file>        prints dollars, e.g. 12.3456
"""
import glob, json, os, sys

HOME = os.environ.get("REALREPO_HOME", os.path.expanduser("~/.cache/mochiii-realrepo"))
total = 0.0
for line in open(sys.argv[1]):
    arm = line.split("#")[0].split()[:1]
    for p in glob.glob(os.path.join(HOME, arm[0] + ".try*.json")) if arm else []:
        try:
            total += sum(t.get("usd", 0) for t in json.load(open(p)))
        except (OSError, ValueError):
            pass
print(f"{total:.4f}")
