package api

import (
	"context"
	"errors"

	"example.com/users/store"
)

// Profile answers a profile request: a status code and a body.
func Profile(id string) (int, string) {
	u, err := store.LoadAccount(context.Background(), id)
	if errors.Is(err, store.ErrNotFound) {
		return 404, "no such user"
	}
	if err != nil {
		return 500, "error"
	}
	return 200, u.Name + "," + u.Email
}
