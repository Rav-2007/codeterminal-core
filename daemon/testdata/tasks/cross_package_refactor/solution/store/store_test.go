package store

import (
	"context"
	"testing"
)

func TestLoadAccount(t *testing.T) {
	u, err := LoadAccount(context.Background(), "u2")
	if err != nil || u.Name != "Lin" {
		t.Fatalf("LoadAccount(u2) = %+v, %v", u, err)
	}
	if _, err := LoadAccount(context.Background(), "nope"); err != ErrNotFound {
		t.Errorf("LoadAccount(nope) err = %v, want ErrNotFound", err)
	}
}

func TestListUsersIsSorted(t *testing.T) {
	var ids []string
	for _, u := range ListUsers() {
		ids = append(ids, u.ID)
	}
	if len(ids) != 3 || ids[0] != "u1" || ids[2] != "u3" {
		t.Errorf("ListUsers ids = %v", ids)
	}
}
