package pricing

import (
	"example.com/shop/coupon"
	"example.com/shop/money"
)

// Discount is how much c takes off subtotal. It never takes off more than the
// subtotal.
func Discount(subtotal money.Cents, c coupon.Coupon) money.Cents {
	var off money.Cents
	switch c.Kind {
	case coupon.PercentOff:
		off = money.Percent(subtotal, c.Value)
	case coupon.AmountOff:
		off = money.Cents(c.Value)
	}
	if off > subtotal {
		off = subtotal
	}
	return off
}

// DiscountForCode looks code up in reg and returns its discount on subtotal.
// An empty or unknown code is no discount, not an error: checkout accepts
// whatever the customer typed.
func DiscountForCode(subtotal money.Cents, reg *coupon.Registry, code string) money.Cents {
	if code == "" {
		return 0
	}
	c, ok := reg.Find(code)
	if !ok {
		return 0
	}
	return Discount(subtotal, c)
}
