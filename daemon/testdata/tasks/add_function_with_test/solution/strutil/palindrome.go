package strutil

import "strings"

// IsPalindrome reports whether s reads the same backwards, ignoring case and spaces.
func IsPalindrome(s string) bool {
	s = strings.ToLower(strings.ReplaceAll(s, " ", ""))
	return s == Reverse(s)
}
