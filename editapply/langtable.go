package editapply

import (
	"path/filepath"
	"strings"
)

// Language is what this product knows a file to be written in.
//
// THE ZERO VALUE IS "I DO NOT KNOW", AND THAT IS THE WHOLE POINT.
//
// Before this table existed the same eight-line switch appeared byte-identically
// in daemon/mcp_lsp.go and daemon/mcp_ast_edit.go, and both of them opened with
//
//	lang := "go"
//
// which is a DEFAULT, not a fallback. A .rs, .java, .rb or .c file matched
// neither case, kept "go", and was handed to gopls. gopls returned no symbols
// for it -- correctly, it is not Go -- so propose_ast_edit answered
// `symbol "X" not found in foo.rs`. The user was told their symbol did not
// exist. The truth was that we asked the wrong compiler, and the two failures
// are indistinguishable from the outside.
//
// So LanguageOf returns LangUnknown for anything it does not recognise, and
// every caller must handle that as its own case. There is no default here and
// none may be added: a wrong language is worse than no language, because no
// language produces an honest refusal naming the extension and a wrong one
// produces a confident lie about the user's code.
//
// The values are the LSP language identifiers, so the string a language server
// wants is the string this type already holds.
type Language string

const (
	// LangUnknown is the ONLY value for an extension this table does not list.
	// See the type comment: it is a decision, not a gap.
	LangUnknown    Language = ""
	LangGo         Language = "go"
	LangTypeScript Language = "typescript"
	LangJavaScript Language = "javascript"
	LangPython     Language = "python"
)

// extensionLanguages maps a lowercased file extension to its language.
//
// THIS IS NOT A MIME REGISTRY, and the restraint is deliberate. A language earns
// an entry here only when this product can DO something with the answer -- run a
// language server, run a syntax check, or give a refusal that is better for
// naming it. Listing Rust or Java without either would buy nothing: the refusal
// "no language server is configured for .rs files" is already the right message,
// and it is produced by the LangUnknown branch without a table entry. A table
// that grows past its capabilities is one whose entries stop meaning anything.
//
// The extensions beyond the original switch (.mjs, .cjs, .mts, .cts, .pyi) are
// not scope creep; they are the same bug. Every one of them was reaching gopls.
var extensionLanguages = map[string]Language{
	".go": LangGo,

	// Both TypeScript flavours and both JavaScript flavours are served by the
	// same binary (see serverCommand), so splitting them changes no routing.
	// They are split because the ANSWER differs: a .js file is not TypeScript,
	// and a note or a refusal that says so is more use than one that does not.
	".ts":  LangTypeScript,
	".tsx": LangTypeScript,
	".mts": LangTypeScript,
	".cts": LangTypeScript,

	".js":  LangJavaScript,
	".jsx": LangJavaScript,
	".mjs": LangJavaScript,
	".cjs": LangJavaScript,

	".py":  LangPython,
	".pyi": LangPython,
}

// LanguageOf reports the language of the file at relPath, or LangUnknown.
//
// It is a pure function of the PATH -- no I/O, no content sniffing -- so it can
// be called identically by a caller holding a file and by one holding only a
// proposed edit, and it costs nothing to call twice.
//
// The extension is folded to lower case, which preserves the strings.EqualFold
// comparison the syntax gate used before this table existed. That matters on the
// case-insensitive filesystems this product supports (APFS, NTFS), where FOO.GO
// and foo.go are the same file: a case-sensitive lookup here would let a
// case-varied name skip the syntax gate while still writing real Go. Same
// reasoning as IsProtectedDirName, one axis over.
func LanguageOf(relPath string) Language {
	return extensionLanguages[strings.ToLower(filepath.Ext(relPath))]
}

// KnownLanguages lists every language this table can produce, for messages that
// tell a user what IS supported after telling them what is not. Sorted by hand
// rather than derived, so the order in a user-facing sentence is stable and
// deliberate rather than map-iteration order.
func KnownLanguages() []Language {
	return []Language{LangGo, LangTypeScript, LangJavaScript, LangPython}
}
