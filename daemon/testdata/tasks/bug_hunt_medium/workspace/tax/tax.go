// Package tax knows the sales-tax rate of each region.
package tax

import (
	"fmt"

	"example.com/shop/money"
)

// rates are in basis points (hundredths of a percent).
var rates = map[string]int64{
	"EU": 2100,
	"UK": 2000,
	"US": 0, // collected by the marketplace, not by us
	"IN": 1800,
}

// Rate returns the tax rate of region in basis points.
func Rate(region string) (int64, error) {
	bp, ok := rates[region]
	if !ok {
		return 0, fmt.Errorf("tax: unknown region %q", region)
	}
	return bp, nil
}

// On returns the tax due on amount in region.
func On(amount money.Cents, region string) (money.Cents, error) {
	bp, err := Rate(region)
	if err != nil {
		return 0, err
	}
	return money.BasisPoints(amount, bp), nil
}
