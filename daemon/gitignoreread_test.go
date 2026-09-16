package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseGitignoreLayer_DoesNotMaterialiseTheWholeFile is the second site, and
// it is the one read in chunker.go that does NOT re-run the gate.
//
// chunker.go's own readEligibleFile re-runs shouldSkipFile (which enforces
// maxFileSize) immediately before reading, and planmode.go states the rule in
// general: the read side must not assume the write side ran. parseGitignoreLayer
// calls os.ReadFile on a .gitignore found while walking, with no size check
// anywhere in its path.
//
// Lower severity than the LSP site and filed that way rather than inflated to
// match: the path is derived from a directory walk, not chosen by the model, so
// an attacker needs a file already in the user's workspace. But the SIZE is
// decided by that file, and search_code and repo_map both reach it.
func TestParseGitignoreLayer_DoesNotMaterialiseTheWholeFile(t *testing.T) {
	const big = 32 << 20 // 32 MiB, 32x maxFileSize

	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	// Real ignore syntax, so the parse has work to do and cannot short-circuit.
	line := "build/ignored-" + strings.Repeat("a", 200) + "\n"
	var b strings.Builder
	for b.Len() < big {
		b.WriteString(line)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("writing the oversized .gitignore: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Vacuity floor: if the fixture is not actually oversized, the assertion
	// below is about nothing.
	if info.Size() < big {
		t.Fatalf("vacuity floor: fixture is %d bytes, wanted at least %d", info.Size(), big)
	}

	var layer *gitignoreLayer
	used := allocDuring(func() { layer = parseGitignoreLayer(path) })
	t.Logf("parseGitignoreLayer over a %d-byte .gitignore: allocated %d bytes, %d rule(s)",
		info.Size(), used, len(layer.rules))

	if used > maxFileSize*4 {
		t.Errorf("parseGitignoreLayer allocated %d bytes for a %d-byte .gitignore, against "+
			"maxFileSize of %d.\n"+
			"os.ReadFile materialises the whole file before any line is looked at, and nothing "+
			"in this path re-runs shouldSkipFile. readEligibleFile in this same file re-runs it "+
			"immediately before reading, for exactly this reason.", used, info.Size(), maxFileSize)
	}
}

// TestParseGitignoreLayer_StillParsesAGitignoreItShouldRead is the positive
// control, and without it the test above is satisfied by a fix that breaks
// everything.
//
// That test asserts a 33.5 MB file yields a small allocation, and it reports "0
// rule(s)". A bound that returned an empty layer for EVERY file would pass it
// perfectly while silently switching gitignore handling off -- and the
// consequence is not cosmetic: nested .gitignore support was a security fix
// (S1), so a layer that always parses to nothing re-opens it.
func TestParseGitignoreLayer_StillParsesAGitignoreItShouldRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	const body = "# a comment\n\nbuild/\n!build/keep.txt\n*.log\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	layer := parseGitignoreLayer(path)
	if len(layer.rules) != 3 {
		t.Fatalf("parsed %d rule(s) from an ordinary .gitignore, wanted 3 (build/, "+
			"!build/keep.txt, *.log). The bound must not cost the common path.", len(layer.rules))
	}
	// The three rules differ in the two flags the parse has to get right, so this
	// also pins that the bounded read did not disturb the parse.
	var sawDirOnly, sawNegate bool
	for _, r := range layer.rules {
		if r.dirOnly {
			sawDirOnly = true
		}
		if r.negate {
			sawNegate = true
		}
	}
	if !sawDirOnly || !sawNegate {
		t.Errorf("rules parsed but the flags did not: dirOnly=%v negate=%v", sawDirOnly, sawNegate)
	}
}

// TestParseGitignoreLayer_BoundIsTheBoundary pins both sides of the limit, since
// an off-by-one here silently drops a legitimate file's rules or admits an
// oversized one.
func TestParseGitignoreLayer_BoundIsTheBoundary(t *testing.T) {
	// A file of exactly maxGitignoreBytes, made of whole rule lines so the count
	// is predictable.
	//
	// FOUR bytes, not three, and the first version used three. maxGitignoreBytes
	// is 1 MiB, which is not a multiple of 3, so the guard below SKIPPED the whole
	// test and it reported no failure -- a skipped arm reads like a passing one in
	// every summary that counts tests. The Fatalf is what a mismatch deserves: the
	// fixture cannot be built, so the assertion cannot be made, and that is a
	// broken test rather than an inapplicable one.
	const line = "xy/\n" // 4 bytes, and 1 MiB is a multiple of 4
	if maxGitignoreBytes%len(line) != 0 {
		t.Fatalf("maxGitignoreBytes (%d) is not a multiple of %d, so this test cannot build a "+
			"file of exactly the bound. Change the line, not this check -- skipping here would "+
			"hide the boundary entirely.", maxGitignoreBytes, len(line))
	}
	want := maxGitignoreBytes / len(line)

	for _, tc := range []struct {
		name  string
		size  int
		rules int
	}{
		{"exactly at the bound is read", maxGitignoreBytes, want},
		{"one byte over the bound is refused", maxGitignoreBytes + len(line), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			if err := os.WriteFile(path, []byte(strings.Repeat(line, tc.size/len(line))), 0o600); err != nil {
				t.Fatalf("writing: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if int(info.Size()) != tc.size {
				t.Fatalf("vacuity floor: fixture is %d bytes, wanted exactly %d", info.Size(), tc.size)
			}
			if got := len(parseGitignoreLayer(path).rules); got != tc.rules {
				t.Errorf("a %d-byte .gitignore parsed to %d rule(s), wanted %d (bound is %d)",
					info.Size(), got, tc.rules, maxGitignoreBytes)
			}
		})
	}
}
