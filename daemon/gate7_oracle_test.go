package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// Gate 7's existence-oracle finding, measured rather than asserted.
//
// The 2026-07-19 audit confirmed that Apply returns DISTINGUISHABLE responses
// by filesystem state, so which error fires is itself an oracle even where no
// path string prints. It flagged the finding and did not fix it: the recorded
// remedy is to unify every error response into one generic string, which is a
// large behavioural change.
//
// This test does two things the record could not:
//
//  1. It ENUMERATES the oracle, so the decision about whether to unify is taken
//     against the actual set of distinguishable responses rather than against a
//     description of one. What each state yields is in the table below.
//  2. It PINS THE PART THAT MATTERS — that none of them carries an absolute
//     host path — across every state, where the existing scrub tests cover
//     three. The hygiene property was the real residual (an error string can
//     travel off-box in a transcript or a pasted bug report); the oracle itself
//     is reachable only by an authenticated same-uid peer, who can lstat
//     everything it discloses.
//
// If the founder rules that responses should be unified, this test is what
// tells you the ten messages that have to collapse and what each one currently
// buys a user. If they rule they should not, this is what stops the hygiene
// half regressing.
func TestGate7_ApplyOracleIsEnumeratedAndCarriesNoAbsolutePaths(t *testing.T) {
	root := realTempDir(t)
	outside := t.TempDir()

	writeTempFile(t, root, "readable.go", "package main\n\nfunc keep() {}\n")
	writeTempFile(t, root, "nomatch.go", "package main\n\nfunc other() {}\n")
	writeTempFile(t, root, "unreadable.go", "package main\n\nfunc keep() {}\n")
	// denyReads, not chmod 0000: the latter is a no-op for READING on Windows,
	// so this state was not the state under test there. The helper verifies the
	// denial took and reports why if it could not.
	unreadableDenied, unreadableWhy := denyReads(t, filepath.Join(root, "unreadable.go"))
	writeTempFile(t, root, "id_rsa_secret", "x")
	if err := os.WriteFile(filepath.Join(outside, "target.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0755); err != nil {
		t.Fatal(err)
	}

	srv := &Server{logger: discardLogger(), workspace: root}
	roots := resolvedRoots(t, root)

	// wantSubstring is what the message must still say, i.e. what a user loses
	// if these are ever unified. Empty means the state succeeds.
	cases := []struct {
		state         string
		path          string
		search        string
		wantSubstring string
	}{
		{"absent", "ghost.go", "func keep() {}", "access denied or does not exist"},
		{"absent nested", "a/b/c/ghost.go", "func keep() {}", "access denied or does not exist"},
		{"present, no match", "nomatch.go", "func keep() {}", "search text not found"},
		{"present, matches", "readable.go", "func keep() {}", ""},
		{"unreadable", "unreadable.go", "func keep() {}", "access denied or does not exist"}, // unified shape
		{"secret-named", "id_rsa_secret", "x", "secret-file rules"},
		{"symlink outside root", "link.go", "func keep() {}", "resolves outside the workspace root"},
		{"a directory", "adir", "func keep() {}", "is a directory"},
		{"escapes the root", "../outside.go", "func keep() {}", "escapes the workspace root"},
		{"protected directory", ".git/config", "x", "refusing to edit it"},
	}

	seen := map[string]string{}
	for _, c := range cases {
		if c.state == "unreadable" && !unreadableDenied {
			t.Logf("NOT RUN: the unreadable state (%s); the oracle's permission-denied branch "+
				"is UNVERIFIED here and the count below is adjusted for it", unreadableWhy)
			continue
		}
		resp := applyEditViaHandler(t, srv, protocol.ApplyEditRequest{
			ProtocolVersion: protocol.ProtocolVersion,
			Workspace:       root,
			Edit:            protocol.EditBlockWire{FilePath: c.path, Search: c.search, Replace: "func kept() {}"},
		})

		if c.wantSubstring == "" {
			if !resp.Applied {
				t.Errorf("%s: expected the edit to apply, got error %q", c.state, resp.Error)
			}
			continue
		}
		if resp.Applied {
			t.Errorf("%s: edit applied but should have been refused", c.state)
			continue
		}

		// THE GATE-7 GUARANTEE, on every state rather than three of them.
		assertNoAbsRoot(t, c.state, resp.Error, roots)

		// And the reason it is worth keeping distinguishable.
		if !strings.Contains(resp.Error, c.wantSubstring) {
			t.Errorf("%s: error = %q, want it to still say %q — a user acts on this",
				c.state, resp.Error, c.wantSubstring)
		}
		if prev, dup := seen[resp.Error]; dup {
			t.Errorf("%s and %s now return the identical message %q; if that was deliberate, this test is the record of what was collapsed",
				c.state, prev, resp.Error)
		}
		seen[resp.Error] = c.state
	}

	// The oracle's size, stated as a number so a change to it is visible in a
	// diff rather than only in behaviour.
	want := 9
	if !unreadableDenied {
		want-- // the state above that could not be built here
	}
	if len(seen) != want {
		t.Errorf("Apply distinguishes %d refusal states, expected %d — the oracle changed shape; update the Gate 7 decision brief with it", len(seen), want)
	}
}
