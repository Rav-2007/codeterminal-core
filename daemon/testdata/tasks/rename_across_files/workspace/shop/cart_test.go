package shop

import "testing"

func TestCalcTotal(t *testing.T) {
	items := []Item{{"pen", 2, 3}, {"book", 10, 1}}
	if got := CalcTotal(items); got != 16 {
		t.Errorf("CalcTotal = %d, want 16", got)
	}
}
