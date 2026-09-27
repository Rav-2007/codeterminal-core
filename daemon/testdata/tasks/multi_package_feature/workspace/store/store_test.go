package store

import (
	"path/filepath"
	"testing"

	"example.com/tasks/model"
)

func TestSaveThenLoad(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "tasks.json")}
	want := []model.Task{{ID: 1, Title: "a"}, {ID: 2, Title: "b", Done: true}}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || len(got) != 2 || got[1] != want[1] {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

func TestMissingFileIsNoTasks(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "none.json")}
	if got, err := s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}
