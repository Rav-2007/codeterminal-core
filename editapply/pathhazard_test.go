package editapply

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// Win32 path shapes that resolve to something other than what they read as.
//
// UNLIKE THE REST OF THE WINDOWS PORT, THESE RUN HERE. Every vector below is a
// pure string check, so it is exercised on Linux in CI today and neuter-verified
// rather than labelled NOT RUN. That is the reason the checks live in
// pathhazard.go without a build tag: the hazards reach a Linux daemon working on
// an NTFS or SMB mount, and a build-tagged guard would have missed all of them.
//
// The family this belongs to: S2 was the ".GIT" case-fold bypass of the
// protected-directory gate, found and fixed. These are its siblings — the other
// ways to spell a name so the gate does not recognise it.

// hazardVectors must be refused outright, before any gate that could be fooled
// by them gets a chance to look.
func TestRejectPathHazards_RefusesEveryKnownForm(t *testing.T) {
	cases := []struct {
		path string
		why  string
	}{
		// Alternate data streams. A directory carries them on NTFS, so ".git:x"
		// reaches .git itself while reading as a single unprotected component.
		{`.git:x`, "ADS on a protected directory"},
		{`.git::$INDEX_ALLOCATION`, "the canonical directory-ADS form"},
		{`.env:leak`, "ADS slipping the secret-name gate"},
		{`src/config.json:hidden`, "ADS at depth"},

		// Drive-relative. filepath.IsAbs reports FALSE for these on Windows, so
		// the absolute-path gate does not fire, and Join produces a stream name.
		{`C:foo`, "drive-relative, reads as a stream on a file named C"},
		{`C:/Windows/System32/x`, "drive-absolute"},

		// UNC and extended-length: a location outside the workspace entirely.
		{`\\server\share\x`, "UNC"},
		{`//server/share/x`, "UNC with forward slashes"},
		{`\\?\C:\x`, "extended-length"},

		// ROOTED, which Win32 does not consider absolute because there is no
		// volume name. CI's first Windows test run found this the hard way:
		// filepath.IsAbs("/tmp/evil.txt") is FALSE there, resolveSafeTarget's
		// absolute-path gate never fired, and Join produced
		// <workspace>\tmp\evil.txt -- a successful write where two tests demanded
		// a refusal.
		//
		// ASSERTED HERE RATHER THAN ONLY THROUGH resolveSafeTarget, on purpose.
		// On POSIX that entry point refuses these upstream via IsAbs, so a test
		// driven through it passes on Linux whether this check exists or not and
		// proves nothing. Calling the gate directly is what makes the refusal
		// verifiable on the platform the developer is actually sitting at.
		{`/tmp/evil.txt`, "rooted POSIX path; IsAbs is false for it on Windows"},
		{`\Windows\System32\drivers\etc\hosts`, "rooted with a backslash, which is what Clean produces on Windows"},
		{`/`, "the root itself"},

		// Reserved device names, at any depth, with any extension.
		{`CON`, "console"},
		{`NUL`, "writes are silently discarded"},
		{`nul`, "device names are case-insensitive"},
		{`COM1`, "can block on a serial port"},
		{`LPT1`, "printer port"},
		{`PRN.txt`, "an extension does not make it a file"},
		{`src/aux.go`, "reserved at depth"},
		{"CON. ", "reserved, reached through the trailing-dot strip"},

		// Components that are nothing but dots and spaces.
		{`...`, "resolves to a parent or to nothing"},
		{`src/. .`, "same, at depth"},

		// Control characters.
		{"src/evil\x00.go", "NUL byte"},
		{"src/evil\n.go", "newline, which can split a log line"},

		// Unicode directional overrides and invisible characters.
		{"src/foo\u202Etxt.exe", "Unicode RTL override"},
		{"src/foo\u200Bbar.go", "Zero-width space"},

		// 8.3 short names
		{"GIT~1", "8.3 alias for .git"},
		{"src/PROGRA~1", "8.3 alias for Program Files"},

		// Path and component length limits.
		{strings.Repeat("a/", 2500), "path length exceeds limit"},
		{"src/" + strings.Repeat("a", 300) + ".go", "component length exceeds limit"},
	}

	for _, tc := range cases {
		if err := RejectPathHazards(tc.path); err == nil {
			t.Errorf("RejectPathHazards(%q) allowed it — %s", tc.path, tc.why)
		}
	}
}

// The gate must not refuse ordinary paths. An over-refusing guard gets narrowed
// by whoever hits it next, and a narrowed guard is how S2 happened.
func TestRejectPathHazards_AllowsOrdinaryPaths(t *testing.T) {
	for _, ok := range []string{
		"main.go",
		"src/main.go",
		"a/b/c/deep_test.go",
		"README.md",
		".github/workflows/build.yml",
		"my-file.name.with.dots.go",
		"conference/talk.md", // "con" is only reserved as a whole component
		"console.log",
		"src/nullable.go",
		"prnter.go",
		"Makefile",
	} {
		if err := RejectPathHazards(ok); err != nil {
			t.Errorf("RejectPathHazards(%q) refused a legitimate path: %v", ok, err)
		}
	}
}

// The backslash bypass, which is the one that is a live gate defeat on LINUX
// rather than only on Windows: `.git\hooks\evil` is a single component there,
// is not in ProtectedDirNames, and would be written straight into .git on any
// NTFS or SMB mount.
func TestProtectedDirComponent_BackslashSeparatorIsNotAHidingPlace(t *testing.T) {
	for _, p := range []string{
		`.git\hooks\evil`,
		`.ssh\authorized_keys`,
		`src\..\.git\config`,
		`.mochiii\backups\x`,
	} {
		if got := ProtectedDirComponent(p); got == "" {
			t.Errorf("ProtectedDirComponent(%q) = \"\" — a backslash-separated path hid a protected directory", p)
		}
	}
}

// Trailing dots and spaces are stripped by Win32 before resolution, so these
// reach the same directory as the bare name.
func TestIsProtectedDirName_TrailingDotsAndSpaces(t *testing.T) {
	for _, name := range []string{
		".git", ".GIT", ".Git",
		".git.", ".git ", ".GIT.", ".Git . ",
		".ssh.", ".AWS ", ".mochiii.",
	} {
		if !IsProtectedDirName(name) {
			t.Errorf("IsProtectedDirName(%q) = false — Win32 strips trailing dots and spaces, so this opens the protected directory", name)
		}
	}
	// Still not over-refusing.
	for _, name := range []string{"git", "gitignore", "notgit", "sshd", "aws-sdk"} {
		if IsProtectedDirName(name) {
			t.Errorf("IsProtectedDirName(%q) = true — over-refusing an ordinary directory", name)
		}
	}
}

// The same strip against the secret-name gate.
func TestMatchesSecretName_TrailingDotsAndSpaces(t *testing.T) {
	for _, name := range []string{
		".env", ".ENV", ".env.", ".env ", ".ENV. ",
		"id_rsa", "id_rsa.", "ID_RSA ",
		"secrets.yaml.", "private.pem ",
	} {
		if !MatchesSecretName(name) {
			t.Errorf("MatchesSecretName(%q) = false — Win32 strips trailing dots and spaces, so this opens the secret file", name)
		}
	}
	// The deliberate carve-outs must survive normalization.
	for _, name := range []string{"id_rsa.pub", "id_ed25519.pub", "main.go", "README.md"} {
		if MatchesSecretName(name) {
			t.Errorf("MatchesSecretName(%q) = true — over-refusing", name)
		}
	}
}

// SplitComponents is the shared primitive; pin it directly so a future
// "simplification" back to filepath.Separator is caught here rather than three
// gates away.
func TestSplitComponents_SplitsOnBothSeparatorsAlways(t *testing.T) {
	got := SplitComponents(`a\b/c\d`)
	want := []string{"a", "b", "c", "d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SplitComponents(`a\\b/c\\d`) = %v, want %v", got, want)
	}
	if got := SplitComponents("a//b/./c"); strings.Join(got, "|") != "a|b|c" {
		t.Errorf("empty and dot components should be dropped, got %v", got)
	}
}

func TestNormalizeComponent(t *testing.T) {
	for in, want := range map[string]string{
		".GIT.":   ".git",
		".git ":   ".git",
		"Foo. . ": "foo",
		"main.go": "main.go",
		"...":     "",
	} {
		if got := NormalizeComponent(in); got != want {
			t.Errorf("NormalizeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

// The hazard gate must be reachable through the REAL entry point, not only as a
// unit. This is the "test through the door the user comes in" rule: a gate that
// only a direct unit test reaches is a gate the product does not have.
func TestResolveSafeTargetPath_RefusesHazardsThroughTheRealEntryPoint(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		`.git:x`,
		`.env:leak`,
		`C:foo`,
		`NUL`,
		`src/con.txt`,
		`\\server\share\x`,
		`.git\hooks\evil`,
	} {
		if _, err := ResolveSafeTargetPath(real, p); err == nil {
			t.Errorf("ResolveSafeTargetPath allowed %q through the production entry point", p)
		}
	}
}

// TestRejectUnprintablePath_RefusesTheWholeControlClass asserts the PROPERTY
// rather than a list of examples: every rune unicode.IsControl calls a control
// character is refused, and every rune it does not is left to the other gates.
//
// Written as a sweep because the defect it pins was an ENUMERATED RANGE that
// covered half its own class. A test naming U+009B would have passed the
// moment someone added 0x9b to the predicate and gone on passing while the
// other thirty-one C1 codes stayed open. The loop below cannot: it walks the
// whole of Latin-1 plus the code points around it, so widening the class in
// Unicode's tables and narrowing it in this package cannot both stay true.
func TestRejectUnprintablePath_RefusesTheWholeControlClass(t *testing.T) {
	var refusedControls, missedControls int
	for r := rune(0); r <= 0x2100; r++ {
		if !unicode.IsControl(r) {
			continue
		}
		if err := RejectUnprintablePath("a" + string(r) + "b.txt"); err != nil {
			refusedControls++
			continue
		}
		missedControls++
		t.Errorf("RejectUnprintablePath allowed U+%04X, which unicode.IsControl calls a control character", r)
	}
	// The vacuity floor. A predicate that refused nothing, or a loop that
	// examined nothing, would reach the end of the sweep with zero errors.
	// Unicode defines exactly 65 control characters in this span (C0's 32, DEL,
	// and C1's 32); anything less means this test stopped checking.
	if refusedControls+missedControls != 65 {
		t.Fatalf("swept %d control characters, want 65; this test cannot have checked what it claims",
			refusedControls+missedControls)
	}
}

// TestRejectUnprintablePath_C1IsRefusedByName is the narrow companion: it names
// the four C1 codes that actually do something to a terminal, so a failure
// reads as "CSI is getting through" rather than as an arithmetic mismatch.
func TestRejectUnprintablePath_C1IsRefusedByName(t *testing.T) {
	for name, r := range map[string]rune{
		"NEL (next line)":                   0x85,
		"CSI (control sequence introducer)": 0x9b,
		"OSC (operating system command)":    0x9d,
		"APC (application program command)": 0x9f,
	} {
		if err := RejectUnprintablePath("tar" + string(r) + "get.txt"); err == nil {
			t.Errorf("RejectUnprintablePath allowed %s (U+%04X) into a file path", name, r)
		}
	}
}

// TestRejectUnprintablePath_LeavesOrdinaryTextAlone guards the other direction:
// widening the predicate to the whole control class must not start refusing
// paths people really type. U+00A0 and the CJK/emoji ranges are NOT control
// characters and must survive.
func TestRejectUnprintablePath_LeavesOrdinaryTextAlone(t *testing.T) {
	for _, p := range []string{
		"src/main.go",
		"docs/RÉSUMÉ.md",
		"pkg/日本語.go",
		"a b.txt", // NBSP: not a control character
		"emoji/🙂.txt",
	} {
		if err := RejectUnprintablePath(p); err != nil {
			t.Errorf("RejectUnprintablePath refused ordinary path %q: %v", p, err)
		}
	}
}
