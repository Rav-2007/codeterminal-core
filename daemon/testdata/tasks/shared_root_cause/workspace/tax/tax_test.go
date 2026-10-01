package tax

import "testing"

func TestVATRoundsToTheNearestCent(t *testing.T) {
	if got := VAT(1999, 750); got != 150 {
		t.Errorf("VAT(1999, 750) = %d, want 150 (149.925 rounds up)", got)
	}
}
