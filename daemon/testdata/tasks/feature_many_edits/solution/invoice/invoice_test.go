package invoice

import (
	"testing"

	"example.com/billing/money"
)

func TestTotal(t *testing.T) {
	lines := []Line{{"tea", money.Money{Cents: 350, Currency: "USD"}, 2}, {"cake", money.Money{Cents: 1400, Currency: "USD"}, 1}}
	got, err := Total(lines)
	if err != nil || got.Cents != 2100 || got.Currency != "USD" {
		t.Errorf("Total = %+v, %v", got, err)
	}
}
