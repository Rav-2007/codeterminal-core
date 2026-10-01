package service

import (
	"strings"

	"example.com/users/store"
)

// Directory lists users as "Name <email>" lines.
func Directory() string {
	var lines []string
	for _, u := range store.ListUsers() {
		lines = append(lines, describe(u))
	}
	return strings.Join(lines, "\n")
}

func describe(u store.Account) string {
	return u.Name + " <" + u.Email + ">"
}
