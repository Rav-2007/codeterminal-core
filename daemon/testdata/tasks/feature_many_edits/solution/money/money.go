package money

import (
	"errors"
	"fmt"
)

// Money is an amount in cents, in a currency (an ISO code such as "EUR").
type Money struct {
	Cents    int64
	Currency string
}

// ErrMixedCurrency is returned when amounts in different currencies are added.
var ErrMixedCurrency = errors.New("amounts are in different currencies")

// Format writes m as "EUR 10.50".
func Format(m Money) string {
	return fmt.Sprintf("%s %d.%02d", m.Currency, m.Cents/100, m.Cents%100)
}

// Sum adds amounts in one currency.
func Sum(ms ...Money) (Money, error) {
	var total Money
	for i, m := range ms {
		if i == 0 {
			total.Currency = m.Currency
		} else if m.Currency != total.Currency {
			return Money{}, ErrMixedCurrency
		}
		total.Cents += m.Cents
	}
	return total, nil
}
