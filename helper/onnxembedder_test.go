package main

import (
	"math"
	"reflect"
	"testing"
)

func TestTruncateKeepingFinalToken(t *testing.T) {
	tests := []struct {
		name   string
		ids    []int
		maxLen int
		want   []int
	}{
		{
			name:   "shorter than max",
			ids:    []int{1, 2, 3},
			maxLen: 5,
			want:   []int{1, 2, 3},
		},
		{
			name:   "exact max",
			ids:    []int{1, 2, 3, 4, 5},
			maxLen: 5,
			want:   []int{1, 2, 3, 4, 5},
		},
		{
			name:   "longer than max preserves final token",
			ids:    []int{1, 2, 3, 4, 5, 6, 7, 8, 99},
			maxLen: 4,
			want:   []int{1, 2, 3, 99},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateKeepingFinalToken(tt.ids, tt.maxLen)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("truncateKeepingFinalToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestL2Normalize(t *testing.T) {
	// Degenerate zero vector
	zeros := []float32{0, 0, 0}
	if got := l2Normalize(zeros); !reflect.DeepEqual(got, zeros) {
		t.Errorf("l2Normalize(zeros) = %v, want %v", got, zeros)
	}

	// Non-zero vector L2 norm should be ~1.0
	vec := []float32{3.0, 4.0}
	normed := l2Normalize(vec)
	if len(normed) != 2 {
		t.Fatalf("expected len 2, got %d", len(normed))
	}
	length := math.Sqrt(float64(normed[0]*normed[0] + normed[1]*normed[1]))
	if math.Abs(length-1.0) > 1e-5 {
		t.Errorf("L2 norm of normalized vector = %f, want 1.0", length)
	}
	if math.Abs(float64(normed[0])-0.6) > 1e-5 || math.Abs(float64(normed[1])-0.8) > 1e-5 {
		t.Errorf("normalized values = %v, want [0.6, 0.8]", normed)
	}
}
