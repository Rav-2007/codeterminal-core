package fees

import "example.com/shop/money"

// CardFee is the 2.9% card fee on cents, to the nearest cent.
func CardFee(cents int64) int64 {
	return money.RoundCents(cents * 290 / 100)
}
