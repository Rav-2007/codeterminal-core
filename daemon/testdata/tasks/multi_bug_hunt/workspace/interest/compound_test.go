package interest

import "testing"

func TestCompoundWithNoYearsIsThePrincipal(t *testing.T) {
	if got := Compound(100000, 0.05, 0); got != 100000 {
		t.Errorf("Compound(100000, 0.05, 0) = %d, want 100000", got)
	}
}
