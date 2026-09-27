package invoice

import (
	"testing"
	"time"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
)

func TestHiddenCouponAsTyped(t *testing.T) {
	var c cart.Cart
	_ = c.Add("NB-01", 3)
	for _, code := range []string{"SAVE10", "save10", "Save10", " SAVE10 ", "save10\t"} {
		inv, err := Build("H-1", time.Now(), c, catalog.Default(), coupon.Default(), code, "EU")
		if err != nil {
			t.Fatal(err)
		}
		if inv.Discount != 300 || inv.Total != 3267 {
			t.Errorf("coupon %q: discount %v total %v, want $3.00 and $32.67", code, inv.Discount, inv.Total)
		}
	}
	inv, err := Build("H-2", time.Now(), c, catalog.Default(), coupon.Default(), "FiveOff", "US")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Discount != 500 {
		t.Errorf("coupon FiveOff: discount %v, want $5.00", inv.Discount)
	}
	inv, err = Build("H-3", time.Now(), c, catalog.Default(), coupon.Default(), "NOPE", "EU")
	if err != nil || inv.Discount != 0 || inv.Total != 3630 {
		t.Errorf("unknown coupon: %+v, %v; want no discount", inv.Breakdown, err)
	}
}

func TestHiddenRegistryFindAsTyped(t *testing.T) {
	r := coupon.Default()
	for _, code := range []string{"SAVE10", "save10", " Save10 "} {
		if _, ok := r.Find(code); !ok {
			t.Errorf("Find(%q) found nothing", code)
		}
	}
}
