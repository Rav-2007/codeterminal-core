package pricing

const (
	// FreeShippingFrom is the order total, in cents, from which shipping is free.
	FreeShippingFrom = 10000
	// ShippingFlat is what shipping costs below that.
	ShippingFlat = 499
)

// FreeShipping reports whether an order total, in cents, ships free.
func FreeShipping(total int64) bool {
	return total > FreeShippingFrom
}

// Shipping is what an order of total cents pays for shipping.
func Shipping(total int64) int64 {
	if FreeShipping(total) {
		return 0
	}
	return ShippingFlat
}
