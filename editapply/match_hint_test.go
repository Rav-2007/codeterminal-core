package editapply

import (
	"strings"
	"testing"
)

func numbered(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// AN AMBIGUOUS SEARCH SAYS WHERE ITS MATCHES ARE, by line, so the retry widens
// the passage that was meant -- and stays a refusal, worded as before.
//
// Neuter check: drop matchLines from the refusal (no "at lines").
func TestAnAmbiguousMatchNamesItsLines(t *testing.T) {
	src := numbered("package m", "", "x := 1", "y := 2", "", "z := 3", "x := 1", "w := 4")
	_, err := findSearch(src, "x := 1", "m.go")
	if err == nil || !strings.Contains(err.Error(), "found 2 times in m.go (at lines 3 and 7); ambiguous, refusing") {
		t.Errorf("err = %v", err)
	}

	var many []string
	for i := 0; i < 11; i++ {
		many = append(many, "dup()")
	}
	_, err = findSearch(numbered(many...), "dup()", "m.go")
	if err == nil || !strings.Contains(err.Error(), "(at lines 1, 2, 3, 4, 5, 6, 7, 8 and 3 more)") {
		t.Errorf("err = %v", err)
	}

	// Found only once line endings are ignored: the lines are the ORIGINAL's.
	crlf := strings.ReplaceAll(numbered("a", "b", "c", "b"), "\n", "\r\n")
	_, err = findSearch(crlf, "b\n", "m.go")
	if err == nil || !strings.Contains(err.Error(), "(at lines 2 and 4)") || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v", err)
	}
}

// A SEARCH THAT MATCHES NOWHERE SAYS WHERE IT MOST LIKELY MEANT: the lines that
// agree with most of it, by number -- never the file's text, which the model
// reads with a tool if it needs it. Nothing close means the plain refusal.
//
// Neuter checks: drop closestPassage from the refusal; drop the half-agree
// floor (the unrelated SEARCH is given a "closest" passage).
func TestAMissedSearchNamesTheClosestPassage(t *testing.T) {
	src := numbered(
		"package m",
		"",
		"func Area(w, h int) int {",
		"\tif w < 0 {",
		"\t\treturn 0 // SECRET-LOOKING-COMMENT",
		"\t}",
		"\treturn w * h",
		"}",
	)
	// The model remembered the guard as "w <= 0" and the rest right.
	stale := "\tif w <= 0 {\n\t\treturn 0 // SECRET-LOOKING-COMMENT\n\t}\n"
	_, err := findSearch(src, stale, "m.go")
	if err == nil {
		t.Fatal("a stale SEARCH matched")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "search text not found in m.go; the closest passage is lines 4-6, where 2 of its 3 lines match") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(msg, "w < 0") || strings.Contains(msg, "w * h") {
		t.Errorf("the refusal quotes the file: %v", err)
	}

	_, err = findSearch(src, "fmt.Println(\"nothing like this\")\nos.Exit(3)\n", "m.go")
	if err == nil || err.Error() != "search text not found in m.go" {
		t.Errorf("an unrelated SEARCH got %v, want the plain refusal", err)
	}
	// One line in four agreeing -- a lone brace -- is not where it meant.
	_, err = findSearch(src, "}\nfoo()\nbar()\nbaz()\n", "m.go")
	if err == nil || err.Error() != "search text not found in m.go" {
		t.Errorf("a SEARCH sharing only a brace got %v, want the plain refusal", err)
	}
}
