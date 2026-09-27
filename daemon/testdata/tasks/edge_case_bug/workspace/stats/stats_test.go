package stats

import "testing"

func TestAverage(t *testing.T) {
	if got := Average([]float64{1, 2, 3}); got != 2 {
		t.Errorf("Average = %v, want 2", got)
	}
}
