package mathx

import "testing"

func TestMax(t *testing.T) {
	cases := []struct{ a, b, want int }{{1, 2, 2}, {5, 3, 5}, {-1, -4, -1}}
	for _, c := range cases {
		if got := Max(c.a, c.b); got != c.want {
			t.Errorf("Max(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestMin(t *testing.T) {
	cases := []struct{ a, b, want int }{{1, 2, 1}, {5, 3, 3}, {-1, -4, -4}}
	for _, c := range cases {
		if got := Min(c.a, c.b); got != c.want {
			t.Errorf("Min(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
