package shop

import "testing"

func TestTotal(t *testing.T) {
	items := []Item{{"pen", 2, 3}, {"book", 10, 1}}
	if got := Total(items); got != 16 {
		t.Errorf("Total = %d, want 16", got)
	}
}
