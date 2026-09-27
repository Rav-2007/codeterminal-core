package handler

import (
	"path/filepath"
	"testing"

	"example.com/tasks/store"
)

func newHandler(t *testing.T) *Handler {
	return &Handler{Store: &store.FileStore{Path: filepath.Join(t.TempDir(), "tasks.json")}}
}

func TestAddAndComplete(t *testing.T) {
	h := newHandler(t)
	a, err := h.Add("first")
	if err != nil || a.ID != 1 {
		t.Fatalf("Add = %+v, %v", a, err)
	}
	if _, err := h.Add(""); err == nil {
		t.Fatal("an empty title was accepted")
	}
	if err := h.Complete(1); err != nil {
		t.Fatal(err)
	}
	list, _ := h.List()
	if len(list) != 1 || !list[0].Done {
		t.Fatalf("List = %+v", list)
	}
}
