package store

import (
	"context"
	"errors"
	"testing"
)

func TestPutThenGet(t *testing.T) {
	s := New()
	s.Put("a", "1")
	if v, err := s.Get(context.Background(), "a"); err != nil || v != "1" {
		t.Fatalf("Get(a) = %q, %v", v, err)
	}
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v", err)
	}
}
