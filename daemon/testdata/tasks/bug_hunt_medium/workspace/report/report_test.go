package report

import (
	"testing"

	"example.com/shop/invoice"
	"example.com/shop/pricing"
)

func TestSummarise(t *testing.T) {
	d := Summarise([]invoice.Invoice{
		{Region: "EU", Breakdown: pricing.Breakdown{Total: 1210, Tax: 210}},
		{Region: "UK", Breakdown: pricing.Breakdown{Total: 600, Tax: 100, Discount: 50}},
	})
	if d.Invoices != 2 || d.Revenue != 1810 || d.Discounts != 50 || d.TaxByArea["EU"] != 210 {
		t.Fatalf("summary = %+v", d)
	}
	if r := d.Regions(); len(r) != 2 || r[0] != "EU" {
		t.Fatalf("regions = %v", r)
	}
}
