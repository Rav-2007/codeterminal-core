package export

import (
	"strings"

	"example.com/users/store"
)

// CSV renders users as id,name,email rows with a header.
func CSV(users []store.UserRecord) string {
	var b strings.Builder
	b.WriteString("id,name,email\n")
	for _, u := range users {
		b.WriteString(u.ID + "," + u.Name + "," + u.Email + "\n")
	}
	return b.String()
}
