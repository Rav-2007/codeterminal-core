#!/usr/bin/env bash
# ==============================================================================
# release-signing-guard.sh: nothing unsigned reaches a release
# ==============================================================================
#
# WHY THIS EXISTS. On 2026-09-05 release.yml was dispatched for the first time in
# its existence. The run was GREEN and the darwin-arm64 binaries were UNSIGNED:
# the signing step is written to be a no-op until the MACOS_* secrets exist, so
# it reports success for the state in which it did nothing (R1.15). On a real tag
# the `files:` globs -- out-vsix/*.vsix and out-bin/codeterminal-tui-* -- would
# have swept two unsigned darwin assets into the release, with two checksum
# manifests naming them.
#
# THE PREDICATE IS "WAS THIS SIGNED?", NOT "IS THIS DARWIN?". That choice is the
# whole design and it was made deliberately over a hard exclusion:
#
#   - A hard exclusion encodes "we do not ship macOS" in the machinery, when the
#     ruling of 2026-09-05 is that macOS is DEFERRED. Somebody would have to find
#     and revert it the day the secrets land, with nothing that notices if they
#     do not -- a second place the ruling lives, and this repo has been burned by
#     that shape twice (the debt-marker gate that checked one module of six, the
#     register checker that missed a fourth register).
#   - Keyed on signedness instead, DARWIN SHIPS WITH ZERO EDITS the moment the
#     secrets exist: the marker appears, the guard stops excluding, done. And
#     Windows Authenticode later needs no second mechanism here -- its signing
#     step writes SIGNED-win32-x64 and this script already understands it.
#
# TWO BEHAVIOURS, SPLIT ON THE TRIGGER, and the split is what made this
# buildable at all:
#
#   dispatch + marker absent -> EXCLUDE that target's assets, warn loudly, GREEN.
#       A dispatch is a rehearsal and has to stay usable. A guard that turned
#       every branch dispatch red is exactly why this was left unbuilt before.
#   tag + marker absent      -> FAIL the job.
#       A tag is a release. Silently shipping fewer platforms than the tag
#       implies is its own fail-open, so it must be loud.
#
# REJECT BY DEFAULT. Releasable requires the POSITIVE presence of
# SIGNED-<target>. A missing marker, an empty dist, a signing path added later
# that forgets to write one -- all read as unsigned. The script never infers
# "signed" from the absence of an UNSIGNED marker.
#
# USAGE
#   release-signing-guard.sh --self-test
#   release-signing-guard.sh --ref <git-ref> --artifacts <dir> \
#                            --vsix-dir <dir> --bin-dir <dir>
# ==============================================================================
set -uo pipefail

# ------------------------------------------------------------------------------
# WHICH TARGETS MUST BE SIGNED. Every built target must appear in exactly one of
# these two lists; an unclassified target is a bug and is treated as a failure
# rather than defaulted into "no signing needed". Same rule as
# scripts/coverage-floors.txt: adding a target means deciding, even if the
# decision is "none".
# ------------------------------------------------------------------------------
ALL_TARGETS="linux-x64 darwin-arm64 win32-x64"
NEEDS_SIGNING="darwin-arm64"           # Gatekeeper quarantines unsigned Mach-O.
NO_SIGNING_NEEDED="linux-x64 win32-x64" # win32-x64 moves left when Authenticode lands.

fail_count=0
excluded=""

say()  { echo "[signing-guard] $*"; }
warn() { echo "[signing-guard] WARNING: $*" >&2; }
err()  { echo "[signing-guard] ERROR: $*" >&2; }

in_list() {
  local needle="$1" hay="$2" w
  for w in $hay; do [ "$w" = "$needle" ] && return 0; done
  return 1
}

# is_signed <artifacts-dir> <target> -- the only definition of "releasable".
is_signed() {
  [ -f "$1/binaries-$2/SIGNED-$2" ]
}

# unsigned_reason <artifacts-dir> <target> -- diagnostics only.
unsigned_reason() {
  local f="$1/binaries-$2/UNSIGNED-$2"
  if [ -f "$f" ]; then
    sed -n 's/^reason=//p' "$f" | head -1
  else
    echo "no marker written at all"
  fi
}

# ------------------------------------------------------------------------------
# THE FAILURE MESSAGE IS PART OF THE DELIVERABLE.
#
# It fires months from now, on somebody else's release, while they are trying to
# ship. If it says "guard failed" they will delete the guard, because that is
# what anyone under time pressure does with a check they cannot act on. So it
# says which target, that the marker was absent and why, exactly which secrets
# fix it, and -- the part that stops it being deleted -- that this is a decision
# somebody took on purpose and not a broken pipeline.
# ------------------------------------------------------------------------------
explain_tag_failure() {
  local target="$1" reason="$2"
  cat >&2 <<EOF

  ============================================================================
  RELEASE BLOCKED: $target is not signed, and this is deliberate.
  ============================================================================

  This is NOT a broken pipeline. It is a guard that was added on purpose, and
  it is doing the job it was added for.

  WHAT HAPPENED
    No SIGNED-$target marker was produced by the signing step.
    Recorded reason: $reason

    The signing script writes SIGNED-<target> only when the binaries are both
    codesigned AND notarized. Anything less leaves no marker, and no marker
    means the artifact does not go into a release.

  WHY THIS IS BLOCKED RATHER THAN SHIPPED UNSIGNED
    macOS Gatekeeper attaches com.apple.quarantine to a downloaded unsigned
    binary and kills it. The daemon never starts and the user sees an
    unexplained "daemon not running" with nothing to search for. Shipping an
    unsigned macOS package is worse than shipping none -- a user who cannot
    install is better served than one who installs something that cannot run.

  HOW TO FIX IT -- add these five repository secrets:
    MACOS_CERT_P12            Developer ID Application .p12 (raw or base64)
    MACOS_CERT_PASSWORD       password for that .p12
    MACOS_NOTARY_KEY_ID       App Store Connect API key id
    MACOS_NOTARY_ISSUER_ID    App Store Connect issuer id (UUID)
    MACOS_NOTARY_KEY_BASE64   base64 of the AuthKey_*.p8 private key

    Nothing else has to change. The moment those exist the marker appears and
    $target ships with ZERO EDITS to this workflow or this guard.

  IF YOU MEANT TO SHIP WITHOUT macOS
    That is a supported outcome and it is what a workflow_dispatch does: on a
    dispatch this guard EXCLUDES the unsigned target and stays green. A tag is
    treated as a real release, which is why it fails here instead.

  BACKGROUND
    docs/RESIDUAL_RISKS.md, row R1.15.
  ============================================================================

EOF
}

# ------------------------------------------------------------------------------
run_guard() {
  local ref="$1" artifacts="$2" vsix_dir="$3" bin_dir="$4"
  local is_tag="no"
  case "$ref" in refs/tags/v*) is_tag="yes" ;; esac
  say "ref=$ref (tag release: $is_tag)"

  # Every target classified, exactly once.
  local t
  for t in $ALL_TARGETS; do
    if in_list "$t" "$NEEDS_SIGNING" && in_list "$t" "$NO_SIGNING_NEEDED"; then
      err "$t is in BOTH signing lists; classify it once."
      fail_count=$((fail_count + 1))
    elif ! in_list "$t" "$NEEDS_SIGNING" && ! in_list "$t" "$NO_SIGNING_NEEDED"; then
      err "$t is in neither signing list. Adding a target means deciding whether"
      err "  it must be signed, even if the decision is 'no'. Refusing to guess."
      fail_count=$((fail_count + 1))
    fi
  done
  [ "$fail_count" -gt 0 ] && return 1

  for t in $NEEDS_SIGNING; do
    if is_signed "$artifacts" "$t"; then
      say "$t: SIGNED marker present -- releasable."
      continue
    fi

    local reason
    reason="$(unsigned_reason "$artifacts" "$t")"

    if [ "$is_tag" = "yes" ]; then
      err "$t is unsigned and this is a tag."
      explain_tag_failure "$t" "$reason"
      fail_count=$((fail_count + 1))
      continue
    fi

    warn "EXCLUDING $t from the release: unsigned ($reason)."
    warn "  This is a dispatch, so the job stays green and the artifacts remain"
    warn "  uploaded for inspection. A TAG would fail here instead."
    excluded="$excluded $t"

    # Assets. Both directories, because both feed the release `files:` list.
    rm -fv "$vsix_dir"/*"$t"*.vsix 2>/dev/null || true
    rm -fv "$bin_dir"/codeterminal-tui-"$t" "$bin_dir"/codeterminal-tui-"$t".exe 2>/dev/null || true
  done

  [ "$fail_count" -gt 0 ] && return 1

  # MANIFESTS ARE REGENERATED, NOT EDITED. A manifest that still names a file the
  # release does not carry is its own defect -- it reads as a missing download
  # rather than as a deliberate omission -- and hand-pruning lines is how a
  # checksum and its file drift apart. Rebuilding from what is actually on disk
  # cannot disagree with what is actually on disk.
  if [ -n "$excluded" ]; then
    if [ -d "$vsix_dir" ] && ls "$vsix_dir"/*.vsix >/dev/null 2>&1; then
      ( cd "$vsix_dir" && sha256sum ./*.vsix > SHA256SUMS ) && say "regenerated $vsix_dir/SHA256SUMS"
    else
      rm -f "$vsix_dir/SHA256SUMS"
      warn "no .vsix packages remain; removed SHA256SUMS rather than leaving an empty one"
    fi
    if [ -d "$bin_dir" ] && ls "$bin_dir"/codeterminal-tui-* >/dev/null 2>&1; then
      ( cd "$bin_dir" && sha256sum codeterminal-tui-* > SHA256SUMS-tui ) && say "regenerated $bin_dir/SHA256SUMS-tui"
    else
      rm -f "$bin_dir/SHA256SUMS-tui"
      warn "no terminal-client binaries remain; removed SHA256SUMS-tui"
    fi
    say "excluded from release:$excluded"
  else
    say "nothing excluded; every target requiring a signature has one."
  fi

  # THE RUN LOG IS THE EVIDENCE. On a dispatch no release is created, so there is
  # no artifact to inspect afterwards to prove the exclusion happened -- the log
  # is the only record. Print exactly what the release `files:` globs would now
  # match, so "no darwin asset reached the release" is something a reader can
  # see rather than infer.
  say "--- release input, after the guard ---"
  local f found=0
  for f in "$vsix_dir"/*.vsix "$vsix_dir"/SHA256SUMS "$bin_dir"/codeterminal-tui-* "$bin_dir"/SHA256SUMS-tui; do
    [ -e "$f" ] || continue
    say "  WOULD ATTACH  $(basename "$f")"
    found=$((found + 1))
  done
  [ "$found" -eq 0 ] && say "  (nothing)"
  say "--- end release input ---"
  return 0
}

# ------------------------------------------------------------------------------
# SELF-TEST. Runs against a simulated staging tree on any platform -- it never
# invokes codesign and never needs a macOS runner.
# ------------------------------------------------------------------------------
self_test() {
  local tmp pass=0 fail=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  mk_tree() { # mk_tree <dir> ; builds a full three-target staging tree
    local d="$1"
    rm -rf "$d"; mkdir -p "$d/artifacts" "$d/out-vsix" "$d/out-bin"
    local t
    for t in $ALL_TARGETS; do
      mkdir -p "$d/artifacts/binaries-$t"
      echo "vsix-$t" > "$d/out-vsix/codeterminal-vscode-$t-0.0.1.vsix"
      if [ "$t" = "win32-x64" ]; then
        echo "tui-$t" > "$d/out-bin/codeterminal-tui-$t.exe"
      else
        echo "tui-$t" > "$d/out-bin/codeterminal-tui-$t"
      fi
    done
    ( cd "$d/out-vsix" && sha256sum ./*.vsix > SHA256SUMS )
    ( cd "$d/out-bin" && sha256sum codeterminal-tui-* > SHA256SUMS-tui )
  }
  sign() { printf 'target=%s\nsigned=yes\nnotarized=yes\n' "$2" > "$1/artifacts/binaries-$2/SIGNED-$2"; }
  unsign() { printf 'target=%s\nsigned=no\nreason=%s\n' "$2" "$3" > "$1/artifacts/binaries-$2/UNSIGNED-$2"; }
  check() { # check <desc> <expected:pass|fail> <actual-rc>
    if { [ "$2" = "pass" ] && [ "$3" -eq 0 ]; } || { [ "$2" = "fail" ] && [ "$3" -ne 0 ]; }; then
      printf '  ok    %s\n' "$1"; pass=$((pass + 1))
    else
      printf '  FAIL  %s (expected %s, rc=%s)\n' "$1" "$2" "$3"; fail=$((fail + 1))
    fi
  }
  absent() { if [ ! -e "$2" ]; then printf '  ok    %s\n' "$1"; pass=$((pass+1)); else printf '  FAIL  %s (still present: %s)\n' "$1" "$2"; fail=$((fail+1)); fi; }
  present() { if [ -e "$2" ]; then printf '  ok    %s\n' "$1"; pass=$((pass+1)); else printf '  FAIL  %s (missing: %s)\n' "$1" "$2"; fail=$((fail+1)); fi; }
  greps() { if grep -q "$2" "$3" 2>/dev/null; then printf '  ok    %s\n' "$1"; pass=$((pass+1)); else printf '  FAIL  %s (no /%s/ in %s)\n' "$1" "$2" "$3"; fail=$((fail+1)); fi; }
  ngreps() { if grep -q "$2" "$3" 2>/dev/null; then printf '  FAIL  %s (/%s/ still in %s)\n' "$1" "$2" "$3"; fail=$((fail+1)); else printf '  ok    %s\n' "$1"; pass=$((pass+1)); fi; }

  echo "release-signing-guard self-test"

  # --- 1. dispatch, darwin unsigned: excluded, green, manifests rebuilt.
  local d="$tmp/a"; mk_tree "$d"; unsign "$d" darwin-arm64 no-certificate
  fail_count=0; excluded=""
  run_guard "refs/heads/some-branch" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "dispatch + unsigned darwin -> green" pass $?
  absent  "  darwin vsix removed"            "$d/out-vsix/codeterminal-vscode-darwin-arm64-0.0.1.vsix"
  absent  "  darwin tui binary removed"      "$d/out-bin/codeterminal-tui-darwin-arm64"
  ngreps  "  SHA256SUMS drops darwin"        "darwin-arm64" "$d/out-vsix/SHA256SUMS"
  ngreps  "  SHA256SUMS-tui drops darwin"    "darwin-arm64" "$d/out-bin/SHA256SUMS-tui"
  present "  linux vsix untouched"           "$d/out-vsix/codeterminal-vscode-linux-x64-0.0.1.vsix"
  present "  windows tui untouched"          "$d/out-bin/codeterminal-tui-win32-x64.exe"
  greps   "  SHA256SUMS still names linux"   "linux-x64"   "$d/out-vsix/SHA256SUMS"
  greps   "  SHA256SUMS-tui names windows"   "win32-x64"   "$d/out-bin/SHA256SUMS-tui"

  # --- 2. tag, darwin unsigned: fails.
  d="$tmp/b"; mk_tree "$d"; unsign "$d" darwin-arm64 no-certificate
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "tag + unsigned darwin -> FAILS" fail $?
  present "  tag failure leaves assets alone" "$d/out-vsix/codeterminal-vscode-darwin-arm64-0.0.1.vsix"

  # --- 3. tag, darwin signed: passes, nothing removed.
  d="$tmp/c"; mk_tree "$d"; sign "$d" darwin-arm64
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "tag + signed darwin -> green" pass $?
  present "  darwin vsix kept"   "$d/out-vsix/codeterminal-vscode-darwin-arm64-0.0.1.vsix"
  greps   "  manifest keeps darwin" "darwin-arm64" "$d/out-vsix/SHA256SUMS"

  # --- 4. NO MARKER AT ALL is read as unsigned (reject-by-default).
  d="$tmp/d"; mk_tree "$d"   # neither SIGNED nor UNSIGNED written
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "tag + NO marker -> FAILS (reject by default)" fail $?

  # --- 5. an UNSIGNED marker never grants release; only SIGNED does.
  d="$tmp/e"; mk_tree "$d"; unsign "$d" darwin-arm64 not-notarized
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "tag + signed-but-not-notarized -> FAILS" fail $?

  # --- 6. PER-TARGET ISOLATION: signing one target does not release another.
  d="$tmp/f"; mk_tree "$d"; sign "$d" linux-x64   # wrong target signed
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "tag + linux marker does not release darwin" fail $?

  # --- 7. every target must be classified.
  d="$tmp/g"; mk_tree "$d"; sign "$d" darwin-arm64
  local save_all="$ALL_TARGETS"; ALL_TARGETS="$ALL_TARGETS freebsd-riscv"
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >/dev/null 2>&1
  check "an unclassified target -> FAILS rather than defaulting" fail $?
  ALL_TARGETS="$save_all"

  # --- 8. the tag failure message carries what a reader needs.
  d="$tmp/h"; mk_tree "$d"; unsign "$d" darwin-arm64 no-certificate
  fail_count=0; excluded=""
  run_guard "refs/tags/v1.0.0" "$d/artifacts" "$d/out-vsix" "$d/out-bin" >"$tmp/msg" 2>&1 || true
  greps "message names the target"        "darwin-arm64"          "$tmp/msg"
  greps "message says NOT a broken pipe"  "NOT a broken pipeline" "$tmp/msg"
  greps "message names the reason"        "no-certificate"        "$tmp/msg"
  greps "message names the secrets"       "MACOS_NOTARY_KEY_BASE64" "$tmp/msg"
  greps "message says zero edits later"   "ZERO EDITS"            "$tmp/msg"
  greps "message points at the register"  "R1.15"                 "$tmp/msg"

  # ---------------------------------------------------------------------------
  # THE OTHER HALF: does the signing script actually WRITE the marker the guard
  # reads? Tested here rather than assumed, because the guard being correct about
  # a marker nobody produces is worth nothing.
  #
  # THREE OF THE FOUR UNSIGNED PATHS ARE REACHABLE ON ANY HOST. Path 4
  # (signed-but-not-notarized) and the signed path need a macOS runner and real
  # Apple credentials, so they are NOT covered here and that is stated rather
  # than papered over -- see the note printed at the end.
  # ---------------------------------------------------------------------------
  local signer="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/macos-sign-and-notarize.sh"
  echo "marker writing (scripts/macos-sign-and-notarize.sh)"

  run_signer() { # run_signer <dist> <target> [env assignments...]
    local dist="$1" target="$2"; shift 2
    env -u MACOS_CERT_P12 -u MACOS_CERT_P12_BASE64 -u MACOS_CERT_PASSWORD \
        -u MACOS_NOTARY_KEY_ID -u MACOS_NOTARY_ISSUER_ID -u MACOS_NOTARY_KEY_BASE64 \
        DIST_DIR="$dist" SIGN_TARGET="$target" "$@" bash "$signer" >/dev/null 2>&1
  }

  # PATH 1 -- no binaries in DIST_DIR.
  local m="$tmp/m1"; mkdir -p "$m"
  run_signer "$m" darwin-arm64
  check   "path 1 (no binaries) exits 0" pass $?
  present "  wrote UNSIGNED-darwin-arm64" "$m/UNSIGNED-darwin-arm64"
  absent  "  wrote NO SIGNED marker"      "$m/SIGNED-darwin-arm64"
  greps   "  reason=no-binaries"          "reason=no-binaries" "$m/UNSIGNED-darwin-arm64"

  # PATH 2 -- binaries present, no certificate.
  m="$tmp/m2"; mkdir -p "$m"; : > "$m/codeterminal-daemon"; : > "$m/codeterminal-tui"
  run_signer "$m" darwin-arm64
  check   "path 2 (no certificate) exits 0" pass $?
  present "  wrote UNSIGNED-darwin-arm64"   "$m/UNSIGNED-darwin-arm64"
  absent  "  wrote NO SIGNED marker"        "$m/SIGNED-darwin-arm64"
  greps   "  reason=no-certificate"         "reason=no-certificate" "$m/UNSIGNED-darwin-arm64"

  # PATH 3 -- certificate present but the host is not macOS. On a Linux runner
  # this is the branch a cert actually reaches, which is why it is testable here.
  m="$tmp/m3"; mkdir -p "$m"; : > "$m/codeterminal-daemon"
  if [ "$(uname -s)" != "Darwin" ]; then
    run_signer "$m" darwin-arm64 MACOS_CERT_P12=ZmFrZQ==
    check   "path 3 (not macOS) exits 0"  pass $?
    present "  wrote UNSIGNED-darwin-arm64" "$m/UNSIGNED-darwin-arm64"
    absent  "  wrote NO SIGNED marker"      "$m/SIGNED-darwin-arm64"
    greps   "  reason=not-macos"            "reason=not-macos" "$m/UNSIGNED-darwin-arm64"
  else
    echo "  skip  path 3 (not macOS) -- this IS macOS; branch unreachable here"
  fi

  # SIGN_TARGET is required, not guessed: the marker's name decides what ships.
  m="$tmp/m4"; mkdir -p "$m"; : > "$m/codeterminal-daemon"
  ( env -u MACOS_CERT_P12 DIST_DIR="$m" SIGN_TARGET="" bash "$signer" >/dev/null 2>&1 )
  check "missing SIGN_TARGET fails loudly rather than defaulting" fail $?

  # A stale SIGNED marker must not survive a later unsigned run.
  m="$tmp/m5"; mkdir -p "$m"; : > "$m/codeterminal-daemon"
  printf 'target=darwin-arm64\nsigned=yes\n' > "$m/SIGNED-darwin-arm64"
  run_signer "$m" darwin-arm64
  absent "a stale SIGNED marker is cleared by an unsigned run" "$m/SIGNED-darwin-arm64"

  # Per-target isolation at the WRITING end as well as the reading end.
  m="$tmp/m6"; mkdir -p "$m"; : > "$m/codeterminal-daemon"
  run_signer "$m" win32-x64
  present "signing win32-x64 writes only its own marker" "$m/UNSIGNED-win32-x64"
  absent  "  and not darwin's"                           "$m/UNSIGNED-darwin-arm64"

  echo
  echo "release-signing-guard: $pass passed, $fail failed"
  echo "NOT COVERED HERE, and not claimed: unsigned path 4 (signed but not"
  echo "  notarized) and the signed path itself. Both need a macOS runner and"
  echo "  real Apple credentials. The guard's own handling of a signed marker IS"
  echo "  covered above, using a marker written by the test."
  [ "$fail" -eq 0 ]
}

# ------------------------------------------------------------------------------
REF=""; ARTIFACTS=""; VSIX_DIR=""; BIN_DIR=""
case "${1:-}" in
  --self-test) self_test; exit $? ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --ref)       REF="$2"; shift 2 ;;
    --artifacts) ARTIFACTS="$2"; shift 2 ;;
    --vsix-dir)  VSIX_DIR="$2"; shift 2 ;;
    --bin-dir)   BIN_DIR="$2"; shift 2 ;;
    *) err "unknown argument: $1"; exit 2 ;;
  esac
done
if [ -z "$REF" ] || [ -z "$ARTIFACTS" ] || [ -z "$VSIX_DIR" ] || [ -z "$BIN_DIR" ]; then
  err "usage: $0 --ref <git-ref> --artifacts <dir> --vsix-dir <dir> --bin-dir <dir>"
  err "   or: $0 --self-test"
  exit 2
fi
run_guard "$REF" "$ARTIFACTS" "$VSIX_DIR" "$BIN_DIR"
exit $?
