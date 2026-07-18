package editapply

import (
	"path/filepath"
	"strings"
)

// SecretFileGlobs match basenames (via filepath.Match) that must never be
// read into the index or targeted by an edit.
var SecretFileGlobs = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"id_rsa*",
	"*.p12",
}

// SecretSubstrings is a defense-in-depth net beyond the glob list: any
// basename containing one of these (case-insensitive) is refused too.
// Over-refusing is a safe failure mode for a security boundary; under-
// refusing isn't.
var SecretSubstrings = []string{"secret", "credential"}

// MatchesSecretName reports whether base (a file's basename) matches the
// indexer's and edit-applier's shared secret-file policy. This is the single
// source of truth for that policy — daemon's chunker.go and this package's
// ResolveSafeTargetPath both call it, rather than each keeping their own copy.
func MatchesSecretName(base string) bool {
	lower := strings.ToLower(base)
	for _, sub := range SecretSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	// Match the globs against the lowercased basename. filepath.Match has no
	// case-fold mode, so matching against the original-case base let case
	// variants (.ENV, *.PEM, *.KEY, *.P12, ID_RSA) slip through. The globs in
	// SecretFileGlobs are all lowercase, so comparing against lower is symmetric.
	for _, glob := range SecretFileGlobs {
		if ok, _ := filepath.Match(glob, lower); ok {
			return true
		}
	}
	return false
}
