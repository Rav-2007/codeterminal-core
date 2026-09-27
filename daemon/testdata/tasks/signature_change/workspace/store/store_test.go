package store

import (
	"errors"
	"testing"
)

func TestPutThenGet(t *testing.T) {
	s := New()
	s.Put("a", "1")
	if v, err := s.Get("a"); err != nil || v != "1" {
		t.Fatalf("Get(a) = %q, %v", v, err)
	}
	if _, err := s.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v", err)
	}
}
