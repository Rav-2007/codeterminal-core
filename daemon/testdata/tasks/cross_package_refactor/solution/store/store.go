package store

import (
	"context"
	"errors"
	"sort"
)

// Account is a stored user.
type Account struct {
	ID    string
	Name  string
	Email string
}

// ErrNotFound is returned for an ID no user has.
var ErrNotFound = errors.New("not found")

var users = map[string]Account{
	"u1": {ID: "u1", Name: "Ada", Email: "ada@example.com"},
	"u2": {ID: "u2", Name: "Lin", Email: "lin@example.com"},
	"u3": {ID: "u3", Name: "Sam", Email: "sam@example.com"},
}

// LoadAccount returns the account with id, or ctx.Err() when ctx is done.
func LoadAccount(ctx context.Context, id string) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, ok := users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &u, nil
}

// ListUsers returns every user, by ID.
func ListUsers() []Account {
	out := make([]Account, 0, len(users))
	for _, u := range users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
