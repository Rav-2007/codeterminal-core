package mathx

import "testing"

func TestHiddenMax(t *testing.T) {
	cases := []struct{ a, b, want int }{{0, 0, 0}, {-3, 7, 7}, {10, 2, 10}}
	for _, c := range cases {
		if got := Max(c.a, c.b); got != c.want {
			t.Errorf("Max(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
