package store

import (
	"context"
	"errors"
	"testing"
)

func TestHiddenGetTakesAContext(t *testing.T) {
	s := New()
	s.Put("a", "1")
	var get func(context.Context, string) (string, error) = s.Get
	if v, err := get(context.Background(), "a"); err != nil || v != "1" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := get(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get with a cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v", err)
	}
}
