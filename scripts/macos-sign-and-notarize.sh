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
#   STRICT_MODE               Set to "true" to fail if signing credentials are missing
# ==============================================================================

set -euo pipefail

DIST_DIR="${DIST_DIR:-./dist}"
CODESIGN_IDENTITY="${CODESIGN_IDENTITY:-Developer ID Application}"
STRICT_MODE="${STRICT_MODE:-false}"

log() {
  echo "[macos-sign] $*"
}

warn() {
  echo "[macos-sign] WARNING: $*" >&2
}

err() {
  echo "[macos-sign] ERROR: $*" >&2
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
  exit 0
fi

# Verify we are on macOS
if [ "$(uname -s)" != "Darwin" ]; then
  warn "Host operating system is not macOS ($(uname -s)). Cannot execute Apple codesign / notarytool."
  if [ "$STRICT_MODE" = "true" ]; then
    err "STRICT_MODE=true: Signing requires a macOS runner."
    exit 1
  fi
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
else
  warn "Notarization credentials (MACOS_NOTARY_KEY_ID / MACOS_NOTARY_ISSUER_ID / MACOS_NOTARY_KEY_BASE64) not fully provided."
  warn "Binaries are signed but NOT submitted to Apple Notary Service."
fi

log "macOS binary signing process completed successfully."
