// Package api answers lookups for remote callers.
package api

import (
	"context"
	"errors"
	"fmt"

	"example.com/kv/store"
)

// Server answers lookups from a store.
type Server struct {
	Store *store.Store
}

// Lookup returns the value of key for a request carrying ctx.
func (s *Server) Lookup(ctx context.Context, key string) (string, error) {
	v, err := s.Store.Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("api: no value for %q", key)
	}
	return v, err
}

// LookupMany returns the values of keys, stopping at the first failure.
func (s *Server) LookupMany(ctx context.Context, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := s.Store.Get(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
