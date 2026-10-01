package split

import "example.com/shop/money"

// Share is one of n equal shares of cents, to the nearest cent.
func Share(cents, n int64) int64 {
	return money.RoundCents(cents * 100 / n)
}
