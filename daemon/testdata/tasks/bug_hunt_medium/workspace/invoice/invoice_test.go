package invoice

import (
	"strings"
	"testing"
	"time"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
)

func TestRenderShowsTheTotal(t *testing.T) {
	var c cart.Cart
	_ = c.Add("BG-03", 1)
	inv, err := Build("A-1", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), c, catalog.Default(), coupon.Default(), "", "US")
	if err != nil {
		t.Fatal(err)
	}
	out := Render(inv)
	if !strings.Contains(out, "Total") || !strings.Contains(out, "$25.99") {
		t.Fatalf("render:\n%s", out)
	}
}
