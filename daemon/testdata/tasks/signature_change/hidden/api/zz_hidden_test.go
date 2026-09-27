package api

import (
	"context"
	"errors"
	"testing"

	"example.com/kv/store"
)

func TestHiddenLookupPassesItsContext(t *testing.T) {
	st := store.New()
	st.Put("k", "v")
	s := &Server{Store: st}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Lookup(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Lookup with a cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := s.LookupMany(ctx, []string{"k"}); !errors.Is(err, context.Canceled) {
		t.Errorf("LookupMany with a cancelled context: err = %v, want context.Canceled", err)
	}
}
