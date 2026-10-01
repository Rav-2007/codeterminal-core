package report

import (
	"strings"
	"testing"

	"example.com/billing/invoice"
	"example.com/billing/money"
)

func TestRender(t *testing.T) {
	got, err := Render([]invoice.Line{{"tea", money.Money{Cents: 350, Currency: "USD"}, 2}})
	if err != nil || !strings.Contains(got, "tea USD 3.50") || !strings.Contains(got, "total USD 7.00") {
		t.Errorf("Render = %q, %v", got, err)
	}
}
