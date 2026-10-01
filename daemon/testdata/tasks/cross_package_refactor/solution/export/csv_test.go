package export

import (
	"testing"

	"example.com/users/store"
)

func TestCSV(t *testing.T) {
	got := CSV([]store.Account{{ID: "u9", Name: "Kim", Email: "kim@example.com"}})
	if got != "id,name,email\nu9,Kim,kim@example.com\n" {
		t.Errorf("CSV = %q", got)
	}
}
