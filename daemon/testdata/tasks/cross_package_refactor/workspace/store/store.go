package store

import (
	"errors"
	"sort"
)

// UserRecord is a stored user.
type UserRecord struct {
	ID    string
	Name  string
	Email string
}

// ErrNotFound is returned for an ID no user has.
var ErrNotFound = errors.New("not found")

var users = map[string]UserRecord{
	"u1": {ID: "u1", Name: "Ada", Email: "ada@example.com"},
	"u2": {ID: "u2", Name: "Lin", Email: "lin@example.com"},
	"u3": {ID: "u3", Name: "Sam", Email: "sam@example.com"},
}

// LoadUser returns the user with id.
func LoadUser(id string) (*UserRecord, error) {
	u, ok := users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &u, nil
}

// ListUsers returns every user, by ID.
func ListUsers() []UserRecord {
	out := make([]UserRecord, 0, len(users))
	for _, u := range users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
