#!/usr/bin/env bash
# ==============================================================================
# macos-sign-and-notarize.sh: Stage 3.5 macOS Binary Signing & Notarization
# ==============================================================================
#
# Encapsulates code signing and Apple Notary Service submission for darwin-arm64
# binaries (codeterminal-daemon and codeterminal-embedder-helper).
#
# ENVIRONMENT VARIABLES:
#   MACOS_CERT_P12_BASE64     Base64-encoded Developer ID Application .p12 certificate
#   MACOS_CERT_P12            Raw file path or base64 string of .p12 certificate
#   MACOS_CERT_PASSWORD       Password for the .p12 certificate
#   MACOS_NOTARY_KEY_ID       Apple Developer API Key ID (10 chars, e.g. ABC123DEFG)
#   MACOS_NOTARY_ISSUER_ID    Apple Developer API Issuer ID (UUID string)
#   MACOS_NOTARY_KEY_BASE64   Base64-encoded AuthKey_*.p8 private key for Notary API
#   CODESIGN_IDENTITY         Signing identity string (default: "Developer ID Application")
#   DIST_DIR                  Directory containing binaries (default: ./dist)
#   SIGN_TARGET               REQUIRED. The release target being signed, e.g.
#                             darwin-arm64. Names the marker files below.
#   STRICT_MODE               Set to "true" to fail if signing credentials are missing
#
# THE MARKER, AND WHY ITS ABSENCE IS THE MEANINGFUL STATE
# ------------------------------------------------------
# This script used to exit 0 in every unsigned case and say nothing a machine
# could read. The release workflow therefore could not tell "signed" from
# "quietly did nothing", and on 2026-09-05 the first-ever dispatch of
# release.yml proved it: green run, UNSIGNED darwin binaries, and a warning
# nothing acted on (R1.15).
#
# It now writes ONE of two files into DIST_DIR:
#
#   SIGNED-<target>     written ONLY when the binaries are signed AND notarized.
#   UNSIGNED-<target>   written at every path that ends without both, carrying a
#                       machine-readable reason= line for the guard's message.
#
# THE GUARD KEYS ON THE PRESENCE OF SIGNED-<target>, NOT ON THE ABSENCE OF
# UNSIGNED-<target>. That is reject-by-default, the same rule as the terminal
# sanitizer: if the code does not positively assert the good state, the good
# state is not assumed. A future exit path that forgets to write anything is
# therefore treated as UNSIGNED, which is the safe direction. UNSIGNED-<target>
# exists for diagnostics only -- it makes the failure message specific -- and
# the guard never needs it to reach the right answer.
#
# PER-TARGET, NOT GLOBAL, so the release step can act on each artifact
# independently. Windows Authenticode later writes SIGNED-win32-x64 from its own
# script and needs no second mechanism on the release side.
#
# FOUR PATHS END UNSIGNED. Enumerated here so a fifth added later is visibly not
# covered by this list:
#
#   1. no target binaries found in DIST_DIR       reason=no-binaries
#   2. no signing certificate supplied            reason=no-certificate
#   3. host is not macOS                          reason=not-macos
#   4. signed, but notarization creds incomplete  reason=not-notarized
#
# (4) IS NOT A "SIGNED" STATE and was made explicit on 2026-09-05. Gatekeeper
# blocks a signed but un-notarized binary downloaded from the internet exactly as
# it blocks an unsigned one, so calling it signed would reproduce the failure
# this script exists to prevent. The STRICT_MODE exit 1 paths are not in this
# list because they fail the job rather than ending unsigned-but-green.
# ==============================================================================

set -euo pipefail

DIST_DIR="${DIST_DIR:-./dist}"
CODESIGN_IDENTITY="${CODESIGN_IDENTITY:-Developer ID Application}"
STRICT_MODE="${STRICT_MODE:-false}"

# REQUIRED and deliberately not defaulted. A default would name the marker after
# a guess, and the guard reads that name to decide whether an artifact may be
# released -- so guessing here is the one place a wrong answer is worst. Failing
# loudly on a missing value is the fail-closed choice.
SIGN_TARGET="${SIGN_TARGET:-}"
if [ -z "$SIGN_TARGET" ]; then
  echo "[macos-sign] ERROR: SIGN_TARGET is not set." >&2
  echo "[macos-sign]   It names the SIGNED-<target> / UNSIGNED-<target> marker that" >&2
  echo "[macos-sign]   scripts/release-signing-guard.sh reads to decide what may ship." >&2
  echo "[macos-sign]   Set it to the release target, e.g. SIGN_TARGET=darwin-arm64." >&2
  exit 1
fi

log() {
  echo "[macos-sign] $*"
}

warn() {
  echo "[macos-sign] WARNING: $*" >&2
}

err() {
  echo "[macos-sign] ERROR: $*" >&2
}

# mark_unsigned <reason> -- records that this target must NOT be released, and
# why. Removes any stale SIGNED marker so a re-run cannot leave both.
mark_unsigned() {
  mkdir -p "$DIST_DIR"
  rm -f "$DIST_DIR/SIGNED-$SIGN_TARGET"
  {
    echo "target=$SIGN_TARGET"
    echo "signed=no"
    echo "reason=$1"
    echo "recorded=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$DIST_DIR/UNSIGNED-$SIGN_TARGET"
  warn "marker: UNSIGNED-$SIGN_TARGET (reason=$1)"
}

# mark_signed -- the ONLY writer of the marker the guard trusts. Reached only
# after both codesign AND notarization have succeeded.
mark_signed() {
  mkdir -p "$DIST_DIR"
  rm -f "$DIST_DIR/UNSIGNED-$SIGN_TARGET"
  {
    echo "target=$SIGN_TARGET"
    echo "signed=yes"
    echo "notarized=yes"
    echo "recorded=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$DIST_DIR/SIGNED-$SIGN_TARGET"
  log "marker: SIGNED-$SIGN_TARGET"
}

# Ensure target directory exists
if [ ! -d "$DIST_DIR" ]; then
  warn "Dist directory '$DIST_DIR' does not exist. Creating it."
  mkdir -p "$DIST_DIR"
fi

# Check for binaries to sign.
#
# codeterminal-tui joined this list on 2026-09-04 (R1.14). It is a standalone
# binary a user downloads and runs from a terminal, which is exactly the path
# that attaches com.apple.quarantine -- an unsigned one is killed by Gatekeeper
# with no explanation, the same failure the daemon has. Shipping it unsigned
# would be shipping the defect this script exists to prevent, one binary over.
TARGET_BINARIES=()
for bin in "$DIST_DIR/codeterminal-daemon" "$DIST_DIR/codeterminal-embedder-helper" "$DIST_DIR/codeterminal-tui"; do
  if [ -f "$bin" ]; then
    TARGET_BINARIES+=("$bin")
  fi
done

if [ ${#TARGET_BINARIES[@]} -eq 0 ]; then
  warn "No target macOS binaries found in '$DIST_DIR'."
  mark_unsigned "no-binaries"   # UNSIGNED PATH 1 of 4
  exit 0
fi

# Check if signing credentials exist
CERT_DATA="${MACOS_CERT_P12_BASE64:-${MACOS_CERT_P12:-}}"

if [ -z "$CERT_DATA" ]; then
  warn "No MACOS_CERT_P12 or MACOS_CERT_P12_BASE64 secret supplied."
  warn "darwin-arm64 binaries will remain UNSIGNED."
  warn "Gatekeeper on macOS will quarantine unsigned binaries unless signed."
  if [ "$STRICT_MODE" = "true" ]; then
    err "STRICT_MODE=true: Failing because signing credentials are missing."
    exit 1
  fi
  log "Signing step completed in DRY-RUN mode (unsigned)."
  mark_unsigned "no-certificate"   # UNSIGNED PATH 2 of 4
  exit 0
fi

# Verify we are on macOS
if [ "$(uname -s)" != "Darwin" ]; then
  warn "Host operating system is not macOS ($(uname -s)). Cannot execute Apple codesign / notarytool."
  if [ "$STRICT_MODE" = "true" ]; then
    err "STRICT_MODE=true: Signing requires a macOS runner."
    exit 1
  fi
  mark_unsigned "not-macos"   # UNSIGNED PATH 3 of 4
  exit 0
fi

log "Initializing temporary keychain for signing..."
KEYCHAIN_PASS=$(openssl rand -hex 16)
KEYCHAIN_PATH="$HOME/Library/Keychains/build-$$.keychain-db"
CERT_FILE="$(mktemp /tmp/cert.XXXXXX.p12)"

cleanup() {
  log "Cleaning up temporary signing artifacts..."
  rm -f "$CERT_FILE"
  if [ -f "$KEYCHAIN_PATH" ]; then
    security delete-keychain "$KEYCHAIN_PATH" 2>/dev/null || true
  fi
  rm -f /tmp/notary_key.p8 /tmp/binaries_to_notarize.zip 2>/dev/null || true
}
trap cleanup EXIT

# Write P12 certificate to temp file
if [[ "$CERT_DATA" =~ ^[A-Za-z0-9+/=]+$ ]] && ! [ -f "$CERT_DATA" ]; then
  echo "$CERT_DATA" | base64 --decode > "$CERT_FILE"
else
  cp "$CERT_DATA" "$CERT_FILE"
fi

# Create & unlock temporary keychain
security create-keychain -p "$KEYCHAIN_PASS" "$KEYCHAIN_PATH"
security set-keychain-settings -lut 21600 "$KEYCHAIN_PATH"
security unlock-keychain -p "$KEYCHAIN_PASS" "$KEYCHAIN_PATH"
security import "$CERT_FILE" -k "$KEYCHAIN_PATH" -P "${MACOS_CERT_PASSWORD:-}" -T /usr/bin/codesign -T /usr/bin/security
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$KEYCHAIN_PASS" "$KEYCHAIN_PATH" > /dev/null
security list-keychains -s "$KEYCHAIN_PATH" $(security list-keychains | tr -d '"')

log "Signing binaries with identity '$CODESIGN_IDENTITY'..."
for bin in "${TARGET_BINARIES[@]}"; do
  log "  Codesigning $bin..."
  codesign --deep --force --options runtime --timestamp \
    --sign "$CODESIGN_IDENTITY" \
    --keychain "$KEYCHAIN_PATH" \
    "$bin"
  
  log "  Verifying signature for $bin..."
  codesign --verify --verbose=2 "$bin"
done

# Check if Notarization credentials exist
if [ -n "${MACOS_NOTARY_KEY_ID:-}" ] && [ -n "${MACOS_NOTARY_ISSUER_ID:-}" ] && [ -n "${MACOS_NOTARY_KEY_BASE64:-}" ]; then
  log "Submitting signed binaries to Apple Notary Service..."
  echo "$MACOS_NOTARY_KEY_BASE64" | base64 --decode > /tmp/notary_key.p8
  
  (cd "$DIST_DIR" && zip -r /tmp/binaries_to_notarize.zip . -i "codeterminal-*")

  log "  Submitting zip archive to notarytool..."
  xcrun notarytool submit /tmp/binaries_to_notarize.zip \
    --key-id "$MACOS_NOTARY_KEY_ID" \
    --issuer "$MACOS_NOTARY_ISSUER_ID" \
    --key /tmp/notary_key.p8 \
    --wait

  log "  Stapling notarization tickets..."
  for bin in "${TARGET_BINARIES[@]}"; do
    xcrun stapler staple "$bin" || warn "Stapling $bin returned non-zero (may be executable binary rather than app bundle, ticket attached online)."
  done
  log "Notarization complete."
  mark_signed
else
  warn "Notarization credentials (MACOS_NOTARY_KEY_ID / MACOS_NOTARY_ISSUER_ID / MACOS_NOTARY_KEY_BASE64) not fully provided."
  warn "Binaries are signed but NOT submitted to Apple Notary Service."
  warn "That is NOT a releasable state: Gatekeeper blocks a signed but un-notarized"
  warn "binary downloaded from the internet exactly as it blocks an unsigned one."
  mark_unsigned "not-notarized"   # UNSIGNED PATH 4 of 4
fi

log "macOS binary signing process completed successfully."
