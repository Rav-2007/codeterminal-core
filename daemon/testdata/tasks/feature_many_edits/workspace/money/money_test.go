package money

import "testing"

func TestFormat(t *testing.T) {
	if got := Format(Money{Cents: 1050}); got != "10.50" {
		t.Errorf("Format = %q", got)
	}
}

func TestSum(t *testing.T) {
	if got := Sum(Money{Cents: 100}, Money{Cents: 250}); got.Cents != 350 {
		t.Errorf("Sum = %+v", got)
	}
}
