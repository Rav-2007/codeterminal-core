package money

import "fmt"

// Money is an amount in cents.
type Money struct {
	Cents int64
}

// Format writes m as "10.50".
func Format(m Money) string {
	return fmt.Sprintf("%d.%02d", m.Cents/100, m.Cents%100)
}

// Sum adds amounts.
func Sum(ms ...Money) Money {
	var total Money
	for _, m := range ms {
		total.Cents += m.Cents
	}
	return total
}
