package pricing

import (
	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
	"example.com/shop/money"
	"example.com/shop/tax"
)

// Breakdown is every figure on an order.
type Breakdown struct {
	Subtotal money.Cents
	Discount money.Cents
	Tax      money.Cents
	Total    money.Cents
}

// Price works out an order: the discount comes off the subtotal, and tax is due
// on what is left.
func Price(c cart.Cart, cat *catalog.Catalog, reg *coupon.Registry, code, region string) (Breakdown, error) {
	sub, err := Subtotal(c, cat)
	if err != nil {
		return Breakdown{}, err
	}
	off := DiscountForCode(sub, reg, code)
	due, err := tax.On(sub-off, region)
	if err != nil {
		return Breakdown{}, err
	}
	return Breakdown{Subtotal: sub, Discount: off, Tax: due, Total: sub - off + due}, nil
}
