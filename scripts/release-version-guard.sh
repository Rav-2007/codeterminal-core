#!/usr/bin/env bash
# ------------------------------------------------------------------------------
# THE VERSION A RELEASE CLAIMS MUST BE THE VERSION IT IS.
#
# WHAT WAS WRONG, measured 2026-09-17. Three sources named a version and nothing
# compared them:
#
#   git tag                          v0.0.1
#   clients/vscode/package.json      0.0.1
#   daemon/server.go daemonVersion   "0.1.0-skeleton"   <- a const, never stamped
#
# and `grep -c ldflags .github/workflows/release.yml` was 0. So a v0.0.2 tag
# would have produced a .vsix labelled 0.0.1 containing a daemon that told every
# client, and every support question, that it was a pre-release skeleton.
#
# WHAT THIS CHECKS, and only this: on a v* tag, the tag and package.json agree.
# The daemon's own string is no longer a third opinion -- release.yml stamps it
# from the tag via -ldflags -X, so there is nothing left to disagree.
#
# THE EXEMPTION AT BIRTH, stated rather than buried. On workflow_dispatch there
# is no tag, so there is no assertion to make and this exits 0 having checked
# nothing -- and SAYS SO, rather than printing a pass. That is narrow and
# forced by the event (no tag exists, so no comparison is possible), not a
# carve-out for a case someone found inconvenient. A dispatch build stamps
# 0.0.0-dev.<sha> precisely so it cannot be mistaken for a release.
#
# WHY A SCRIPT AND NOT AN INLINE `run:`. Same reason as the other two guards:
# logic in a workflow cannot be unit-tested or neutered. --self-test runs on
# every push via .github/workflows/gates.yml and locally via `make check`.
#
#   release-version-guard.sh --ref <git-ref> --package-json <path>
#   release-version-guard.sh --self-test
# ------------------------------------------------------------------------------
set -uo pipefail

err()  { echo "[release-version-guard] $*" >&2; }
note() { echo "[release-version-guard] $*"; }

# read_package_version prints the "version" field of a package.json.
#
# sed rather than node, because this runs in the `guard` job, which sets up Go
# and not Node -- and adding a Node toolchain to read one string is a lot of
# moving parts for a guard whose whole job is to be simpler than what it guards.
read_package_version() {
  sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" | head -1
}

run_guard() { # run_guard <ref> <package.json path>
  local ref="$1" pkg="$2"

  if [ -z "$ref" ]; then
    err "FAIL: no ref given. Pass --ref, filled from github.ref."
    return 2
  fi

  case "$ref" in
    refs/tags/v*) ;;
    *)
      note "ok -- '$ref' is not a v* tag, so there is no released version to"
      note "      agree with. NOT CHECKED, deliberately: a dispatch has no tag,"
      note "      and release.yml stamps it 0.0.0-dev.<sha> so it cannot be"
      note "      mistaken for a release."
      return 0
      ;;
  esac

  if [ ! -f "$pkg" ]; then
    err "FAIL: $pkg does not exist. A guard that cannot read the file it is"
    err "      comparing cannot clear it."
    return 2
  fi

  local tag_version pkg_version
  tag_version="${ref#refs/tags/v}"
  pkg_version="$(read_package_version "$pkg")"

  if [ -z "$pkg_version" ]; then
    err "FAIL: no \"version\" field found in $pkg. NOT waved through -- an"
    err "      unreadable version is a broken guard, and a broken guard blocks"
    err "      the release."
    return 2
  fi

  if [ "$tag_version" != "$pkg_version" ]; then
    err "FAIL: the tag says $tag_version and $pkg says $pkg_version."
    err ""
    err "  The .vsix filename, the marketplace listing and the extension's own"
    err "  reported version all come from package.json. Shipping them under a"
    err "  tag that says something else means every bug report cites a version"
    err "  that does not identify the code it came from."
    err ""
    err "  FIX: set \"version\": \"$tag_version\" in $pkg, commit, and re-tag."
    return 1
  fi

  note "ok -- tag v$tag_version and $pkg agree"
  note "NOT checked: whether the code is correct, whether CI went green, or"
  note "             whether the daemon's stamped version matches -- that one"
  note "             is stamped FROM the tag by release.yml rather than"
  note "             compared against it, so it cannot disagree."
  return 0
}

self_test() {
  local pass=0 fail=0 tmp out rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  check() { # check <description> <expected-rc> <ref> <pkg>
    local desc="$1" want="$2" ref="$3" pkg="$4"
    out="$(run_guard "$ref" "$pkg" 2>&1)"; rc=$?
    if [ "$rc" -eq "$want" ]; then
      echo "  ok    $desc"; pass=$((pass + 1))
    else
      echo "  FAIL  $desc (rc=$rc, want $want)"; echo "$out" | sed 's/^/        /'
      fail=$((fail + 1))
    fi
  }
  contains() { # contains <description> <needle>
    if echo "$out" | grep -qF -- "$2"; then
      echo "  ok      $1"; pass=$((pass + 1))
    else
      echo "  FAIL    $1 -- output did not contain: $2"; fail=$((fail + 1))
    fi
  }

  echo "release-version-guard self-test"

  printf '{\n  "name": "x",\n  "version": "1.2.3",\n  "publisher": "y"\n}\n' > "$tmp/match.json"
  printf '{\n  "name": "x",\n  "version": "9.9.9"\n}\n'                      > "$tmp/mismatch.json"
  printf '{\n  "name": "x"\n}\n'                                            > "$tmp/noversion.json"

  check "tag matching package.json -> green"        0 "refs/tags/v1.2.3" "$tmp/match.json"
  check "tag DISAGREEING -> FAILS"                  1 "refs/tags/v1.2.3" "$tmp/mismatch.json"
  contains "names both versions"                      "the tag says 1.2.3"
  contains "names the file"                           "$tmp/mismatch.json"
  contains "says what breaks"                         "every bug report cites a version"
  contains "names the remedy"                         "and re-tag"
  check "a package.json with no version -> FAILS"   2 "refs/tags/v1.2.3" "$tmp/noversion.json"
  check "a missing package.json -> FAILS"           2 "refs/tags/v1.2.3" "$tmp/nope.json"
  check "an empty ref -> FAILS rather than passing" 2 ""                 "$tmp/match.json"

  # The exemption, asserted rather than assumed: it must exit 0 AND say it
  # checked nothing. A guard that is silent about not checking is how a
  # dispatch-shaped hole becomes invisible.
  check "dispatch (branch ref) exits 0"             0 "refs/heads/main"  "$tmp/mismatch.json"
  contains "and SAYS it did not check"                "NOT CHECKED, deliberately"
  check "a non-v tag is not a release either"       0 "refs/tags/nightly" "$tmp/mismatch.json"

  # Prefix confusion: v1.2.3 must not satisfy a v1.2.30 tag, or any tag whose
  # version is a prefix of another.
  printf '{\n  "version": "1.2.30"\n}\n' > "$tmp/long.json"
  check "v1.2.3 does not match 1.2.30"              1 "refs/tags/v1.2.3" "$tmp/long.json"
  check "v1.2.30 matches 1.2.30"                    0 "refs/tags/v1.2.30" "$tmp/long.json"

  # A pre-release tag is a real tag and must be compared, not waved through.
  printf '{\n  "version": "2.0.0-rc.1"\n}\n' > "$tmp/rc.json"
  check "a pre-release tag is still compared"       0 "refs/tags/v2.0.0-rc.1" "$tmp/rc.json"
  check "and still fails when it disagrees"         1 "refs/tags/v2.0.0-rc.2" "$tmp/rc.json"

  echo "release-version-guard: $pass passed, $fail failed"
  echo "NOT COVERED HERE, and not claimed: that the daemon binary actually"
  echo "  carries the stamped string. That is release.yml's -ldflags and the"
  echo "  Go linker, not this script; daemon/server.go's comment states the"
  echo "  contract and the default is \"dev\" so an unstamped build is obvious."
  [ "$fail" -eq 0 ]
}

# ------------------------------------------------------------------------------
REF=""; PKG="clients/vscode/package.json"
case "${1:-}" in
  --self-test) self_test; exit $? ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)          REF="$2"; shift 2 ;;
    --package-json) PKG="$2"; shift 2 ;;
    *) err "unknown argument: $1"; exit 2 ;;
  esac
done
if [ -z "$REF" ]; then
  err "usage: $0 --ref <git-ref> [--package-json <path>]"
  err "   or: $0 --self-test"
  exit 2
fi
run_guard "$REF" "$PKG"
exit $?
