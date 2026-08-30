package editapply

import (
	"path/filepath"
	"strings"
	"testing"
)

// THE BUG THIS FILE EXISTS FOR, stated as a test.
//
// Two byte-identical extension switches opened with `lang := "go"`, so a .rs
// file was handed to gopls, which correctly found no symbols in it -- and the
// caller reported that as "symbol not found", telling the user their code was
// wrong. A default is not a fallback. There is no default here.
func TestUnknownExtensionHasNoLanguage(t *testing.T) {
	for _, path := range []string{
		"src/main.rs",
		"Widget.java",
		"lib.c",
		"lib.h",
		"app.rb",
		"notes.txt",
		"README.md",
		"Makefile",
		"noextension",
		"",
		".",
		"weird.",
	} {
		if got := LanguageOf(path); got != LangUnknown {
			t.Errorf("LanguageOf(%q) = %q, want LangUnknown. A wrong language is worse than "+
				"no language: no language produces an honest refusal naming the extension, "+
				"and a wrong one produces a confident lie about the user's code.", path, got)
		}
	}
}

func TestLanguageOfRecognisesWhatItClaims(t *testing.T) {
	for path, want := range map[string]Language{
		"main.go":               LangGo,
		"a/b/c/deep_test.go":    LangGo,
		"app.ts":                LangTypeScript,
		"Component.tsx":         LangTypeScript,
		"config.mts":            LangTypeScript,
		"config.cts":            LangTypeScript,
		"index.js":              LangJavaScript,
		"Component.jsx":         LangJavaScript,
		"esm.mjs":               LangJavaScript,
		"cjs.cjs":               LangJavaScript,
		"train.py":              LangPython,
		"stubs.pyi":             LangPython,
		"clients/vscode/x.d.ts": LangTypeScript,
	} {
		if got := LanguageOf(path); got != want {
			t.Errorf("LanguageOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// The fold is not cosmetic. On the case-insensitive filesystems this product
// supports (APFS, NTFS) FOO.GO and foo.go are THE SAME FILE, so a case-sensitive
// lookup would let a case-varied name skip the syntax gate while still writing
// real Go to disk -- the same shape as the .GIT bypass IsProtectedDirName folds
// for. On a case-sensitive filesystem the fold is merely redundant.
func TestExtensionMatchIsCaseInsensitive(t *testing.T) {
	for _, path := range []string{"MAIN.GO", "Main.Go", "main.gO"} {
		if got := LanguageOf(path); got != LangGo {
			t.Errorf("LanguageOf(%q) = %q, want LangGo. A case-varied extension reaches the "+
				"same file on APFS and NTFS, so it must reach the same gate.", path, got)
		}
	}
}

// A table entry that no constant names, or a constant no entry reaches, is a
// half-made decision: the first routes files to a language nothing handles, the
// second advertises a language nothing can be.
func TestTableAndConstantsAgree(t *testing.T) {
	if len(extensionLanguages) == 0 {
		t.Fatal("the extension table is empty; every test in this file would be vacuous")
	}

	declared := map[Language]bool{}
	for _, l := range KnownLanguages() {
		declared[l] = true
	}
	if declared[LangUnknown] {
		t.Error("KnownLanguages() includes LangUnknown. It is the absence of a language, " +
			"not one of them, and listing it would put \"\" in a user-facing sentence.")
	}

	reached := map[Language]bool{}
	for ext, lang := range extensionLanguages {
		if lang == LangUnknown {
			t.Errorf("extension %q maps to LangUnknown; delete the row instead, or the "+
				"table claims to know something it does not", ext)
		}
		if !declared[lang] {
			t.Errorf("extension %q maps to %q, which KnownLanguages() does not list", ext, lang)
		}
		reached[lang] = true
	}
	for _, lang := range KnownLanguages() {
		if !reached[lang] {
			t.Errorf("KnownLanguages() names %q but no extension produces it, so no file "+
				"can ever be it", lang)
		}
	}
}

// Every key must be in the exact shape filepath.Ext returns, or it can never
// match: a missing dot, an upper-case letter or a stray path separator makes a
// row dead code that reads as though it works.
func TestTableKeysAreInFilepathExtForm(t *testing.T) {
	for ext := range extensionLanguages {
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("table key %q has no leading dot; filepath.Ext never returns that", ext)
		}
		if ext != strings.ToLower(ext) {
			t.Errorf("table key %q is not lower-case; LanguageOf folds the input, so this "+
				"row is unreachable", ext)
		}
		if got := filepath.Ext("file" + ext); got != ext {
			t.Errorf("table key %q is not a well-formed extension (filepath.Ext gave %q)", ext, got)
		}
	}
}
