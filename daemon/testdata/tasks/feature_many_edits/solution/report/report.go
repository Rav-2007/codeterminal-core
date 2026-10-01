package report

import (
	"strings"

	"example.com/billing/invoice"
	"example.com/billing/money"
)

// Render writes an invoice as text: one line per item, then the total.
func Render(lines []invoice.Line) (string, error) {
	total, err := invoice.Total(lines)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Desc + " " + money.Format(l.Price) + "\n")
	}
	b.WriteString("total " + money.Format(total) + "\n")
	return b.String(), nil
}
