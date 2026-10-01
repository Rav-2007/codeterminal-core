package service

import (
	"context"
	"fmt"

	"example.com/users/store"
)

// Greet greets the user with id.
func Greet(id string) (string, error) {
	u, err := store.LoadAccount(context.Background(), id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Hello, %s", u.Name), nil
}
