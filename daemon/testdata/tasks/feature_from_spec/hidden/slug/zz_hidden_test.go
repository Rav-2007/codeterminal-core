package slug

import "testing"

func TestHiddenSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World":    "hello-world",
		"  Go_is FUN!! ": "go-is-fun",
		"C++ & Rust":     "c-rust",
		"---":            "",
		"a__b":           "a-b",
		"Already-slug":   "already-slug",
		"Version 2.0":    "version-20",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
