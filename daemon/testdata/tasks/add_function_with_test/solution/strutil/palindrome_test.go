package strutil

import "testing"

func TestIsPalindrome(t *testing.T) {
	if !IsPalindrome("Never odd or even") {
		t.Error("want true")
	}
}
