package editapply

import (
	"path/filepath"
	"strings"
)

// SecretFileGlobs match basenames (via filepath.Match, against the lowercased
// basename — see MatchesSecretName) that must never be read into the index or
// targeted by an edit. All patterns are lowercase so the case-fold match is
// symmetric. The gate is basename-only (no path context is available), so
// patterns that would otherwise rely on a directory (kubeconfig under .kube/,
// service-account JSON under a creds path) are shaped to match on the basename
// alone without over-matching ordinary files.
var SecretFileGlobs = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"id_rsa*",
	"*.p12",

	// SSH private keys other than RSA. Exact names (no trailing "*") so the
	// matching ".pub" PUBLIC keys — which are not secrets — are NOT flagged.
	// (Note: the older id_rsa* above does still flag id_rsa.pub; that is
	// pre-existing, harmless over-refusal, and left unchanged by this task.)
	"id_ed25519",
	"id_ecdsa",
	"id_dsa",

	// Credential-bearing dotfiles (auth tokens / passwords in the file itself).
	".npmrc",
	".netrc",
	".pgpass",

	// Kubernetes configs carry embedded client certs/tokens. Basename-only, so
	// a bare file literally named "config" (the .kube/config case) is NOT
	// caught — matching bare "config" would flag far too many ordinary files.
	// Content scrubbing (Layer 2) is what covers that residual gap.
	"*kubeconfig*",

	// PKCS#12 cert+key bundles (sibling of the existing *.p12).
	"*.pfx",

	// Terraform state — routinely contains secrets in plaintext. The .backup
	// sibling needs its own pattern (*.tfstate does not match the trailing
	// ".backup").
	"*.tfstate",
	"*.tfstate.backup",

	// Cloud service-account key files. Scoped to the "service-account" name so
	// bare *.json (package.json, tsconfig.json, ...) is NOT swept in; the
	// GCP-style *-<hash>.json form is deliberately NOT matched here (too broad
	// for a basename gate) and is left to Layer 2.
	"*service-account*.json",
	"*serviceaccount*.json",
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
