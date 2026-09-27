package strutil

import "testing"

func TestHiddenIsPalindrome(t *testing.T) {
	cases := map[string]bool{
		"Never odd or even":           true,
		"abc":                         false,
		"":                            true,
		"A man a plan a canal Panama": true,
		"Go gopher":                   false,
	}
	for in, want := range cases {
		if got := IsPalindrome(in); got != want {
			t.Errorf("IsPalindrome(%q) = %v, want %v", in, got, want)
		}
	}
}
