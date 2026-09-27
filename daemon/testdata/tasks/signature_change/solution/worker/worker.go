// Package worker processes batches of keys in the background.
package worker

import (
	"context"
	"strings"

	"example.com/kv/store"
)

// Upper reads each key and writes its value back upper-cased, under key+".up".
func Upper(ctx context.Context, s *store.Store, keys []string) (int, error) {
	done := 0
	for _, k := range keys {
		v, err := s.Get(ctx, k)
		if err != nil {
			return done, err
		}
		s.Put(k+".up", strings.ToUpper(v))
		done++
	}
	return done, nil
}

// Exists reports whether key has a value. It is used by health checks that
// carry no context of their own.
func Exists(s *store.Store, key string) bool {
	_, err := s.Get(context.Background(), key)
	return err == nil
}
