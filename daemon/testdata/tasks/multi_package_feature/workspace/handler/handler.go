// Package handler is what the task commands call.
package handler

import (
	"fmt"
	"sort"

	"example.com/tasks/model"
	"example.com/tasks/store"
)

// Handler adds, lists and completes tasks.
type Handler struct {
	Store *store.FileStore
}

// Add creates a task with the next free ID.
func (h *Handler) Add(title string) (model.Task, error) {
	tasks, err := h.Store.Load()
	if err != nil {
		return model.Task{}, err
	}
	next := 1
	for _, t := range tasks {
		if t.ID >= next {
			next = t.ID + 1
		}
	}
	task := model.Task{ID: next, Title: title}
	if err := task.Validate(); err != nil {
		return model.Task{}, err
	}
	if err := h.Store.Save(append(tasks, task)); err != nil {
		return model.Task{}, err
	}
	return task, nil
}

// List returns every task, by ID.
func (h *Handler) List() ([]model.Task, error) {
	tasks, err := h.Store.Load()
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Complete marks task id done.
func (h *Handler) Complete(id int) error {
	tasks, err := h.Store.Load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			tasks[i].Done = true
			return h.Store.Save(tasks)
		}
	}
	return fmt.Errorf("handler: no task %d", id)
}
