package money

import (
	"errors"
	"testing"
)

func TestHiddenCurrency(t *testing.T) {
	if got := Format(Money{Cents: 1050, Currency: "EUR"}); got != "EUR 10.50" {
		t.Errorf("Format = %q, want EUR 10.50", got)
	}
	if _, err := Sum(Money{100, "EUR"}, Money{100, "USD"}); !errors.Is(err, ErrMixedCurrency) {
		t.Errorf("Sum of EUR and USD gave %v, want ErrMixedCurrency", err)
	}
	if got, err := Sum(); err != nil || got != (Money{}) {
		t.Errorf("Sum() = %+v, %v, want the zero Money", got, err)
	}
}
