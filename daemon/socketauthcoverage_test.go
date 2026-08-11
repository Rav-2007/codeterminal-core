package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EVERY PLACE THIS PRODUCT ADMITS A LOCAL PEER IS CLASSIFIED HERE.
//
// D1 and D3 (docs/DECISION_PACK.md) rest on ONE premise: the only party who can
// reach the daemon's request surface is an authenticated same-uid peer, verified
// by the kernel. D1 establishes it; D3's argument for keeping nine distinct
// refusal messages is explicitly built on it ("the only party who can reach this
// surface ... can lstat everything these messages disclose").
//
// Both rulings record the same cost of being wrong, in prose, in two separate
// documents: *if socket access is ever widened beyond same-uid, this must be
// revisited*. Nothing enforced that. The day the premise broke would have been
// caught by somebody remembering a paragraph.
//
// This test is that paragraph, made executable. Add an accept loop anywhere in
// the product and the build fails until it is classified -- which is the moment
// to reopen D1 and D3, not months later.
//
// It is the same discipline as the slash-catalog and model-allow-list parity
// tests: a comment cannot fail a build, and every invariant in this project that
// was maintained by comment has eventually drifted.

// peerAdmission is how one accept site establishes who it is talking to.
type peerAdmission int

const (
	// authorizesPeer: the file must call authorizePeer, which compares a
	// kernel-supplied peer credential against this daemon's own identity and
	// refuses on every failure path. This is the model D1 rules on.
	authorizesPeer peerAdmission = iota

	// transportPrimitive: a listener wrapper that returns a conn to a caller
	// which authorizes it. It admits nobody itself and must dispatch no
	// requests -- asserted below, so this classification cannot be used to park
	// a site that actually handles peers.
	transportPrimitive

	// filePermissionsOnly: no peer credential is checked. The socket's 0600 mode
	// and its 0700 parent directory are the whole access control. Permitted ONLY
	// with a recorded reason, because SECURITY_MODEL.md states the opposite rule
	// for the daemon socket: "File permissions are not the access control -- they
	// narrow who can reach the socket, but they do not identify who did."
	filePermissionsOnly
)

type acceptSite struct {
	admission peerAdmission
	// why is required for filePermissionsOnly and must be substantive. An
	// exemption nobody had to justify is an exemption nobody reviewed.
	why string
}

// The classified surface, repo-root-relative. A new entry here is a security
// decision; the test below refuses to pass while an accept site is unlisted.
var acceptSites = map[string]acceptSite{
	// The daemon's request socket -- the surface D1 and D3 are about.
	"daemon/server.go": {admission: authorizesPeer},

	// The embedder helper.
	"helper/main.go": {admission: authorizesPeer},

	// A listener wrapper, not an admission point.
	"protocol/transport_unix.go": {admission: transportPrimitive},
}

// dispatchMarkers are the things a transportPrimitive must NOT do. A wrapper
// that starts decoding requests has stopped being a wrapper, and parking it
// under that classification would hide exactly what this test looks for.
var dispatchMarkers = []string{"handleConn", "json.NewDecoder", "Decode("}

// stripGoComments removes // and /* */ comments so every check below reads CODE
// rather than prose about code.
//
// This is not tidiness. Without it, `strings.Contains(src, "authorizePeer")`
// matched server.go's comment "Fails closed. See authorizePeer." -- so deleting
// the actual call left this test green, on the single most important assertion
// it makes. Measured, and it is the third time this session a guard has been
// defeated by matching a word where a construct was meant.
//
// String literals are deliberately NOT stripped: `0600` and the dispatch markers
// are legitimately literals, and a comment is the only thing that can assert
// something the code does not do.
func stripGoComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); i++ {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end - 1 // leave the newline for the next iteration
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += 2 + end + 1
		default:
			out.WriteByte(src[i])
		}
	}
	return out.String()
}

func findAcceptSites(t *testing.T) []string {
	t.Helper()

	root := ".."
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The walk root is ".." -- a name that starts with a dot -- so the
			// dot-directory rule below must never see it, or the scan skips the
			// entire repository and reports a perfectly classified surface of
			// nothing. The count floor would catch that, but only by accident;
			// this makes it impossible rather than merely noticed.
			if path == root {
				return nil
			}
			// Dot-directories are never product source. This is not only .git:
			// .codeterminal/backups/ holds the edit-undo system's COPIES of real
			// source files, so a scan that walked it found `daemon/server.go`
			// three times -- once as code, twice as a snapshot of code -- and
			// demanded a security classification for a backup. Measured.
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			// testdata holds deliberate fakes (fakehelper, fakelsp) whose whole
			// job is to stand in for a real peer; vendored and build trees are
			// not our surface.
			case "node_modules", "testdata", "vendor", "out", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(raw), ".Accept()") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repo for accept sites: %v", err)
	}
	sort.Strings(found)
	return found
}

func TestEveryAcceptSiteIsClassifiedForPeerAuth(t *testing.T) {
	found := findAcceptSites(t)

	// ANTI-VACUITY. A walk that stops matching would report a clean, fully
	// classified surface forever -- the failure mode of every scanning test in
	// this repo, hit three times now.
	if len(found) < 3 {
		t.Fatalf("found only %d accept site(s): %v\nThe scanner must have broken (a moved "+
			"repo root, a renamed method, an over-broad skip). A pass here would mean "+
			"nothing, so this fails instead.", len(found), found)
	}

	for _, path := range found {
		site, listed := acceptSites[path]
		if !listed {
			t.Errorf("UNCLASSIFIED ACCEPT SITE: %s\n"+
				"Something in this product now admits a local peer at a place nobody has "+
				"ruled on. D1 and D3 (docs/DECISION_PACK.md) both rest on the premise that "+
				"every peer reaching the request surface is same-uid and kernel-verified; a "+
				"new accept loop is exactly the event whose 'cost of being wrong' those two "+
				"rulings describe.\n"+
				"Classify it in acceptSites, and if it does not call authorizePeer, reopen "+
				"D1 and D3 rather than adding an exemption quietly.", path)
			continue
		}

		raw, err := os.ReadFile(filepath.Join("..", path))
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		// Comments stripped: this must read what the file DOES, not what it says
		// about itself. See stripGoComments.
		src := stripGoComments(string(raw))

		switch site.admission {
		case authorizesPeer:
			// The classification must be true, not aspirational. Matched as a
			// CALL -- the bare name appears in this file's own doc comments, and
			// a name is not an invocation.
			if !strings.Contains(src, "protocol.AuthorizePeer(") {
				t.Errorf("%s is classified as authorizesPeer but never calls protocol.AuthorizePeer. "+
					"The peer-authentication step has been removed from a surface D1 and D3 "+
					"depend on.", path)
			}

		case transportPrimitive:
			for _, marker := range dispatchMarkers {
				if strings.Contains(src, marker) {
					t.Errorf("%s is classified as a transportPrimitive -- a wrapper that admits "+
						"nobody -- but it contains %q, so it is handling peers itself. Either it "+
						"authorizes them or the classification is wrong.", path, marker)
				}
			}

		case filePermissionsOnly:
			// An exemption is only meaningful if somebody had to write down why.
			if len(strings.TrimSpace(site.why)) < 80 {
				t.Errorf("%s is exempt from peer authentication with no substantive reason. "+
					"Every exemption here widens the premise D1 and D3 are ruled on, so the "+
					"reason is the review.", path)
			}
			// And it must genuinely restrict the socket, or "file permissions
			// only" describes no protection at all.
			if !strings.Contains(src, "0600") {
				t.Errorf("%s relies on file permissions as its ONLY access control but does not "+
					"chmod its socket to 0600. That is not a weaker model than peer auth -- it "+
					"is no model.", path)
			}
		}
	}

	t.Logf("classified %d accept site(s): %s", len(found), strings.Join(found, ", "))
}

// A classification map that has drifted from the tree is worse than none: it
// reads as a reviewed surface while describing a repo that no longer exists.
// An entry naming a file that is gone means a socket was deleted or moved
// without the security record following it.
func TestAcceptSiteClassificationsAllPointAtRealFiles(t *testing.T) {
	for path := range acceptSites {
		if _, err := os.Stat(filepath.Join("..", path)); err != nil {
			t.Errorf("acceptSites lists %s, which does not exist: %v\nA socket moved or was "+
				"deleted and the security classification did not follow it.", path, err)
		}
	}
}

// stripGoComments is the reason the checks above test code rather than prose,
// so it gets its own coverage: if it silently stopped stripping, every check
// would go back to matching doc comments and this whole file would soften into
// a spell-checker.
func TestStripGoComments(t *testing.T) {
	cases := []struct {
		name, in, wantGone, wantKept string
	}{
		{
			name:     "line comment naming a function it does not call",
			in:       "x := 1 // See authorizePeer.\ny := 2\n",
			wantGone: "authorizePeer",
			wantKept: "y := 2",
		},
		{
			name:     "block comment",
			in:       "a := 1\n/* handleConn lives elsewhere */\nb := 2\n",
			wantGone: "handleConn",
			wantKept: "b := 2",
		},
		{
			name:     "unterminated block comment does not panic or leak",
			in:       "a := 1\n/* handleConn\n",
			wantGone: "handleConn",
			wantKept: "a := 1",
		},
		{
			name:     "trailing line comment with no newline",
			in:       "a := 1 // authorizePeer",
			wantGone: "authorizePeer",
			wantKept: "a := 1",
		},
	}

	for _, tc := range cases {
		got := stripGoComments(tc.in)
		if strings.Contains(got, tc.wantGone) {
			t.Errorf("%s: %q survived stripping:\n%q", tc.name, tc.wantGone, got)
		}
		if !strings.Contains(got, tc.wantKept) {
			t.Errorf("%s: stripping ate real code %q:\n%q", tc.name, tc.wantKept, got)
		}
	}

	// Code must survive intact -- an over-eager stripper that removed the call
	// would fail the daemon's check for the opposite reason and send someone
	// hunting a security regression that does not exist.
	code := "if err := protocol.AuthorizePeer(conn); err != nil { // fail closed\n\treturn\n}\n"
	if !strings.Contains(stripGoComments(code), "protocol.AuthorizePeer(conn)") {
		t.Error("stripGoComments removed a real call alongside its trailing comment")
	}
}

// THE GUARD ON THE GUARD. The scanner's anchor is the literal ".Accept()", and
// if that ever stops matching, TestEveryAcceptSiteIsClassifiedForPeerAuth fails
// on the count floor rather than passing emptily -- but only while the floor is
// above what a broken scan returns. This pins the anchor against the real tree:
// the daemon's own accept loop must be found, by scan, every time.
func TestAcceptScannerFindsTheDaemonsOwnSocket(t *testing.T) {
	found := findAcceptSites(t)
	for _, p := range found {
		if p == "daemon/server.go" {
			return
		}
	}
	t.Fatalf("the scanner did not find daemon/server.go, which is the one accept loop this "+
		"test suite is certain exists. The anchor is broken and every other result here is "+
		"meaningless. Found: %v", found)
}
