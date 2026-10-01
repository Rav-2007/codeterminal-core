package parse

import "testing"

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]int64{"0.99": 99, "12": 1200, "1,234.50": 123450} {
		got, err := ParseAmount(in)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
