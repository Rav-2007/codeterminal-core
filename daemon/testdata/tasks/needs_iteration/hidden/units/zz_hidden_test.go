package units

import "testing"

func TestHiddenParseSize(t *testing.T) {
	for in, want := range map[string]int64{"4 kb": 4096, "0.5KB": 512} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
