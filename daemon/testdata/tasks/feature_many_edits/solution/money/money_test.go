package money

import "testing"

func TestFormat(t *testing.T) {
	if got := Format(Money{Cents: 1050, Currency: "EUR"}); got != "EUR 10.50" {
		t.Errorf("Format = %q", got)
	}
}

func TestSum(t *testing.T) {
	got, err := Sum(Money{100, "EUR"}, Money{250, "EUR"})
	if err != nil || got.Cents != 350 || got.Currency != "EUR" {
		t.Errorf("Sum = %+v, %v", got, err)
	}
}
