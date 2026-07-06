package main

import (
	"path/filepath"
	"strings"
)

// FileClass buckets an indexed file into a coarse category used to tilt
// retrieval ranking (see rerank.go): prose ABOUT code should not routinely
// outrank the code itself for a code question, but a doc must still be
// findable when it's genuinely the best answer.
type FileClass string

const (
	FileClassCode   FileClass = "code"
	FileClassDoc    FileClass = "doc"
	FileClassConfig FileClass = "config"
	FileClassOther  FileClass = "other" // unrecognized extension/basename; treated as neutral
)

// codeExtensions are source-code file extensions (lowercased, including the
// leading dot, matching filepath.Ext's own format).
var codeExtensions = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".java": true, ".c": true, ".h": true, ".hpp": true, ".cc": true, ".cpp": true,
	".cs": true, ".rb": true, ".php": true, ".rs": true, ".swift": true, ".kt": true,
	".scala": true, ".sh": true, ".bash": true, ".zsh": true, ".sql": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".lua": true, ".r": true,
	".m": true, ".mm": true, ".pl": true, ".ex": true, ".exs": true, ".clj": true,
	".hs": true, ".vue": true, ".svelte": true, ".proto": true, ".graphql": true,
}

// docExtensions are prose-about-things extensions. This is the bucket that
// catches README.md and daemon/prompts/system.txt — files that DESCRIBE code
// but aren't themselves the answer to "how does X work in the code".
var docExtensions = map[string]bool{
	".md": true, ".mdx": true, ".txt": true, ".rst": true, ".adoc": true,
}

// configExtensions are structured config/data formats.
var configExtensions = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true,
}

// configBasenames are exact-name config files with no (or a misleading)
// extension.
var configBasenames = map[string]bool{
	"go.mod":         true,
	"makefile":       true,
	"dockerfile":     true,
	".gitattributes": true,
}

// classifyFile buckets relPath by extension, falling back to an exact
// basename match for extensionless config files, and FileClassOther for
// anything unrecognized (e.g. LICENSE). Pure function of the path — no I/O,
// so it can be recomputed identically at index time or query time.
func classifyFile(relPath string) FileClass {
	base := strings.ToLower(filepath.Base(relPath))
	ext := strings.ToLower(filepath.Ext(base))

	switch {
	case codeExtensions[ext]:
		return FileClassCode
	case docExtensions[ext]:
		return FileClassDoc
	case configExtensions[ext], configBasenames[base]:
		return FileClassConfig
	default:
		return FileClassOther
	}
}

// noiseBasenames are hard-excluded from the retrieval index entirely (never
// chunked, never classified, never findable) — not merely down-weighted.
// This is a narrow, exact-match list: .gitignore and dependency lockfiles.
// They can never usefully answer a code question (a lockfile is a hash/
// version dump; .gitignore is a list of patterns), unlike docs, which stay
// indexed and are only down-weighted by classWeight in rerank.go. Binary/
// generated junk is already excluded by the pre-existing sniffBinary check
// in chunker.go (SkipBinary) — nothing new needed for that.
var noiseBasenames = map[string]bool{
	".gitignore":        true,
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"cargo.lock":        true,
	"gemfile.lock":      true,
	"composer.lock":     true,
	"poetry.lock":       true,
	"pipfile.lock":      true,
}

// isNoiseFile reports whether relPath's basename is on the hard-exclude
// list.
func isNoiseFile(relPath string) bool {
	return noiseBasenames[strings.ToLower(filepath.Base(relPath))]
}
