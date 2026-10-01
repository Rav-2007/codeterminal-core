package invoice

import "example.com/billing/money"

// Line is one invoice line.
type Line struct {
	Desc  string
	Price money.Money
	Qty   int64
}

// Total is the invoice's total, in its lines' currency.
func Total(lines []Line) (money.Money, error) {
	amounts := make([]money.Money, 0, len(lines))
	for _, l := range lines {
		amounts = append(amounts, money.Money{Cents: l.Price.Cents * l.Qty, Currency: l.Price.Currency})
	}
	return money.Sum(amounts...)
}
