package protocol

// THE DRIVE LETTER IS NOT THE CALLER'S JOB, because leaving it there cost us
// Windows entirely. WorkspaceTag (protocol.go) asks its callers for one
// canonical root, and for the drive letter that was an invariant nothing
// enforced and neither caller could satisfy: VS Code's uri.fsPath through
// fs.realpathSync hands the extension a LOWER-case drive letter, while
// filepath.Abs through filepath.EvalSymlinks hands the daemon an UPPER-case
// one. Same folder, one byte apart, two tags. Captured on a real Windows
// machine 2026-10-11:
//
//	c:\Users\shaik\OneDrive\Desktop\catalyst -> 03ea217e0c967379 (extension waited here)
//	C:\Users\shaik\OneDrive\Desktop\catalyst -> 3b707727f6517a4d (daemon listened here)
//
// So normalizeRoot runs INSIDE WorkspaceTag, where no call site can forget it
// and no new call site can reintroduce the bug.
//
// WHY THIS IS A FILE OF ITS OWN (2026-10-13). It was written into protocol.go,
// forty lines of it, a hundred lines from the top. The retrieval eval indexes
// this repository in fixed windows of lines, so lines added high in a long file
// move every window under them: with eighteen more lines further down, it cost
// the eval two questions -- 40 to 38 in CI, one above the floor, and 38 to 36
// on the machine it was found on, which is under it. Nothing had changed in
// retrieval. protocol.go's lines are back where they were. The same rule, and
// the measurement behind it, is in daemon/serverextras.go.

// normalizeRoot upper-cases a leading ASCII drive letter and touches nothing
// else. It is the one piece of canonicalisation WorkspaceTag cannot delegate to
// its callers -- see the 2026-10-11 capture above for what delegating it did.
//
// UP, NEVER DOWN. The daemon already produces C:\..., so upper-casing leaves
// every tag the daemon has ever written UNCHANGED and moves only the extension
// onto it. Lower-casing would reach agreement just as well, on a third tag that
// nothing is listening on yet -- every session open across the upgrade would
// break, to fix a bug those users had already worked around or walked away from.
//
// PURE STRING WORK, deliberately: no filepath, no os, nothing that reads GOOS.
// A Linux daemon is routinely asked about Windows-shaped paths (and the shared
// golden vectors in workspacetag_test.go assert exactly that, on Linux CI),
// where filepath.VolumeName returns "" and would quietly restore the outage.
func normalizeRoot(realRoot string) string {
	// "C:" is the shortest path that can carry a drive letter.
	if len(realRoot) < 2 || realRoot[1] != ':' {
		return realRoot
	}
	c := realRoot[0]
	if c < 'a' || c > 'z' {
		return realRoot
	}
	return string(c-'a'+'A') + realRoot[1:]
}
