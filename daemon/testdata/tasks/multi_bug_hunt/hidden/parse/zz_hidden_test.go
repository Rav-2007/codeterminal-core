package parse

import "testing"

func TestHiddenManySeparators(t *testing.T) {
	got, err := ParseAmount("1,234,567.89")
	if err != nil || got != 123456789 {
		t.Errorf("ParseAmount(1,234,567.89) = %d, %v; want 123456789", got, err)
	}
}
