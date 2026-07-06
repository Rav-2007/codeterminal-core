// Package mathutil provides basic descriptive statistics over slices of
// float64 samples.
package mathutil

import (
	"math"
	"sort"
)

// Mean returns the arithmetic mean of samples. It returns 0 for an empty
// slice.
func Mean(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += s
	}
	return sum / float64(len(samples))
}

// StdDev returns the population standard deviation of samples.
func StdDev(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	mean := Mean(samples)
	var sumSq float64
	for _, s := range samples {
		diff := s - mean
		sumSq += diff * diff
	}
	return math.Sqrt(sumSq / float64(len(samples)))
}

// Median returns the middle value of samples (or the average of the two
// middle values for an even-length slice). samples is not mutated; Median
// sorts a copy internally.
func Median(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}

	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// Percentile returns the value at the given percentile (0-100) of samples,
// using linear interpolation between the two nearest ranks.
func Percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}

	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
