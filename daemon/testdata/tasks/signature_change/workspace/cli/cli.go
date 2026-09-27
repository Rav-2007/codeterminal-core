// Package cli implements the kv command's subcommands.
package cli

import (
	"fmt"
	"io"

	"example.com/kv/store"
)

// Get prints the value of key to w.
func Get(w io.Writer, s *store.Store, key string) error {
	v, err := s.Get(key)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, v)
	return err
}

// Dump prints key=value for each of keys that has a value.
func Dump(w io.Writer, s *store.Store, keys []string) {
	for _, k := range keys {
		if v, err := s.Get(k); err == nil {
			fmt.Fprintf(w, "%s=%s\n", k, v)
		}
	}
}
