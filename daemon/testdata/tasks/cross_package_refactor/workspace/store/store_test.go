package store

import "testing"

func TestLoadUser(t *testing.T) {
	u, err := LoadUser("u2")
	if err != nil || u.Name != "Lin" {
		t.Fatalf("LoadUser(u2) = %+v, %v", u, err)
	}
	if _, err := LoadUser("nope"); err != ErrNotFound {
		t.Errorf("LoadUser(nope) err = %v, want ErrNotFound", err)
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
