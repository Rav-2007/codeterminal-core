// Package invoice builds the invoice a customer receives.
package invoice

import (
	"time"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
	"example.com/shop/money"
	"example.com/shop/pricing"
)

// Line is one line of an invoice.
type Line struct {
	Name   string
	Qty    int
	Amount money.Cents
}

// Invoice is what the customer is charged, and why.
type Invoice struct {
	Number string
	Issued time.Time
	Region string
	Coupon string
	Lines  []Line
	pricing.Breakdown
}

// Build prices c and writes its invoice.
func Build(number string, issued time.Time, c cart.Cart, cat *catalog.Catalog, reg *coupon.Registry, code, region string) (Invoice, error) {
	b, err := pricing.Price(c, cat, reg, code, region)
	if err != nil {
		return Invoice{}, err
	}
	inv := Invoice{Number: number, Issued: issued, Region: region, Coupon: code, Breakdown: b}
	for _, l := range c.Lines {
		p, _ := cat.Lookup(l.SKU)
		inv.Lines = append(inv.Lines, Line{Name: p.Name, Qty: l.Qty, Amount: p.Price * money.Cents(l.Qty)})
	}
	return inv, nil
}
