package stats

import "testing"

func TestHiddenAverageEmpty(t *testing.T) {
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %v, want 0", got)
	}
	if got := Average([]float64{}); got != 0 {
		t.Errorf("Average(empty) = %v, want 0", got)
	}
	if got := Average([]float64{2, 4}); got != 3 {
		t.Errorf("Average(2,4) = %v, want 3", got)
	}
}
