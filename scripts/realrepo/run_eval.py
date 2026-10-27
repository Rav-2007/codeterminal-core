"""Run TestRealRepoTasks with the user's stored key (read here, never put on a command line).

    python3 -I run_eval.py <out-name> [task,task,...]

Environment: REALREPO_HOME (data: tasks/, tools/, results; default ~/.cache/mochiii-realrepo),
REALREPO_SRC (the Mochiii checkout whose agent is measured; default the one holding this script),
REALREPO_MODEL (the provider's model id), and any of the eval's own REALREPO_* knobs, passed through.
"""
import json, os, sys
RR = os.environ.get("REALREPO_HOME", os.path.expanduser("~/.cache/mochiii-realrepo"))
SRC = os.environ.get("REALREPO_SRC", os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
cred = json.load(open(os.path.expanduser("~/.mochiii/credentials.json")))
env = dict(os.environ)
env.update(
    MOCHIII_API_BASE=cred["api_base"], MOCHIII_API_KEY=cred["api_key"],
    REALREPO_TASKS=os.path.join(RR, "tasks"), REALREPO_MODEL=os.environ["REALREPO_MODEL"],
    REALREPO_OUT=os.path.join(RR, sys.argv[1] + ".json"),
    REALREPO_LOG=os.path.join(RR, sys.argv[1] + ".daemon.log"),
    REALREPO_KEEP=os.path.join(RR, "kept", sys.argv[1]),
    PATH=":".join([os.path.join(RR, "tools/java/bin"), os.path.join(RR, "tools/py/bin"),
                   os.path.expanduser("~/.local/go/bin"), "/usr/local/bin", "/usr/bin", "/bin"]),
)
if len(sys.argv) > 2:
    env["REALREPO_ONLY"] = sys.argv[2]
# Its own temp folder per run: several runs at once must not share /tmp/m2-head-<uid>
# (the mvn wrapper's local repository for run_command) or the agents' scratch files.
tmp = os.path.join("/tmp", "rr-" + sys.argv[1])
os.makedirs(tmp, exist_ok=True)
env["TMPDIR"] = tmp
os.chdir(os.path.join(SRC, "daemon"))
os.execvpe("go", ["go", "test", "-tags", "eval", "-count=1", "-run", "TestRealRepoTasks$", "-v", "-timeout", "900m", "."], env)
