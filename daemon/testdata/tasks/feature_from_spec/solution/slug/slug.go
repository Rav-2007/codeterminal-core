package slug

import "strings"

// Slugify turns a title into a URL slug, following SPEC.md.
func Slugify(title string) string {
	s := strings.ToLower(title)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '_' || r == '-':
			b.WriteRune('-')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
