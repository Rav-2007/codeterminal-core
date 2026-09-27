// Package report summarises a day of invoices.
package report

import (
	"sort"

	"example.com/shop/invoice"
	"example.com/shop/money"
)

// Daily is one day's figures.
type Daily struct {
	Invoices  int
	Revenue   money.Cents
	Discounts money.Cents
	TaxByArea map[string]money.Cents
}

// Summarise adds up invs.
func Summarise(invs []invoice.Invoice) Daily {
	d := Daily{TaxByArea: map[string]money.Cents{}}
	for _, inv := range invs {
		d.Invoices++
		d.Revenue += inv.Total
		d.Discounts += inv.Discount
		d.TaxByArea[inv.Region] += inv.Tax
	}
	return d
}

// Regions lists the regions in d, sorted.
func (d Daily) Regions() []string {
	out := make([]string, 0, len(d.TaxByArea))
	for r := range d.TaxByArea {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
