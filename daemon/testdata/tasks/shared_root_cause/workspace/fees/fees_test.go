package fees

import "testing"

func TestCardFee(t *testing.T) {
	if got := CardFee(1099); got != 32 {
		t.Errorf("CardFee(1099) = %d, want 32 (31.871 rounds up)", got)
	}
}
