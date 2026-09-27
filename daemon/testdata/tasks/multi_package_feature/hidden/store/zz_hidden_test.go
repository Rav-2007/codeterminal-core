package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"example.com/tasks/model"
)

func TestHiddenPriorityIsStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	s := &FileStore{Path: path}
	if err := s.Save([]model.Task{{ID: 1, Title: "a", Priority: 3}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 || raw[0]["priority"] != float64(3) {
		t.Errorf("file does not carry \"priority\": 3: %s", data)
	}
	got, err := s.Load()
	if err != nil || len(got) != 1 || got[0].Priority != 3 {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

func TestHiddenOldFileLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"title":"old","done":false}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := (&FileStore{Path: path}).Load()
	if err != nil || len(got) != 1 || got[0].Priority != 0 || got[0].Title != "old" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}
