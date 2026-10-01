package invoice

import (
	"testing"

	"example.com/billing/money"
)

func TestTotal(t *testing.T) {
	lines := []Line{{"tea", money.Money{Cents: 350}, 2}, {"cake", money.Money{Cents: 1400}, 1}}
	if got := Total(lines); got.Cents != 2100 {
		t.Errorf("Total = %+v", got)
	}
}
