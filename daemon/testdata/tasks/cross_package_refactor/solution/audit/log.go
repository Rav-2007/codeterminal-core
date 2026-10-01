package audit

import (
	"fmt"

	"example.com/users/store"
)

// Line is the audit-log line for an action on a user.
func Line(action string, u *store.Account) string {
	return fmt.Sprintf("%s user=%s name=%q", action, u.ID, u.Name)
}
