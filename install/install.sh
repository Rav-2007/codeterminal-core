#!/bin/sh
# Mochiii installer for macOS and Linux.
#
#   curl -fsSL https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.sh | sh
#
# Options go after `sh -s --`, or in the environment variable beside each:
#
#   --version <v>      install release <v> instead of the latest    MOCHIII_VERSION
#   --no-model         skip the code-search model download         MOCHIII_NO_MODEL=1
#   --no-modify-path   do not add ~/.local/bin to your shell PATH  MOCHIII_NO_MODIFY_PATH=1
#   --uninstall        remove Mochiii, keeping ~/.mochiii          MOCHIII_UNINSTALL=1
#   --purge            with --uninstall, remove ~/.mochiii too     MOCHIII_PURGE=1
#   --allow-root       install even when running as root           MOCHIII_ALLOW_ROOT=1
#
# A variable counts only when it is exactly 1, as in install.ps1: MOCHIII_PURGE=0
# purges nothing.
#
# What it does, in order:
#   1. works out which build this machine needs (Linux x64 or ARM64, glibc or musl;
#      Apple Silicon Mac);
#   2. downloads it and its checksum from the GitHub release, and refuses to go on
#      unless the checksum matches -- there is no switch to skip that;
#   3. unpacks it beside the current install, checks it runs, then swaps it in, so a
#      failure part-way leaves the previous version working;
#   4. links `mochiii` into ~/.local/bin and, if that is not on your PATH, adds one
#      marked line to your shell's startup file;
#   5. downloads the code-search model (about 41 MB on Linux, 63 MB on macOS, once).
#
# It installs the terminal app only. Until 1.2.1 it also installed the VS Code
# extension when it found VS Code; that comes from the Marketplace now, and
# 1.1's --no-vscode is still accepted and does nothing.
#
# It never uses sudo. Everything goes under your home directory: the programs in
# ~/.mochiii/bin, your keys, models and chats in ~/.mochiii as before.
#
# Everything runs inside main(), called on the last line, so a download that is cut
# off part-way runs nothing at all.

set -eu

REPO="Rav-2007/codeterminal-core"
MARKER="# Added by the Mochiii installer"

say() { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
warn() { printf '%swarning:%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() {
	printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2
	exit 1
}

setup_colors() {
	BOLD="" RED="" YELLOW="" RESET=""
	if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
		BOLD=$(printf '\033[1m') RED=$(printf '\033[31m') YELLOW=$(printf '\033[33m') RESET=$(printf '\033[0m')
	fi
}

has() { command -v "$1" >/dev/null 2>&1; }

# env_flag <value> prints 1 when an option's environment variable is exactly 1,
# and nothing otherwise, so a 0 or a stray word can never switch on --purge.
env_flag() {
	if [ "${1:-}" = 1 ]; then echo 1; fi
}

parse_args() {
	VERSION="${MOCHIII_VERSION:-}"
	NO_MODEL=$(env_flag "${MOCHIII_NO_MODEL:-}")
	NO_MODIFY_PATH=$(env_flag "${MOCHIII_NO_MODIFY_PATH:-}")
	UNINSTALL=$(env_flag "${MOCHIII_UNINSTALL:-}")
	PURGE=$(env_flag "${MOCHIII_PURGE:-}")
	ALLOW_ROOT=$(env_flag "${MOCHIII_ALLOW_ROOT:-}")
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			[ $# -ge 2 ] || die "--version needs a version, for example: --version 1.1.0"
			VERSION="$2"
			shift
			;;
		--version=*) VERSION="${1#--version=}" ;;
		--no-model) NO_MODEL=1 ;;
		--no-vscode) ;; # 1.1's option: the installer no longer touches VS Code
		--no-modify-path) NO_MODIFY_PATH=1 ;;
		--uninstall) UNINSTALL=1 ;;
		--purge) PURGE=1 ;;
		--allow-root) ALLOW_ROOT=1 ;;
		-h | --help)
			sed -n '2,/^$/p' "$0" 2>/dev/null || say "See https://github.com/$REPO#install"
			exit 0
			;;
		*) die "unknown option: $1 (see --help)" ;;
		esac
		shift
	done
	VERSION="${VERSION#v}"
}

setup_paths() {
	[ -n "${HOME:-}" ] || die "HOME is not set"
	MOCHIII_DIR="${MOCHIII_HOME:-$HOME/.mochiii}"
	BIN_DIR="$MOCHIII_DIR/bin"
	LINK_DIR="$HOME/.local/bin"
}

detect_target() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
	Darwin)
		# An arm64 Mac running this shell under Rosetta reports x86_64.
		if [ "$arch" = "x86_64" ] && [ "$(sysctl -in sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then
			arch=arm64
		fi
		case "$arch" in
		arm64) TARGET=darwin-arm64 ;;
		*) die "Intel Macs are not supported yet: the code-search engine Mochiii runs on (onnxruntime) has no Intel Mac build. Mochiii needs an Apple Silicon Mac (M1 or later)." ;;
		esac
		;;
	Linux)
		case "$arch" in
		x86_64 | amd64) TARGET=linux-x64 ;;
		aarch64 | arm64) TARGET=linux-arm64 ;;
		*) die "no Mochiii build for Linux on $arch yet (there are builds for x86_64 and arm64)" ;;
		esac
		check_libc
		;;
	MINGW* | MSYS* | CYGWIN*)
		die "on Windows, run this in PowerShell instead: irm https://github.com/$REPO/releases/latest/download/install.ps1 | iex"
		;;
	*) die "no Mochiii build for $os yet (there are builds for Linux, macOS and Windows)" ;;
	esac
}

# Mochiii's code search needs glibc 2.27 or newer, onnxruntime's own floor. Chat,
# agent tools and everything else work without it, so an older or musl system is warned, not refused.
# The regular Linux build needs glibc's loader to start at all, so a musl system
# (Alpine) gets the static -musl build instead, which has no code search and so
# no model to download.
check_libc() {
	if is_musl; then
		TARGET="$TARGET-musl"
		NO_MODEL=1
		warn "this system uses musl libc: installing the build without code search, which needs glibc 2.27+. Everything else works."
		return
	fi
	glibc=$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}')
	[ -n "$glibc" ] || return 0
	major=${glibc%%.*}
	minor=${glibc#*.}
	minor=${minor%%.*}
	if [ "$major" -lt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -lt 27 ]; }; then
		warn "this system has glibc $glibc; code search needs 2.27+ and will be off. Everything else works."
	fi
}

# is_musl: ldd says musl, or, with no ldd to ask, there is a musl loader and no
# glibc one (a glibc system can carry musl's loader too).
is_musl() {
	if ldd --version 2>&1 | grep -qi musl; then
		return 0
	fi
	for f in /lib/ld-musl-*; do
		[ -e "$f" ] || continue
		for g in /lib/ld-linux* /lib64/ld-linux*; do
			[ -e "$g" ] && return 1
		done
		return 0
	done
	return 1
}

check_user() {
	if [ "$(id -u)" = "0" ] && [ -z "$ALLOW_ROOT" ]; then
		die "run this as your own user, not as root or with sudo: Mochiii installs into your home directory. (--allow-root, or MOCHIII_ALLOW_ROOT=1, to do it anyway.)"
	fi
}

setup_download() {
	if [ -n "${MOCHIII_DOWNLOAD_BASE:-}" ]; then
		BASE="${MOCHIII_DOWNLOAD_BASE%/}"
		case "$BASE" in
		https://*) ;;
		http://127.0.0.1:* | http://localhost:*) ;; # a local test server, never a remote one
		*) die "MOCHIII_DOWNLOAD_BASE must be an https:// address" ;;
		esac
	elif [ -n "$VERSION" ]; then
		BASE="https://github.com/$REPO/releases/download/v$VERSION"
	else
		BASE="https://github.com/$REPO/releases/latest/download"
	fi
	if has curl; then
		FETCH=curl
	elif has wget; then
		FETCH=wget
	else
		die "curl or wget is needed to download Mochiii"
	fi
}

fetch() { # fetch <url> <file>
	case "$1" in
	https://*) proto="=https" ;;
	*) proto="=http,https" ;;
	esac
	if [ "$FETCH" = curl ]; then
		curl -fsSL --proto "$proto" --tlsv1.2 --retry 3 --retry-delay 2 -o "$2" "$1"
	else
		wget -q --tries=3 -O "$2" "$1"
	fi
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif has shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif has openssl; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		die "no SHA-256 tool (sha256sum, shasum or openssl) to verify the download with"
	fi
}

download_and_verify() {
	ARCHIVE="mochiii-$TARGET.tar.gz"
	step "Downloading Mochiii${VERSION:+ $VERSION} for $TARGET"
	fetch "$BASE/SHA256SUMS-archives" "$TMP/SHA256SUMS-archives" ||
		die "could not download the release's checksums from $BASE"
	fetch "$BASE/$ARCHIVE" "$TMP/$ARCHIVE" || die "could not download $ARCHIVE from $BASE"
	expected=$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*" f {print $1}' "$TMP/SHA256SUMS-archives")
	[ -n "$expected" ] || die "the release's checksums do not list $ARCHIVE, so it cannot be verified"
	actual=$(sha256_of "$TMP/$ARCHIVE")
	[ "$expected" = "$actual" ] ||
		die "checksum mismatch for $ARCHIVE (expected $expected, got $actual). Nothing was installed."
	say "    checksum verified"
}

install_files() {
	mkdir -p "$MOCHIII_DIR"
	staging="$MOCHIII_DIR/bin.new.$$"
	previous="$MOCHIII_DIR/bin.old.$$"
	rm -rf "$staging"
	mkdir -p "$staging"
	tar -xzf "$TMP/$ARCHIVE" -C "$staging" || die "could not unpack $ARCHIVE"
	[ -f "$staging/mochiii" ] || die "$ARCHIVE has no mochiii in it"
	if [ "$TARGET" = darwin-arm64 ] && has xattr; then
		xattr -dr com.apple.quarantine "$staging" 2>/dev/null || true
	fi
	INSTALLED=$("$staging/mochiii" --version 2>/dev/null | awk '{print $2}') ||
		die "the downloaded mochiii does not run on this machine"
	[ -n "$INSTALLED" ] || die "the downloaded mochiii does not run on this machine"
	if [ -n "$VERSION" ] && [ "$INSTALLED" != "$VERSION" ]; then
		die "asked for $VERSION but the download says it is $INSTALLED"
	fi

	if [ -d "$BIN_DIR" ]; then
		mv "$BIN_DIR" "$previous"
	fi
	if ! mv "$staging" "$BIN_DIR"; then
		[ -d "$previous" ] && mv "$previous" "$BIN_DIR"
		die "could not move the new version into $BIN_DIR"
	fi
	rm -rf "$previous"
	step "Installed Mochiii $INSTALLED in $BIN_DIR"
}

# regex_quote <text> prints text with every regular-expression special character
# escaped, so a path is matched as itself.
regex_quote() {
	# shellcheck disable=SC2016 # the $ is a character to escape, not an expansion
	printf '%s' "$1" | sed 's/[][\.*^$()+?{}|]/\\&/g'
}

# daemon_pids prints the process IDs of background services started from this
# install. It matches the full path, so a daemon VS Code started from its own
# copy is never touched -- and both spellings of it: a program runs under the
# path it was started by, and the terminal app starts the service by its
# symlink-resolved path, which differs from BIN_DIR for a home reached through a
# symlink (on macOS, any home under /tmp, which is /private/tmp).
#
# And both GENERATIONS of it: since 1.2.0 the service is the one binary run as
# `mochiii daemon`, and until then it was a program of its own, mochiii-daemon.
# Upgrading a 1.1.0 install must stop the 1.1.0 service, or the next `mochiii`
# would reach the old daemon and its old helper. A terminal app open elsewhere
# (`mochiii` with no role word) is never matched.
daemon_pids() {
	has pgrep || return 0
	physical=$(cd "$BIN_DIR" 2>/dev/null && pwd -P) || physical=$BIN_DIR
	for dir in "$BIN_DIR" "$physical"; do
		pgrep -f "^$(regex_quote "$dir")/mochiii(-daemon| daemon)( |\$)" 2>/dev/null || true
	done | sort -u
}

# stop_running stops background services started from this install and waits
# for them to finish (each stops its own embedder helper), so the next `mochiii`
# starts the version just installed rather than reaching one shutting down.
stop_running() {
	pids=$(daemon_pids)
	[ -n "$pids" ] || return 0
	# shellcheck disable=SC2086 # one pid per word
	kill $pids 2>/dev/null || true
	i=0
	while [ -n "$(daemon_pids)" ] && [ $i -lt 10 ]; do
		sleep 1
		i=$((i + 1))
	done
	say "    stopped the running background service; the next \`mochiii\` starts the new one"
}

link_and_path() {
	mkdir -p "$LINK_DIR"
	ln -sf "$BIN_DIR/mochiii" "$LINK_DIR/mochiii"
	case ":$PATH:" in
	*":$LINK_DIR:"*) return 0 ;;
	esac
	if [ -n "$NO_MODIFY_PATH" ]; then
		NEEDS_PATH="add $LINK_DIR to your PATH"
		return 0
	fi
	case "$(basename "${SHELL:-sh}")" in
	zsh) rc="$HOME/.zshrc" line="export PATH=\"$LINK_DIR:\$PATH\"" ;;
	bash)
		rc="$HOME/.bashrc"
		[ "$(uname -s)" = Darwin ] && rc="$HOME/.bash_profile"
		line="export PATH=\"$LINK_DIR:\$PATH\""
		;;
	fish) rc="$HOME/.config/fish/config.fish" line="fish_add_path $LINK_DIR" ;;
	*) rc="$HOME/.profile" line="export PATH=\"$LINK_DIR:\$PATH\"" ;;
	esac
	if ! grep -qF "$MARKER" "$rc" 2>/dev/null; then
		mkdir -p "$(dirname "$rc")"
		printf '\n%s\n%s\n' "$MARKER" "$line" >>"$rc"
		say "    added $LINK_DIR to your PATH in $rc"
	fi
	NEEDS_PATH="open a new terminal (or run: export PATH=\"$LINK_DIR:\$PATH\")"
}

fetch_model() {
	if [ -n "$NO_MODEL" ]; then
		return 0
	fi
	step "Downloading the code-search model (once for this computer)"
	if ! "$BIN_DIR/mochiii" download-model; then
		warn "the model download did not finish; Mochiii offers it again the first time you run it in a project"
	fi
}

uninstall() {
	step "Removing Mochiii"
	stop_running
	if [ -L "$LINK_DIR/mochiii" ]; then
		rm -f "$LINK_DIR/mochiii"
	fi
	rm -rf "$BIN_DIR"
	for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.profile" "$HOME/.config/fish/config.fish"; do
		if [ ! -f "$rc" ] || ! grep -qF "$MARKER" "$rc"; then
			continue
		fi
		awk -v m="$MARKER" '$0 == m {skip = 1; next} skip {skip = 0; next} {print}' "$rc" >"$rc.mochiii-tmp" &&
			mv "$rc.mochiii-tmp" "$rc"
	done
	if [ -n "$PURGE" ]; then
		rm -rf "$MOCHIII_DIR"
		say "Removed Mochiii and everything in $MOCHIII_DIR."
	else
		say "Removed Mochiii. Your keys, models and saved chats are still in $MOCHIII_DIR (--purge removes them)."
	fi
}

main() {
	setup_colors
	parse_args "$@"
	setup_paths
	check_user
	if [ -n "$UNINSTALL" ]; then
		uninstall
		return 0
	fi
	detect_target
	setup_download
	TMP=$(mktemp -d 2>/dev/null || mktemp -d -t mochiii)
	trap 'rm -rf "$TMP"' EXIT INT TERM
	NEEDS_PATH=""
	download_and_verify
	install_files
	stop_running
	link_and_path
	fetch_model
	say ""
	say "${BOLD}Mochiii $INSTALLED is ready.${RESET}"
	if [ -n "$NEEDS_PATH" ]; then
		say "First $NEEDS_PATH, then run it inside a project folder:"
	else
		say "Run it inside a project folder:"
	fi
	say ""
	say "    cd your-project && mochiii"
	say ""
	say "The first run asks which AI provider to use and for its API key."
}

main "$@"
