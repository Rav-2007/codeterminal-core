package semver

import "testing"

func TestHiddenOrderedChain(t *testing.T) {
	chain := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		"1.0.1", "1.2.0", "1.10.0", "2.0.0-0", "2.0.0-1", "2.0.0-a", "2.0.0",
	}
	for i := range chain {
		for j := range chain {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			got, err := Compare(chain[i], chain[j])
			if err != nil || got != want {
				t.Errorf("Compare(%q, %q) = %d, %v; want %d", chain[i], chain[j], got, err, want)
			}
		}
	}
}

func TestHiddenEqualForms(t *testing.T) {
	for _, pair := range [][2]string{
		{"v1.0.0", "1.0.0"}, {"1.0.0+build.7", "1.0.0"}, {"v1.0.0+a", "1.0.0+b-c.9"},
		{"1.0.0-rc.1+x", "v1.0.0-rc.1"}, {"0.0.0", "v0.0.0"},
	} {
		if got, err := Compare(pair[0], pair[1]); err != nil || got != 0 {
			t.Errorf("Compare(%q, %q) = %d, %v; want 0", pair[0], pair[1], got, err)
		}
	}
}

func TestHiddenBigNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"99999999999999999999.0.0", "9.0.0", 1},
		{"1.0.0", "1.0.99999999999999999999999", -1},
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000", -1},
	}
	for _, c := range cases {
		if got, err := Compare(c.a, c.b); err != nil || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
}

func TestHiddenInvalid(t *testing.T) {
	for _, bad := range []string{
		"", "1.0", "1.0.0.0", "01.0.0", "1.01.0", "1.0.00", "1.0.0-", "1.0.0-alpha..1",
		"1.0.0-01", "1.0.0+", "1.0.0+a..b", "a.b.c", "1.0.0-al$pha", "vv1.0.0", "1.0.0 ", "-1.0.0",
	} {
		if _, err := Compare(bad, "1.0.0"); err == nil {
			t.Errorf("Compare(%q, \"1.0.0\") gave no error", bad)
		}
		if _, err := Compare("1.0.0", bad); err == nil {
			t.Errorf("Compare(\"1.0.0\", %q) gave no error", bad)
		}
	}
}
