#!/usr/bin/env bash
# Every GitHub Action must be pinned to a full commit SHA, never a tag.
#
# WHY. `uses: actions/checkout@v4` resolves at run time to whatever commit that
# tag points at TODAY. Tags are mutable: the owner can move one, and an account
# takeover can move one for them. The action then runs inside a workflow holding
# this repository's secrets and its release credentials, with write access to the
# checkout. Nothing in the repo would change, no review would fire, and the diff
# a human looks at would be empty.
#
# That is not hypothetical for third-party actions in particular -- this repo
# uses softprops/action-gh-release in the RELEASE workflow, which is the highest
# value target it has.
#
# F-10 in AGENT_SECURITY_AND_CAPABILITY_AUDIT.md. All 26 uses were floating tags
# until 2026-08-31.
#
# WHY A SCRIPT AND NOT A ONE-TIME FIX. Pinning is a state, not an event: the next
# person to add a job will copy `uses: actions/foo@v1` from any example on the
# internet, and the pins that exist today say nothing about the one they add.
#
# HOW TO BUMP A PIN (the cost of this policy, stated honestly -- Dependabot can
# do it for you if it is ever enabled, and it understands this exact format):
#
#   gh api repos/actions/checkout/git/ref/tags/v4 --jq .object.sha
#   gh api 'repos/actions/checkout/tags?per_page=100' \
#     --jq '.[] | select(.commit.sha=="<sha>") | .name'
#
# then replace the SHA and update the trailing `# vX.Y.Z` comment, which exists
# so a reader can tell at a glance what version they are on.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
found=0

while IFS= read -r line; do
  file="${line%%:*}"
  rest="${line#*:}"
  lineno="${rest%%:*}"
  ref="$(printf '%s' "$rest" | sed 's/^[0-9]*://' | sed 's/.*uses: *//' | sed 's/ *#.*//' | tr -d '\r')"

  # Local composite actions (./.github/actions/...) and docker:// refs are not
  # tag-resolved and have nothing to pin.
  case "$ref" in
    ./*|docker://*) continue ;;
  esac

  found=$((found + 1))
  version="${ref##*@}"
  if ! printf '%s' "$version" | grep -qE '^[0-9a-f]{40}$'; then
    echo "FAIL  $file:$lineno uses $ref"
    echo "      That is a mutable ref. Pin it to a full 40-character commit SHA and"
    echo "      leave the version in a trailing comment: uses: owner/repo@<sha> # v1.2.3"
    fail=1
  fi
done < <(grep -rn 'uses: ' .github/workflows/*.yml 2>/dev/null)

# ANTI-VACUITY. A glob that matched nothing, a renamed directory, or a broken
# parse would print "all pinned" over an empty set and read as a pass. The repo
# has 26 uses; anything under 10 means this check stopped seeing the workflows.
if [ "$found" -lt 10 ]; then
  echo "FAIL  found only $found action reference(s). This check parsed almost nothing,"
  echo "      so its verdict is meaningless. Check .github/workflows/ still exists."
  exit 1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "actions-pinned: $found action reference(s), all pinned to commit SHAs"
