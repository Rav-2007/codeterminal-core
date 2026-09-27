// Package pricing turns a cart into money: subtotal, discount, tax and total.
package pricing

import (
	"fmt"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/money"
)

// Subtotal is the sum of every line at catalog price.
func Subtotal(c cart.Cart, cat *catalog.Catalog) (money.Cents, error) {
	var sum money.Cents
	for _, l := range c.Lines {
		p, ok := cat.Lookup(l.SKU)
		if !ok {
			return 0, fmt.Errorf("pricing: unknown SKU %q", l.SKU)
		}
		sum += p.Price * money.Cents(l.Qty)
	}
	return sum, nil
}
