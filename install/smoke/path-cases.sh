#!/bin/sh
# path-cases.sh <installer> <download-base> <version>
#
# What the installer does and says about PATH in the cases users have hit, each in
# a fresh HOME with ~/.local/bin NOT on PATH, as on a Mac:
#
#   A. a zsh user: one line goes into ~/.zshrc, and the installer ends on the one
#      line THIS terminal needs, which works there (a Mac user typed `mochiii` in
#      the window they installed from and got "command not found", 2026-10-26);
#   B. installing again adds nothing and still says that line;
#   C. a ~/.zshrc it cannot change (one owned by root, on another Mac, 2026-10-25)
#      does not stop it part-way: it finishes, warns, and prints the fix;
#   D. a ~/.local it cannot write to: the PATH names ~/.mochiii/bin instead;
#   E. ~/.local/bin already on PATH: nothing to say, and nothing written;
#   F. --no-modify-path: the line and where to put it, and nothing written;
#   G. a fish user: fish's own line.
#
# run-smoke.sh runs it against the release it serves, under the system's sh.
# Exit status 0 only if every case passed.
set -u

[ $# -eq 3 ] || {
	echo "usage: path-cases.sh <installer> <download-base> <version>" >&2
	exit 2
}
installer=$1 base=$2 want=$3
if [ "$(id -u)" = 0 ]; then
	echo "path-cases: skipped as root, who can write to any file (and the installer refuses root)"
	exit 0
fi
work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
failed=0
ok() { echo "  ok   $*"; }
bad() {
	echo "  FAIL $*"
	failed=1
}

# install <case> <login shell> [installer options]: as a user of that shell would,
# from a terminal whose PATH has no ~/.local/bin. Output: $work/<case>.out.
install() {
	c=$1 user_shell=$2
	shift 2
	mkdir -p "$work/$c"
	env -i HOME="$work/$c" PATH=/usr/bin:/bin SHELL="$user_shell" TERM=dumb NO_COLOR=1 \
		MOCHIII_DOWNLOAD_BASE="$base" sh "$installer" --no-model "$@" >"$work/$c.out" 2>&1
}

# says <case> <text>: the output has <text> somewhere; line <case> <text>: as a
# whole line of its own.
says() { grep -qF "$2" "$work/$1.out"; }
line() { grep -qxF "$2" "$work/$1.out"; }

# works_here <case>: the export line it printed, run in a shell, finds mochiii.
works_here() {
	printed=$(grep -x '    export PATH=.*' "$work/$1.out" | sed 's/^    //')
	[ -n "$printed" ] &&
		[ "$(env -i HOME="$work/$1" PATH=/usr/bin:/bin sh -c "$printed && mochiii --version" 2>&1)" = "mochiii $want" ]
}

echo "A. zsh, ~/.local/bin not on PATH"
h=$work/A
if install A /bin/zsh; then ok "installed"; else bad "the installer failed: $(tail -3 "$work/A.out")"; fi
if [ "$(grep -c '.local/bin' "$h/.zshrc" 2>/dev/null)" = 1 ]; then ok "one line in ~/.zshrc"; else bad "the line is not in ~/.zshrc once"; fi
if says A "This terminal cannot find mochiii yet" && line A "    export PATH=\"$h/.local/bin:\$PATH\""; then
	ok "ends on the line this terminal needs, on its own"
else
	bad "does not end on the line this terminal needs"
fi
if works_here A; then ok "that line works in this terminal"; else bad "the printed line does not find mochiii"; fi
if says A "New terminals find it by themselves."; then ok "says new terminals find it"; else bad "says nothing of new terminals"; fi

echo "B. the same home, installed again"
if install A /bin/zsh; then ok "installed"; else bad "the second install failed"; fi
if [ "$(grep -c '.local/bin' "$h/.zshrc")" = 1 ]; then ok "still one line in ~/.zshrc"; else bad "a second line went into ~/.zshrc"; fi
if says A "This terminal cannot find mochiii yet"; then ok "still says the line"; else bad "no longer says the line"; fi

echo "C. a ~/.zshrc it cannot change"
h=$work/C
mkdir -p "$h" && echo 'echo hi' >"$h/.zshrc" && chmod 444 "$h/.zshrc"
if install C /bin/zsh; then ok "installed"; else bad "the installer stopped: $(tail -3 "$work/C.out")"; fi
if says C "cannot be changed, so new terminals will not find mochiii either"; then ok "warns"; else bad "no warning"; fi
if says C "is ready" && line C "    cd your-project && mochiii"; then ok "reached the end"; else bad "did not reach the end"; fi
if line C "    sudo chown \"\$USER\" \"$h/.zshrc\" && chmod u+w \"$h/.zshrc\""; then ok "prints the fix"; else bad "no fix printed"; fi
if [ "$(cat "$h/.zshrc")" = "echo hi" ]; then ok "left ~/.zshrc as it was"; else bad "changed ~/.zshrc"; fi
if says C "Permission denied"; then bad "the shell's own error reached the user"; else ok "no raw shell error"; fi
if works_here C; then ok "the printed line works in this terminal"; else bad "the printed line does not find mochiii"; fi

echo "D. a ~/.local it cannot write to"
h=$work/D
mkdir -p "$h/.local" && chmod 555 "$h/.local"
if install D /bin/zsh; then ok "installed"; else bad "the installer stopped: $(tail -3 "$work/D.out")"; fi
if says D "cannot be written to, so mochiii goes on your PATH from"; then ok "warns"; else bad "no warning"; fi
if grep -qF "$h/.mochiii/bin:" "$h/.zshrc" 2>/dev/null; then ok "the line in ~/.zshrc names ~/.mochiii/bin"; else bad "the line in ~/.zshrc does not name ~/.mochiii/bin"; fi
if works_here D; then ok "the printed line works in this terminal"; else bad "the printed line does not find mochiii"; fi

echo "E. ~/.local/bin already on PATH"
h=$work/E
mkdir -p "$h/.local/bin"
if env -i HOME="$h" PATH="$h/.local/bin:/usr/bin:/bin" SHELL=/bin/zsh TERM=dumb NO_COLOR=1 \
	MOCHIII_DOWNLOAD_BASE="$base" sh "$installer" --no-model >"$work/E.out" 2>&1; then
	ok "installed"
else
	bad "the installer failed"
fi
if says E "This terminal cannot find" || ! says E "Run it inside a project folder:"; then
	bad "asks for a line it does not need"
else
	ok "nothing to add"
fi
if [ -e "$h/.zshrc" ]; then bad "wrote a ~/.zshrc"; else ok "no ~/.zshrc written"; fi

echo "F. --no-modify-path"
h=$work/F
if install F /bin/zsh --no-modify-path; then ok "installed"; else bad "the installer failed"; fi
if says F "This terminal cannot find mochiii yet" && says F "add that line to $h/.zshrc"; then
	ok "says the line, and where to put it"
else
	bad "does not say the line and where to put it"
fi
if [ -e "$h/.zshrc" ]; then bad "wrote a ~/.zshrc"; else ok "no ~/.zshrc written"; fi

echo "G. fish"
h=$work/G
if install G /usr/bin/fish; then ok "installed"; else bad "the installer failed"; fi
if line G "    fish_add_path $h/.local/bin" && grep -qF "fish_add_path $h/.local/bin" "$h/.config/fish/config.fish"; then
	ok "fish's line, printed and written"
else
	bad "no fish line"
fi

exit $failed
