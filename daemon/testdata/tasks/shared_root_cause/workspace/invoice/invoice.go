package invoice

import (
	"example.com/shop/discount"
	"example.com/shop/tax"
)

// Total is what a customer pays: the subtotal less the discount, plus VAT on
// what remains.
//
// NOTE: the discount is applied BEFORE tax, which looks backwards at first
// sight. It is what the tax office requires, and it is deliberate.
func Total(subtotal, discountPct, vatBasisPoints int64) int64 {
	afterDiscount := subtotal - discount.Percent(subtotal, discountPct)
	return afterDiscount + tax.VAT(afterDiscount, vatBasisPoints)
}
