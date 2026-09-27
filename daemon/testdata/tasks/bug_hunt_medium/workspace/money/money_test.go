package money

import "testing"

func TestString(t *testing.T) {
	for c, want := range map[Cents]string{0: "$0.00", 5: "$0.05", 1230: "$12.30", -250: "-$2.50"} {
		if got := c.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", c, got, want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := Percent(3000, 10); got != 300 {
		t.Errorf("Percent(3000, 10) = %d", got)
	}
	if got := Percent(1999, 10); got != 200 {
		t.Errorf("Percent(1999, 10) = %d", got)
	}
}

func TestBasisPoints(t *testing.T) {
	if got := BasisPoints(2700, 2100); got != 567 {
		t.Errorf("BasisPoints(2700, 2100) = %d", got)
	}
}
