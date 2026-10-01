package discount

import "example.com/shop/money"

// Percent is pct percent of cents, rounded to the nearest cent.
func Percent(cents, pct int64) int64 {
	return money.RoundCents(cents * pct)
}
