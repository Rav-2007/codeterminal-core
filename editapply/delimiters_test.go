package editapply

import (
	"strings"
	"testing"
)

// TestScanDelimiters_SkipStates covers the constructs that would false-positive
// without skip-state tracking.
//
// Every case here is a bracket that is NOT structure — it is inside a string, a
// comment, a template, a docstring or a regex. Without the skip states each one
// reports an imbalance on a perfectly good file, and a note that fires on good
// files is a note people learn to ignore.
func TestScanDelimiters_SkipStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang Language
		src  string
		want delimiterVerdict
	}{
		{"plain balanced ts", LangTypeScript, "function f() { return [1, 2]; }\n", delimBalanced},
		{"brace in a double-quoted string", LangTypeScript, "const a = \"{\";\n", delimBalanced},
		{"brace in a single-quoted string", LangTypeScript, "const a = '}';\n", delimBalanced},
		{"escaped quote inside a string", LangTypeScript, "const a = \"he said \\\" { \";\n", delimBalanced},
		{"brace in a line comment", LangTypeScript, "// {\nconst a = 1;\n", delimBalanced},
		{"brace in a block comment", LangTypeScript, "/* { { { */\nconst a = 1;\n", delimBalanced},
		{"brace in a template literal", LangTypeScript, "const a = `a { b`;\n", delimBalanced},
		{"template interpolation nests back into code", LangTypeScript, "const a = `x ${ {k: 1}.k } y`;\n", delimBalanced},
		{"nested template inside interpolation", LangTypeScript, "const a = `x ${ `y ${ 1 }` } z`;\n", delimBalanced},
		{"brace inside a regex literal", LangJavaScript, "const re = /a{2}/;\nconst b = 1;\n", delimBalanced},
		{"bracket class inside a regex", LangJavaScript, "const re = /[/{]/;\nconst b = 1;\n", delimBalanced},
		{"division is not a regex", LangJavaScript, "const a = (x) / 2;\nconst b = 3 / 4;\n", delimBalanced},

		{"python docstring full of braces", LangPython, "def f():\n    \"\"\"a { b { c\"\"\"\n    return 1\n", delimBalanced},
		{"python hash comment", LangPython, "# {\nx = 1\n", delimBalanced},
		{"python dict", LangPython, "d = {'a': [1, 2], 'b': (3,)}\n", delimBalanced},

		// The actual defects it exists to catch.
		{"unclosed brace", LangTypeScript, "function f() {\n  return 1;\n", delimUnbalanced},
		{"extra closer", LangTypeScript, "function f() { return 1; }\n}\n", delimUnbalanced},
		{"crossed pair", LangTypeScript, "const a = [1, 2);\n", delimUnbalanced},
		{"unclosed python bracket", LangPython, "d = {'a': 1\n", delimUnbalanced},

		// Indeterminate: structure this scan cannot read at all.
		{"unterminated block comment", LangTypeScript, "/* never closed\nconst a = 1;\n", delimIndeterminate},
		{"unterminated string", LangTypeScript, "const a = \"open;\n", delimIndeterminate},
		{"unterminated template", LangTypeScript, "const a = `open;\n", delimIndeterminate},
		{"unterminated python docstring", LangPython, "\"\"\"open\nx = 1\n", delimIndeterminate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scanDelimiters(tc.lang, tc.src)
			if got.Verdict != tc.want {
				t.Errorf("scanDelimiters = %v (%s), want %v", got.Verdict, got.Detail, tc.want)
			}
		})
	}
}

// TestScanDelimiters_ReportsWhereNotJustThat pins the message, because a note
// saying only "unbalanced" sends a reader to re-read the whole file.
func TestScanDelimiters_ReportsWhereNotJustThat(t *testing.T) {
	got := scanDelimiters(LangTypeScript, "const a = 1;\nfunction f() {\n  return 2;\n")
	if got.Verdict != delimUnbalanced {
		t.Fatalf("verdict = %v, want unbalanced", got.Verdict)
	}
	if !strings.Contains(got.Detail, "line 2") {
		t.Errorf("Detail = %q, want it to name line 2, where the unclosed brace was opened", got.Detail)
	}
	if !strings.Contains(got.Detail, "{") {
		t.Errorf("Detail = %q, want it to name the delimiter", got.Detail)
	}
}

// TestDelimiterTierAppliesTheDeltaRule mirrors Tier A's asymmetry: an edit that
// leaves an already-broken file broken says so, instead of blaming the edit.
func TestDelimiterTierAppliesTheDeltaRule(t *testing.T) {
	broken := "function f() {\n  return 1;\n"
	worse := "function f() {\n  if (x) {\n    return 1;\n"

	note := checkDelimiterTier(LangTypeScript, "a.ts", &broken, worse)
	if !strings.Contains(note, "already") {
		t.Errorf("note = %q, want it to say the file was already unbalanced before this edit", note)
	}

	fine := "function f() { return 1; }\n"
	note = checkDelimiterTier(LangTypeScript, "a.ts", &fine, worse)
	if strings.Contains(note, "already") {
		t.Errorf("note = %q, want it to blame THIS edit: the prior file was balanced", note)
	}
}

// TestDelimiterTierAlwaysSaysItIsAdvisory is the honesty check on the note
// itself. A user reading "delimiters unbalanced" with no qualifier will read it
// as a syntax error, which is a claim this tier cannot support.
func TestDelimiterTierAlwaysSaysItIsAdvisory(t *testing.T) {
	for _, src := range []string{
		"function f() { return 1; }\n",
		"function f() {\n",
		"/* unterminated\n",
	} {
		note := checkDelimiterTier(LangTypeScript, "a.ts", nil, src)
		if !strings.Contains(strings.ToLower(note), "advisory") {
			t.Errorf("checkDelimiterTier(%q) = %q, want every note to mark itself advisory", src, note)
		}
	}
}
