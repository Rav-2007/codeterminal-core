package units

import "testing"

func TestParseSize(t *testing.T) {
	good := map[string]int64{
		"512":   512,
		"10KB":  10240,
		"10kb":  10240,
		"1.5MB": 1572864,
		"2 GB":  2147483648,
		" 7B ":  7,
		"3TB":   3298534883328,
	}
	for in, want := range good {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-5KB", "1.5B", "10XB"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got)
		}
	}
}
