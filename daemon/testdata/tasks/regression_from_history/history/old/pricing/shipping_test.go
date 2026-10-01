package pricing

import "testing"

func TestShipping(t *testing.T) {
	if !FreeShipping(20000) {
		t.Error("an order of $200 should ship free")
	}
	if got := Shipping(5000); got != flatShipping {
		t.Errorf("Shipping(5000) = %d, want %d", got, flatShipping)
	}
}
