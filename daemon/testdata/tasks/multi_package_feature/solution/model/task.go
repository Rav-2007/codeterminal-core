// Package model defines a task.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Task is one thing to do.
type Task struct {
	ID    int
	Title string
	Done  bool
	// Priority is 0 (none), 1 (low), 2 (high) or 3 (urgent).
	Priority int
}

// Validate reports what is wrong with t, if anything.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("model: a task needs a title")
	}
	if t.Priority < 0 || t.Priority > 3 {
		return fmt.Errorf("model: priority must be 0-3, got %d", t.Priority)
	}
	return nil
}
