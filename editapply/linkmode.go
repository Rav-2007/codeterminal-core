package editapply

import "io/fs"

// A WINDOWS JUNCTION IS NOT REPORTED AS A SYMLINK, AND SEVEN CHECKS ASSUMED IT
// WAS.
//
// Every place this product refuses to follow a link asked the same question:
//
//	info.Mode()&fs.ModeSymlink != 0
//
// On POSIX that is complete. On Windows it is not, and the gap is exactly the
// reparse point that needs NO PRIVILEGE to create. From os/types_windows.go
// (Go 1.23+, the winsymlink=1 default):
//
//	switch fs.ReparseTag {
//	case syscall.IO_REPARSE_TAG_SYMLINK:  m |= ModeSymlink
//	case windows.IO_REPARSE_TAG_AF_UNIX:  m |= ModeSocket
//	case windows.IO_REPARSE_TAG_DEDUP:    // treated as regular
//	default:                              m |= ModeIrregular
//	}
//
// IO_REPARSE_TAG_MOUNT_POINT -- a junction, what `mklink /J` makes -- falls to
// the default and comes back ModeIrregular. Before Go 1.23 it came back
// ModeSymlink (modePreGo1_23 special-cases it), so this is a change the
// toolchain made underneath code that was correct when it was written.
//
// WHAT IT COSTS, worst first. daemon/chunker.go's walk is a READ PRIMITIVE:
// whatever it indexes becomes retrievable text, which is fed into prompts and
// leaves the machine on the completion request. A junction planted anywhere in
// the workspace made the indexer walk straight out of it. Its own comment said
// "this check alone stops any symlink escape without needing to inspect the
// target" -- true on the platform it was written for, false on the other one.
// daemon/fileref.go's @file walk is the same primitive by a different door. The
// write-side and undo-side checks are less severe (the rename that commits
// still refuses a directory) but the contract was the same and so is the fix.
//
// Widening costs nothing on POSIX: os/types_unix.go never sets ModeIrregular at
// all -- the S_IFMT switch has no branch for it -- so on Linux and macOS this
// predicate is exactly the old one. On Windows it now also covers junctions,
// and anything else NTFS grows a reparse tag for, which is the conservative
// direction for a confinement check to fail in.
//
// This lives in editapply because editapply already owns the confinement
// vocabulary (RejectPathHazards, IsProtectedDirName, MatchesSecretName) and
// daemon imports it. One definition, not a mirrored pair, so the two can never
// drift.
const LinkLikeModes = fs.ModeSymlink | fs.ModeIrregular

// IsLinkLike reports whether mode describes something a confinement check must
// refuse to follow: a symlink on any platform, and on Windows also a junction
// or any other reparse point Go does not recognise.
//
// Pass fs.DirEntry.Type() or the result of an LSTAT -- never a Stat, which has
// already followed the link and will answer about the target.
func IsLinkLike(mode fs.FileMode) bool {
	return mode&LinkLikeModes != 0
}
