package pricing

// freeShippingThreshold is the order total, in cents, from which shipping is free.
const freeShippingThreshold = 10000

// flatShipping is what shipping costs below that.
const flatShipping = 499

// FreeShipping reports whether an order total, in cents, ships free.
func FreeShipping(total int64) bool {
	return total >= freeShippingThreshold
}

// Shipping is what an order of total cents pays for shipping.
func Shipping(total int64) int64 {
	if FreeShipping(total) {
		return 0
	}
	return flatShipping
}
