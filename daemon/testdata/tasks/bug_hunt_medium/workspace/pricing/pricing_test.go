package pricing

import (
	"testing"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
)

func TestPriceWithoutCoupon(t *testing.T) {
	var c cart.Cart
	_ = c.Add("NB-01", 3)
	b, err := Price(c, catalog.Default(), coupon.Default(), "", "EU")
	if err != nil {
		t.Fatal(err)
	}
	if b.Total != 3630 {
		t.Fatalf("total = %v, want $36.30", b.Total)
	}
}

func TestPriceWithLowerCaseCoupon(t *testing.T) {
	var c cart.Cart
	_ = c.Add("NB-01", 3)
	b, err := Price(c, catalog.Default(), coupon.Default(), "save10", "EU")
	if err != nil {
		t.Fatal(err)
	}
	if b.Discount != 300 || b.Total != 3267 {
		t.Fatalf("breakdown = %+v, want $3.00 off and $32.67", b)
	}
}

func TestAmountOffNeverGoesNegative(t *testing.T) {
	var c cart.Cart
	_ = c.Add("PN-02", 1)
	b, err := Price(c, catalog.Default(), coupon.Default(), "fiveoff", "US")
	if err != nil {
		t.Fatal(err)
	}
	if b.Total != 0 {
		t.Fatalf("total = %v, want $0.00", b.Total)
	}
}
