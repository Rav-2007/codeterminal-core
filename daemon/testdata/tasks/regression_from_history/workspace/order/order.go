package order

import "example.com/shipping/pricing"

// Order is a basket's subtotal, in cents.
type Order struct {
	Subtotal int64
}

// Due is what the customer pays: the subtotal and its shipping.
func (o Order) Due() int64 {
	return o.Subtotal + pricing.Shipping(o.Subtotal)
}
