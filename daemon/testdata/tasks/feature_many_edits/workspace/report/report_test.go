package report

import (
	"strings"
	"testing"

	"example.com/billing/invoice"
	"example.com/billing/money"
)

func TestRender(t *testing.T) {
	got := Render([]invoice.Line{{"tea", money.Money{Cents: 350}, 2}})
	if !strings.Contains(got, "tea 3.50") || !strings.Contains(got, "total 7.00") {
		t.Errorf("Render = %q", got)
	}
}
