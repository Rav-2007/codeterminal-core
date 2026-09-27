package semver

import "testing"

func TestCompareChain(t *testing.T) {
	chain := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0"}
	for i := 0; i+1 < len(chain); i++ {
		if got, err := Compare(chain[i], chain[i+1]); err != nil || got != -1 {
			t.Errorf("Compare(%q, %q) = %d, %v; want -1", chain[i], chain[i+1], got, err)
		}
	}
}

func TestCompareIgnoresBuildAndV(t *testing.T) {
	if got, err := Compare("v1.0.0", "1.0.0+build.7"); err != nil || got != 0 {
		t.Errorf("got %d, %v; want 0", got, err)
	}
}

func TestCompareInvalid(t *testing.T) {
	for _, bad := range []string{"", "1.0", "01.0.0", "1.0.0-", "1.0.0-01", "1.0.0-alpha..1"} {
		if _, err := Compare(bad, "1.0.0"); err == nil {
			t.Errorf("Compare(%q) gave no error", bad)
		}
	}
}

func TestCompareBigNumbers(t *testing.T) {
	if got, err := Compare("99999999999999999999.0.0", "9.0.0"); err != nil || got != 1 {
		t.Errorf("got %d, %v; want 1", got, err)
	}
}
