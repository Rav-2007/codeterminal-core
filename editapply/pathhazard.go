package editapply

import (
	"fmt"
	"strings"
)

// Path shapes that mean something different to Win32 than they read as.
//
// WHY THIS RUNS ON EVERY PLATFORM, NOT UNDER //go:build windows. Two reasons,
// and the second is the one that actually decides it:
//
//  1. These are pure string checks. A Linux daemon indexing or editing a
//     workspace on an NTFS or SMB mount faces every hazard below, and a
//     build-tagged guard would miss all of them.
//  2. The gates they protect — ProtectedDirComponent and MatchesSecretName —
//     already fold case on every platform for exactly this reason. That fold
//     (S2, the ".GIT" bypass) was the first member of this family to be found,
//     and its fix was deliberately made unconditional. These are the rest of
//     the family.
//
// The `.pub` over-refusal precedent applies throughout: where a check might
// refuse something legitimate, it fails in the SAFE direction and says so,
// rather than being narrowed until it can be bypassed.

// reservedDeviceNames are Win32 device names. A file cannot have one, at any
// depth, with any extension: "CON.txt" opens the console, not a file.
//
// The failure they cause is not an escape, it is a hang or a silent discard —
// a write to NUL succeeds and goes nowhere, so the model is told its edit
// landed when nothing was written; a write to COM1 or PRN can block on a serial
// or printer port. Both are reachable from model-generated output, which is
// what makes them worth refusing rather than documenting.
var reservedDeviceNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SplitComponents splits a path on BOTH separators, on every platform.
//
// filepath.Split and strings.Split(p, string(filepath.Separator)) are the wrong
// tool here and the difference is a real bypass: on Linux, `.git\hooks\evil` is
// a SINGLE component named ".git\hooks\evil", which is not in ProtectedDirNames
// and therefore not protected — while on the NTFS share that path may well be
// written to, it is three components and the first is .git.
//
// Splitting on both always is strictly more conservative: a legitimate Linux
// filename containing a backslash is split into parts, and the only consequence
// is that its parts are ALSO checked against the protected and secret lists.
func SplitComponents(p string) []string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	out := parts[:0]
	for _, part := range parts {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

// NormalizeComponent puts one path component into the form the OS will actually
// resolve it in: case-folded, with trailing dots and spaces removed.
//
// Win32 strips trailing dots and spaces from every path component before
// resolving it, so ".git.", ".git " and ".git" all open the same directory —
// but only the last is in ProtectedDirNames. The same trick slips ".env " past
// MatchesSecretName and "id_rsa." past the private-key glob.
//
// Applied on every platform. On a genuinely POSIX filesystem a file really can
// be named ".git." and it is a different file — so this over-refuses there, in
// the safe direction, for a name no legitimate source tree uses.
func NormalizeComponent(c string) string {
	return strings.ToLower(strings.TrimRight(c, ". "))
}

// RejectPathHazards refuses a workspace-relative path whose shape would resolve
// to something other than what it reads as.
//
// It runs BEFORE the protected-directory and secret-name gates, because each
// hazard below is a way of writing a name those gates would otherwise not
// recognise.
func RejectPathHazards(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty path")
	}
	if len(rel) > 4096 {
		return fmt.Errorf("path exceeds maximum length limit of 4096 bytes")
	}

	// Control characters, Unicode RTL overrides, and invisible zero-width characters.
	// Never legitimate in a path, and used to spoof file extensions or slip past secret gates.
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path %q contains a control character", rel)
		}
		switch r {
		case '\u202A', '\u202B', '\u202C', '\u202D', '\u202E', '\u2066', '\u2067', '\u2068', '\u2069':
			return fmt.Errorf("path %q contains a Unicode directional override character", rel)
		case '\u200B', '\u200C', '\u200D', '\uFEFF':
			return fmt.Errorf("path %q contains an invisible zero-width character", rel)
		}
	}

	// A COLON anywhere is refused, which covers three distinct Win32 forms at
	// once and is why this is one check rather than three:
	//
	//   .git:x            an alternate data stream ON THE .git DIRECTORY.
	//                     Directories carry ADS on NTFS. Component-wise the path
	//                     is one part, ".git:x", which is not ".git" and so
	//                     passes the protected gate while reaching .git itself.
	//   .env:leak         the same trick against the secret-name gate.
	//   C:foo             DRIVE-RELATIVE, and the trap here is specific:
	//                     filepath.IsAbs("C:foo") is FALSE on Windows, so the
	//                     absolute-path gate above does not fire, and
	//                     filepath.Join(root, "C:foo") yields `root\C:foo`,
	//                     which NTFS reads as stream "foo" on a file named "C".
	//
	// Colons are legal in POSIX filenames, so this over-refuses there. That is
	// the same trade the `.pub` rule makes deliberately: a source file with a
	// colon in its name is vanishingly rare, and each of the three forms above
	// is a gate bypass.
	if strings.ContainsRune(rel, ':') {
		return fmt.Errorf("path %q contains a colon, which names an alternate data stream or a drive-relative path on Windows; refusing it", rel)
	}

	// UNC and extended-length forms. Both name a location outside the
	// workspace, and neither is caught by filepath.IsAbs on a POSIX build.
	if strings.HasPrefix(rel, `\\`) || strings.HasPrefix(rel, "//") {
		return fmt.Errorf("path %q is a UNC path; edits must target workspace-relative paths", rel)
	}

	// Win32 8.3 short names. An old 8.3 name (like GIT~1) bypasses the
	// ProtectedDirNames gate (which looks for .git). Rather than guessing
	// the short name, we refuse any component containing a tilde followed
	// by a digit, which is the universal shape of an 8.3 collision suffix.
	for i := 0; i < len(rel)-1; i++ {
		if rel[i] == '~' && rel[i+1] >= '0' && rel[i+1] <= '9' {
			return fmt.Errorf("path %q contains a tilde followed by a digit; refusing it as a potential Win32 8.3 short name bypass", rel)
		}
	}

	for _, part := range SplitComponents(rel) {
		if len(part) > 255 {
			return fmt.Errorf("path component %q exceeds maximum component length limit of 255 bytes", part)
		}
		norm := NormalizeComponent(part)
		if norm == "" {
			// A component that is nothing but dots and spaces resolves to its
			// parent or to nothing at all, depending on the OS.
			return fmt.Errorf("path %q has a component (%q) made only of dots and spaces", rel, part)
		}
		// A device name keeps its meaning with any extension: CON.txt is CON.
		base := norm
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if reservedDeviceNames[base] {
			return fmt.Errorf("path %q contains the reserved device name %q; a write there is discarded or blocks rather than reaching a file", rel, part)
		}
	}

	return nil
}
