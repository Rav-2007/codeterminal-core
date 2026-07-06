// Package validate checks user-submitted contact fields (email addresses
// and phone numbers) for well-formedness before they're stored.
package validate

import (
	"regexp"
	"strings"
)

var emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// Email reports whether addr looks like a valid email address. This is a
// deliberately permissive syntactic check (matching the general shape
// local-part@domain.tld) — it does not verify the domain exists or that
// the mailbox can receive mail.
func Email(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	return emailPattern.MatchString(addr)
}

// phonePattern accepts an optional leading "+", then 7-15 digits, with
// spaces, hyphens, or parentheses allowed as separators.
var phonePattern = regexp.MustCompile(`^\+?[0-9()\-\s]{7,20}$`)

// Phone reports whether number looks like a valid phone number: an
// optional country code prefix followed by 7-15 digits, ignoring common
// separator characters. Numbers that are too short, too long, or contain
// letters are rejected.
func Phone(number string) bool {
	number = strings.TrimSpace(number)
	if !phonePattern.MatchString(number) {
		return false
	}

	digitCount := 0
	for _, r := range number {
		if r >= '0' && r <= '9' {
			digitCount++
		}
	}
	return digitCount >= 7 && digitCount <= 15
}
