package strutil

import "testing"

func TestReverse(t *testing.T) {
	if got := Reverse("abc"); got != "cba" {
		t.Errorf("Reverse(abc) = %q", got)
	}
}
