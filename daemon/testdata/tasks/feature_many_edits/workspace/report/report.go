package report

import (
	"strings"

	"example.com/billing/invoice"
	"example.com/billing/money"
)

// Render writes an invoice as text: one line per item, then the total.
func Render(lines []invoice.Line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Desc + " " + money.Format(l.Price) + "\n")
	}
	b.WriteString("total " + money.Format(invoice.Total(lines)) + "\n")
	return b.String()
}
