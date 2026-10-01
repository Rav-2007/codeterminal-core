package split

import "testing"

func TestShareRounds(t *testing.T) {
	if got := Share(1000, 3); got != 333 {
		t.Errorf("Share(1000, 3) = %d, want 333", got)
	}
	if got := Share(1001, 2); got != 501 {
		t.Errorf("Share(1001, 2) = %d, want 501 (500.5 rounds up)", got)
	}
}
