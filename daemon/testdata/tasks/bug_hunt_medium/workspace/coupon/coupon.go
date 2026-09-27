// Package coupon holds the discount codes customers can use.
package coupon

import (
	"fmt"
	"strings"
)

// Kind says how a coupon discounts.
type Kind int

const (
	// PercentOff takes Value percent off the subtotal.
	PercentOff Kind = iota
	// AmountOff takes Value cents off the subtotal.
	AmountOff
)

// Coupon is one discount code.
type Coupon struct {
	Code  string
	Kind  Kind
	Value int64
}

// Registry finds coupons by code.
type Registry struct {
	byCode map[string]Coupon
}

// NewRegistry builds a registry. Codes are stored in lower case, so a code is
// the same code however it is typed.
func NewRegistry(coupons ...Coupon) (*Registry, error) {
	r := &Registry{byCode: map[string]Coupon{}}
	for _, c := range coupons {
		key := strings.ToLower(strings.TrimSpace(c.Code))
		if key == "" {
			return nil, fmt.Errorf("coupon: empty code")
		}
		if _, dup := r.byCode[key]; dup {
			return nil, fmt.Errorf("coupon: duplicate code %q", c.Code)
		}
		r.byCode[key] = c
	}
	return r, nil
}

// Find returns the coupon for code.
func (r *Registry) Find(code string) (Coupon, bool) {
	c, ok := r.byCode[code]
	return c, ok
}
