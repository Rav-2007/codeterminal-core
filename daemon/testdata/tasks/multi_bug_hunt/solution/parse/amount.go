package parse

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseAmount reads a money amount such as "1,234.50" into cents.
func ParseAmount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	whole, frac, found := strings.Cut(s, ".")
	if !found {
		frac = "00"
	}
	if len(frac) != 2 {
		return 0, fmt.Errorf("amount %q: want two decimal places", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q: %v", s, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q: %v", s, err)
	}
	return w*100 + f, nil
}
