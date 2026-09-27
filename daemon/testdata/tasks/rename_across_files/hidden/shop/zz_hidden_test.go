package shop

import "testing"

func TestHiddenTotal(t *testing.T) {
	if got := Total([]Item{{"x", 3, 3}}); got != 9 {
		t.Errorf("Total = %d, want 9", got)
	}
}
