// Package money does arithmetic on amounts held as whole cents.
package money

import "fmt"

// Cents is an amount of money in cents.
type Cents int64

// String renders c as dollars, e.g. "$12.30".
func (c Cents) String() string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// Percent returns pct percent of c, rounded half away from zero.
func Percent(c Cents, pct int64) Cents {
	n := int64(c) * pct
	if n >= 0 {
		return Cents((n + 50) / 100)
	}
	return Cents((n - 50) / 100)
}

// BasisPoints returns bp hundredths of a percent of c, rounded half away from zero.
func BasisPoints(c Cents, bp int64) Cents {
	n := int64(c) * bp
	if n >= 0 {
		return Cents((n + 5000) / 10000)
	}
	return Cents((n - 5000) / 10000)
}
