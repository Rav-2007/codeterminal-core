// Package semver compares semantic version strings. See SPEC.md.
package semver

import (
	"fmt"
	"strings"
)

type version struct {
	core [3]string
	pre  []string
}

// Compare returns -1, 0 or 1 as a is lower than, equal to or higher than b.
func Compare(a, b string) (int, error) {
	va, err := parse(a)
	if err != nil {
		return 0, err
	}
	vb, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if c := compareNumeric(va.core[i], vb.core[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, nil
	case len(va.pre) == 0:
		return 1, nil
	case len(vb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := compareIdent(va.pre[i], vb.pre[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1, nil
	case len(va.pre) > len(vb.pre):
		return 1, nil
	}
	return 0, nil
}

func parse(s string) (version, error) {
	var v version
	bad := func() (version, error) { return version{}, fmt.Errorf("invalid version %q", s) }
	rest := strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		if !validIdents(rest[i+1:], false) {
			return bad()
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		if !validIdents(rest[i+1:], true) {
			return bad()
		}
		v.pre = strings.Split(rest[i+1:], ".")
		rest = rest[:i]
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return bad()
	}
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return bad()
		}
		v.core[i] = p
	}
	return v, nil
}

// validIdents reports whether s is one or more dot-separated identifiers of
// [0-9A-Za-z-]; for a pre-release, numeric ones may not have leading zeros.
func validIdents(s string, pre bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
				return false
			}
		}
		if pre && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareNumeric compares two digit strings with no leading zeros, of any length.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		return compareNumeric(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
