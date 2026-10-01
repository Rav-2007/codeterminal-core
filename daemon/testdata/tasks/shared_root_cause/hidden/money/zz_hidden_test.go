package money

import "testing"

func TestHiddenRoundsHalfUp(t *testing.T) {
	for in, want := range map[int64]int64{0: 0, 49: 0, 50: 1, 149: 1, 150: 2, 14999: 150, 15050: 151} {
		if got := RoundCents(in); got != want {
			t.Errorf("RoundCents(%d) = %d, want %d", in, got, want)
		}
	}
}
