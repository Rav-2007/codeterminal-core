package discount

import "testing"

func TestPercentRounds(t *testing.T) {
	if got := Percent(1999, 15); got != 300 {
		t.Errorf("Percent(1999, 15) = %d, want 300 (299.85 rounds up)", got)
	}
}
