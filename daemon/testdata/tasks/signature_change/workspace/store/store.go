// Package store is an in-memory key-value store.
package store

import (
	"errors"
	"sync"
)

// ErrNotFound is returned for a key that has no value.
var ErrNotFound = errors.New("store: not found")

// Store holds string values by key. It is safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	data map[string]string
}

// New returns an empty store.
func New() *Store { return &Store{data: map[string]string{}} }

// Put sets key to value.
func (s *Store) Put(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

// Get returns the value of key.
func (s *Store) Get(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
