package service

import (
	"fmt"

	"example.com/users/store"
)

// Greet greets the user with id.
func Greet(id string) (string, error) {
	u, err := store.LoadUser(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Hello, %s", u.Name), nil
}
