package report

import (
	"strings"
	"testing"

	"example.com/billing/invoice"
	"example.com/billing/money"
)

func TestHiddenRenderPrintsCurrencies(t *testing.T) {
	got, err := Render([]invoice.Line{{"tea", money.Money{Cents: 350, Currency: "JPY"}, 2}})
	if err != nil || !strings.Contains(got, "tea JPY 3.50") || !strings.Contains(got, "total JPY 7.00") {
		t.Errorf("Render = %q, %v", got, err)
	}
	if _, err := Render([]invoice.Line{{"a", money.Money{1, "JPY"}, 1}, {"b", money.Money{1, "EUR"}, 1}}); err == nil {
		t.Error("Render of mixed currencies returned no error")
	}
}
