package tax

import "example.com/shop/money"

// VAT is the tax on cents at a rate in basis points (750 is 7.5%).
func VAT(cents, basisPoints int64) int64 {
	return money.RoundCents(cents * basisPoints / 100)
}
