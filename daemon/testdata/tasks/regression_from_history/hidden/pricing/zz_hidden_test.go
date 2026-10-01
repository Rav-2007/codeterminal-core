package pricing

import "testing"

func TestHiddenExactlyAHundredShipsFree(t *testing.T) {
	if !FreeShipping(10000) || Shipping(10000) != 0 {
		t.Errorf("an order of exactly $100.00: FreeShipping = %v, Shipping = %d; want free", FreeShipping(10000), Shipping(10000))
	}
	if FreeShipping(9999) {
		t.Error("an order of $99.99 must not ship free")
	}
}
