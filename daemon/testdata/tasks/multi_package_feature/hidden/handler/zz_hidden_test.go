package handler

import (
	"path/filepath"
	"testing"

	"example.com/tasks/store"
)

func TestHiddenAddAndListByPriority(t *testing.T) {
	h := &Handler{Store: &store.FileStore{Path: filepath.Join(t.TempDir(), "tasks.json")}}
	for _, p := range []int{1, 3, 0, 3, 2} {
		if _, err := h.Add("t", p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Add("bad", 4); err == nil {
		t.Error("priority 4 was accepted")
	}
	if _, err := h.Add("bad", -1); err == nil {
		t.Error("priority -1 was accepted")
	}
	list, err := h.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, task := range list {
		ids = append(ids, task.ID)
	}
	want := []int{2, 4, 5, 1, 3}
	if len(ids) != len(want) {
		t.Fatalf("List IDs = %v, want %v (nothing stored for a rejected priority)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("List IDs = %v, want %v", ids, want)
		}
	}
}
