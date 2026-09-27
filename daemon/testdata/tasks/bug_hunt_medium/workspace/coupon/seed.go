package coupon

// Default is the registry of coupons currently on offer.
func Default() *Registry {
	r, err := NewRegistry(
		Coupon{Code: "SAVE10", Kind: PercentOff, Value: 10},
		Coupon{Code: "FIVEOFF", Kind: AmountOff, Value: 500},
	)
	if err != nil {
		panic(err)
	}
	return r
}
