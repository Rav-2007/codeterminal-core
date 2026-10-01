package invoice

import (
	"errors"
	"testing"

	"example.com/billing/money"
)

func TestHiddenTotalCarriesTheCurrency(t *testing.T) {
	got, err := Total([]Line{{"a", money.Money{Cents: 500, Currency: "GBP"}, 3}})
	if err != nil || got != (money.Money{Cents: 1500, Currency: "GBP"}) {
		t.Errorf("Total = %+v, %v", got, err)
	}
	_, err = Total([]Line{{"a", money.Money{Cents: 1, Currency: "GBP"}, 1}, {"b", money.Money{Cents: 1, Currency: "EUR"}, 1}})
	if !errors.Is(err, money.ErrMixedCurrency) {
		t.Errorf("mixed lines gave %v, want ErrMixedCurrency", err)
	}
}
