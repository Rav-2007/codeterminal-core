package ledger

import "testing"

func TestTotal(t *testing.T) {
	got, err := Total([]Entry{{"rent", "1,200.00"}, {"tea", "3.50"}})
	if err != nil || got != 120350 {
		t.Errorf("Total = %d, %v; want 120350", got, err)
	}
}
