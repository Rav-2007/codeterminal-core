package units

import "errors"

// ParseSize parses a human-readable size such as "10KB" or "1.5 MB" into a
// number of bytes. Units are B, KB, MB, GB and TB, powers of 1024, in any
// letter case.
func ParseSize(s string) (int64, error) {
	return 0, errors.New("not implemented")
}
