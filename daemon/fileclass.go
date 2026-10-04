package main

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// FileClass buckets an indexed file into a coarse category used to tilt
// retrieval ranking (see rerank.go): prose ABOUT code should not routinely
// outrank the code itself for a code question, but a doc must still be
// findable when it's genuinely the best answer.
type FileClass string

const (
	FileClassCode   FileClass = "code"
	FileClassTest   FileClass = "test"
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
	case isTestFile(base):
		return FileClassTest
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

// isTestFile reports whether base (already lowercased) is a Go test file.
// Scoped to the one convention actually present in this repo (verified: no
// other test/spec naming pattern — e.g. *.spec.ts, *_test.py — appears
// anywhere in the tree); broaden this only once a measured query against a
// repo using another convention shows the same ranking failure.
func isTestFile(base string) bool {
	return strings.HasSuffix(base, "_test.go")
}

// jsLikeTestExtensions are the extensions whose test files are named
// name.test.ext or name.spec.ext.
var jsLikeTestExtensions = map[string]bool{".ts": true, ".tsx": true, ".js": true, ".jsx": true}

// isTestPathBeyondGo reports whether relPath (an index key, forward-slash) is
// a test by another language's convention: test_x.py or x_test.py, x.test.ts or
// x.spec.ts and their JavaScript siblings, XTest.java or XTests.java, or any
// file under a test, tests or __tests__ directory -- where Python, Rust,
// Java and JavaScript projects keep theirs. See rankPolicy.TestPathsBeyondGo.
func isTestPathBeyondGo(relPath string) bool {
	base := path.Base(relPath)
	lower := strings.ToLower(base)
	ext := path.Ext(lower)
	stem := strings.TrimSuffix(lower, ext)
	switch {
	case ext == ".py" && (strings.HasPrefix(lower, "test_") || strings.HasSuffix(stem, "_test")):
		return true
	case jsLikeTestExtensions[ext] && (strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")):
		return true
	// Case-sensitive on purpose: "latest.java" ends in "test.java" too.
	case ext == ".java" && (strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.java")):
		return true
	}
	for _, dir := range strings.Split(path.Dir(relPath), "/") {
		switch dir {
		case "test", "tests", "__tests__":
			return true
		}
	}
	return false
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

// setupFileStem is the base name, without its extension, that marks a setup
// file in any language.
var setupFileStem = regexp.MustCompile(`(?i)^(main|setup\w*|config\w*|init\w*)$`)

// isSetupFileBeyondGo reports whether path is a main, setup, config or init
// file in a code language other than Go, which setupFilesPattern covers.
func isSetupFileBeyondGo(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(base)
	return ext != ".go" && codeExtensions[ext] && setupFileStem.MatchString(strings.TrimSuffix(base, ext))
}
