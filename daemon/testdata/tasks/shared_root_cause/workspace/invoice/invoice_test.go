package invoice

import "testing"

func TestTotal(t *testing.T) {
	// 1999 less 15% (299.85 -> 300) is 1699; VAT at 7.5% on 1699 is 127.425 -> 127.
	if got := Total(1999, 15, 750); got != 1826 {
		t.Errorf("Total(1999, 15, 750) = %d, want 1826", got)
	}
}
