// Package model defines a task.
package model

import (
	"errors"
	"strings"
)

// Task is one thing to do.
type Task struct {
	ID    int
	Title string
	Done  bool
}

// Validate reports what is wrong with t, if anything.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("model: a task needs a title")
	}
	return nil
}
