package interest

import "testing"

func TestHiddenTwoYears(t *testing.T) {
	if got := Compound(100000, 0.10, 2); got != 121000 {
		t.Errorf("Compound(100000, 0.10, 2) = %d, want 121000", got)
	}
	if got := Compound(100000, 0.10, 1); got != 110000 {
		t.Errorf("Compound(100000, 0.10, 1) = %d, want 110000", got)
	}
}
