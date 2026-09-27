package units

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var multipliers = []struct {
	suffix string
	mult   float64
}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}

// ParseSize parses a human-readable size such as "10KB" or "1.5 MB" into a
// number of bytes. Units are B, KB, MB, GB and TB, powers of 1024, in any
// letter case.
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, errors.New("empty size")
	}
	mult := 1.0
	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			mult = m.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, m.suffix))
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid size")
	}
	v := n * mult
	if v != math.Trunc(v) {
		return 0, errors.New("size is not a whole number of bytes")
	}
	return int64(v), nil
}
