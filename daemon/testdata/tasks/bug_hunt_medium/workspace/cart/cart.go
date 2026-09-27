// Package cart holds what a customer is about to order.
package cart

import "fmt"

// Line is a quantity of one product.
type Line struct {
	SKU string
	Qty int
}

// Cart is an order being put together.
type Cart struct {
	Lines []Line
}

// Add puts qty more of sku in the cart.
func (c *Cart) Add(sku string, qty int) error {
	if qty <= 0 {
		return fmt.Errorf("cart: quantity must be positive, got %d", qty)
	}
	for i := range c.Lines {
		if c.Lines[i].SKU == sku {
			c.Lines[i].Qty += qty
			return nil
		}
	}
	c.Lines = append(c.Lines, Line{SKU: sku, Qty: qty})
	return nil
}

// Remove takes sku out of the cart entirely.
func (c *Cart) Remove(sku string) {
	out := c.Lines[:0]
	for _, l := range c.Lines {
		if l.SKU != sku {
			out = append(out, l)
		}
	}
	c.Lines = out
}
