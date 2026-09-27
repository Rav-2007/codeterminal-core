package stats

import "testing"

func TestAverage(t *testing.T) {
	if got := Average([]float64{1, 2, 3}); got != 2 {
		t.Errorf("Average = %v, want 2", got)
	}
}

func TestAverageEmpty(t *testing.T) {
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %v, want 0", got)
	}
}
