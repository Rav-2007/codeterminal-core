package invoice

import "example.com/billing/money"

// Line is one invoice line.
type Line struct {
	Desc  string
	Price money.Money
	Qty   int64
}

// Total is the invoice's total.
func Total(lines []Line) money.Money {
	amounts := make([]money.Money, 0, len(lines))
	for _, l := range lines {
		amounts = append(amounts, money.Money{Cents: l.Price.Cents * l.Qty})
	}
	return money.Sum(amounts...)
}
