//go:build linux

// toolchainExposure is consulted only by the bwrap and Landlock backends, both
// Linux-only, and every case below is a Linux toolchain layout (GOROOT under
// ~/.local, ~/.cargo + ~/.rustup, nvm) exercised through a fixture that sets
// HOME -- which only governs ~ and os.UserHomeDir on Linux and macOS. On
// Windows the sandbox does not exist and these paths mean nothing, so the file
// is Linux-only rather than carrying a per-test skip.
package mcp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// toolchainExposure must grant every directory a toolchain needs to RUN, and
// never a directory broad enough to hold the user's credentials (OPEN_ITEMS
// item 45). Each case is a real on-disk layout under a temp HOME, with lookPath
// pointed at the fake binary, so the function is exercised exactly as it is in
// production (it stats the paths and resolves symlinks).

// fakeHome sets HOME to a fresh temp dir and points lookPath at a map of
// command -> absolute path. Returns the home.
func fakeHome(t *testing.T, bins map[string]string) string {
	t.Helper()
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	t.Setenv("HOME", home)
	t.Setenv("RUSTUP_HOME", "") // default to ~/.rustup unless a case sets it
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(cmd string) (string, error) {
		if p, ok := bins[cmd]; ok {
			return p, nil
		}
		return "", os.ErrNotExist
	}
	return home
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func exposes(exp []string, dir string) bool { return slices.Contains(exp, dir) }

// withinAny reports whether dir is one of exp or inside one of them -- i.e. the
// exposure grants read access to dir.
func grants(exp []string, dir string) bool {
	for _, e := range exp {
		if e == dir || coveredBy(dir, []string{e}) {
			return true
		}
	}
	return false
}

// go: the golang.org tarball layout. GOROOT (~/.local/go) holds bin/, lib/,
// pkg/ and must be exposed whole. It is under ~/.local but is a specific install
// dir, not ~/.local itself.
func TestToolchainExposure_GoRootExposedWholeButNotDotLocal(t *testing.T) {
	home := fakeHome(t, nil)
	goroot := filepath.Join(home, ".local", "go")
	touch(t, filepath.Join(goroot, "bin", "go"))
	mkdirAll(t, filepath.Join(goroot, "lib"))
	mkdirAll(t, filepath.Join(goroot, "pkg"))
	// A credential beside the generic parent, to prove the parent is not exposed.
	touch(t, filepath.Join(home, ".local", "share", "keyrings", "login.keyring"))
	setLookPath(t, "go", filepath.Join(goroot, "bin", "go"))

	exp := toolchainExposure("go")
	if !exposes(exp, goroot) {
		t.Errorf("GOROOT not exposed: %v", exp)
	}
	if grants(exp, filepath.Join(home, ".local")) || grants(exp, filepath.Join(home, ".local", "share", "keyrings")) {
		t.Errorf("~/.local (or the keyring under it) is exposed: %v", exp)
	}
	// go needs its lib and pkg: they are under the exposed GOROOT.
	if !grants(exp, filepath.Join(goroot, "lib")) || !grants(exp, filepath.Join(goroot, "pkg")) {
		t.Errorf("GOROOT's lib/pkg not reachable: %v", exp)
	}
}

// nvm: `npm` is a SYMLINK into ../lib/node_modules/npm. Resolving it alone lands
// in npm's package dir and loses `node`; the version dir (the PATH location's
// parent) holds both. The exposure must reach node and npm's lib.
func TestToolchainExposure_NvmNpmStillFindsNode(t *testing.T) {
	home := fakeHome(t, nil)
	ver := filepath.Join(home, ".nvm", "versions", "node", "v20.20.0")
	node := filepath.Join(ver, "bin", "node")
	touch(t, node)
	npmCli := filepath.Join(ver, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	touch(t, npmCli)
	npmLink := filepath.Join(ver, "bin", "npm")
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "npm", "bin", "npm-cli.js"), npmLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	setLookPath(t, "npm", npmLink)

	exp := toolchainExposure("npm")
	if !grants(exp, node) {
		t.Errorf("npm's node is not reachable, so npm cannot run: %v", exp)
	}
	if !grants(exp, filepath.Join(ver, "lib", "node_modules", "npm")) {
		t.Errorf("npm's own lib is not reachable: %v", exp)
	}
}

// cargo (rustup): ~/.cargo holds credentials.toml (a crates.io token). The
// install root ~/.cargo must NOT be exposed; ~/.cargo/bin and ~/.rustup must be.
func TestToolchainExposure_CargoExposesBinAndRustupNotDotCargo(t *testing.T) {
	home := fakeHome(t, nil)
	cargoBin := filepath.Join(home, ".cargo", "bin")
	touch(t, filepath.Join(cargoBin, "cargo"))
	creds := filepath.Join(home, ".cargo", "credentials.toml")
	touch(t, creds)
	rustup := filepath.Join(home, ".rustup")
	mkdirAll(t, filepath.Join(rustup, "toolchains"))
	setLookPath(t, "cargo", filepath.Join(cargoBin, "cargo"))

	exp := toolchainExposure("cargo")
	if grants(exp, filepath.Join(home, ".cargo")) && !exposes(exp, cargoBin) {
		t.Errorf("~/.cargo is exposed wholesale: %v", exp)
	}
	if grants(exp, creds) {
		t.Errorf("the crates.io token (credentials.toml) is exposed: %v", exp)
	}
	if !exposes(exp, cargoBin) {
		t.Errorf("~/.cargo/bin (the cargo binary) is not exposed, so cargo cannot run: %v", exp)
	}
	if !exposes(exp, rustup) {
		t.Errorf("~/.rustup (the toolchains) is not exposed, so cargo has nothing to build with: %v", exp)
	}
}

// cargo with RUSTUP_HOME pointing elsewhere: that dir is exposed, not ~/.rustup.
func TestToolchainExposure_CargoHonoursRustupHome(t *testing.T) {
	home := fakeHome(t, nil)
	touch(t, filepath.Join(home, ".cargo", "bin", "cargo"))
	touch(t, filepath.Join(home, ".cargo", "credentials.toml"))
	rustup := filepath.Join(t.TempDir(), "custom-rustup")
	mkdirAll(t, rustup)
	t.Setenv("RUSTUP_HOME", rustup)
	setLookPath(t, "cargo", filepath.Join(home, ".cargo", "bin", "cargo"))

	exp := toolchainExposure("cargo")
	if !exposes(exp, rustup) {
		t.Errorf("RUSTUP_HOME not exposed: %v", exp)
	}
}

// Item 45's generic case: a toolchain binary in ~/.local/bin makes the root
// ~/.local, which holds the OS keyring and Mochiii's state. Only ~/.local/bin is
// exposed, never ~/.local.
func TestToolchainExposure_GenericLocalBinDoesNotExposeDotLocal(t *testing.T) {
	home := fakeHome(t, nil)
	localBin := filepath.Join(home, ".local", "bin")
	touch(t, filepath.Join(localBin, "go"))
	touch(t, filepath.Join(home, ".local", "share", "keyrings", "login.keyring"))
	mkdirAll(t, filepath.Join(home, ".local", "state", "mochiii"))
	setLookPath(t, "go", filepath.Join(localBin, "go"))

	exp := toolchainExposure("go")
	if grants(exp, filepath.Join(home, ".local")) {
		t.Errorf("~/.local is exposed: %v", exp)
	}
	if grants(exp, filepath.Join(home, ".local", "share", "keyrings")) ||
		grants(exp, filepath.Join(home, ".local", "state", "mochiii")) {
		t.Errorf("a credential store under ~/.local is exposed: %v", exp)
	}
	if !exposes(exp, localBin) {
		t.Errorf("~/.local/bin (the binary) is not exposed: %v", exp)
	}
}

// A binary directly in the home directory exposes only ~/bin-equivalent, never
// the home itself (which holds ~/.ssh, ~/.aws, ...).
func TestToolchainExposure_HomeBinDoesNotExposeHome(t *testing.T) {
	home := fakeHome(t, nil)
	binDir := filepath.Join(home, "bin")
	touch(t, filepath.Join(binDir, "make"))
	touch(t, filepath.Join(home, ".ssh", "id_rsa"))
	setLookPath(t, "make", filepath.Join(binDir, "make"))

	exp := toolchainExposure("make")
	if grants(exp, home) {
		t.Errorf("the home directory is exposed: %v", exp)
	}
	if grants(exp, filepath.Join(home, ".ssh")) {
		t.Errorf("~/.ssh is exposed: %v", exp)
	}
}

// A ~/.local/bin/go SYMLINK into a real GOROOT ~/.local/go: the resolved target
// is the real toolchain, so GOROOT is exposed (go runs) while ~/.local is not.
func TestToolchainExposure_SymlinkIntoRealGoRoot(t *testing.T) {
	home := fakeHome(t, nil)
	goroot := filepath.Join(home, ".local", "go")
	realGo := filepath.Join(goroot, "bin", "go")
	touch(t, realGo)
	mkdirAll(t, filepath.Join(goroot, "pkg"))
	link := filepath.Join(home, ".local", "bin", "go")
	mkdirAll(t, filepath.Dir(link))
	if err := os.Symlink(realGo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	setLookPath(t, "go", link)

	exp := toolchainExposure("go")
	if !exposes(exp, goroot) {
		t.Errorf("the real GOROOT behind the symlink is not exposed, so go cannot run: %v", exp)
	}
	if grants(exp, filepath.Join(home, ".local")) {
		t.Errorf("~/.local is exposed: %v", exp)
	}
}

// A command not on PATH yields no exposure rather than a bogus directory.
func TestToolchainExposure_MissingCommand(t *testing.T) {
	fakeHome(t, nil)
	if exp := toolchainExposure("doesnotexist"); exp != nil {
		t.Errorf("a missing command exposed %v", exp)
	}
}

// setLookPath points lookPath at one command -> path (used after fakeHome).
func setLookPath(t *testing.T, cmd, path string) {
	t.Helper()
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(c string) (string, error) {
		if c == cmd {
			return path, nil
		}
		return "", os.ErrNotExist
	}
}
