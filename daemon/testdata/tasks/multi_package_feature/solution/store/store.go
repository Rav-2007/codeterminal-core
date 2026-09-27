// Package store keeps tasks in a JSON file.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"example.com/tasks/model"
)

// record is a task as it is written to the file.
type record struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
	// Priority is absent from files written before it existed; those load as 0.
	Priority int `json:"priority"`
}

func toRecord(t model.Task) record {
	return record{ID: t.ID, Title: t.Title, Done: t.Done, Priority: t.Priority}
}

func fromRecord(r record) model.Task {
	return model.Task{ID: r.ID, Title: r.Title, Done: r.Done, Priority: r.Priority}
}

// FileStore reads and writes all tasks at once.
type FileStore struct {
	Path string
}

// Load returns every task in the file; a missing file is no tasks.
func (s *FileStore) Load() ([]model.Task, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	tasks := make([]model.Task, 0, len(recs))
	for _, r := range recs {
		tasks = append(tasks, fromRecord(r))
	}
	return tasks, nil
}

// Save replaces the file's tasks with tasks.
func (s *FileStore) Save(tasks []model.Task) error {
	recs := make([]record, 0, len(tasks))
	for _, t := range tasks {
		recs = append(recs, toRecord(t))
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o644)
}
