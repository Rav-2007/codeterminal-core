#!/bin/bash
# Builds this fixture's git history in the current directory -- the task's own
# working tree: the OLD files as the first commit, then the workspace's files
# as the commit that broke it. Fixed identities and dates, so every trial sees
# the same history. FIXTURE names this fixture's folder.
set -euo pipefail
F=${FIXTURE:?FIXTURE must name the fixture folder}
export GIT_CONFIG_NOSYSTEM=1 GIT_AUTHOR_NAME=dev GIT_AUTHOR_EMAIL=dev@example.com
export GIT_COMMITTER_NAME=dev GIT_COMMITTER_EMAIL=dev@example.com
export GIT_AUTHOR_DATE=2026-09-01T10:00:00Z GIT_COMMITTER_DATE=2026-09-01T10:00:00Z
cp -r "$F/history/old/." .
git init -q -b main
git add -A
git commit -qm "pricing: shipping rules"
cp -r "$F/workspace/." .
export GIT_AUTHOR_DATE=2026-09-20T10:00:00Z GIT_COMMITTER_DATE=2026-09-20T10:00:00Z
git add -A
git commit -qm "pricing: tidy the shipping rules"
