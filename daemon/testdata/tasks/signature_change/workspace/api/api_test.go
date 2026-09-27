package api

import (
	"context"
	"testing"

	"example.com/kv/store"
)

func TestLookup(t *testing.T) {
	st := store.New()
	st.Put("k", "v")
	s := &Server{Store: st}
	if v, err := s.Lookup(context.Background(), "k"); err != nil || v != "v" {
		t.Fatalf("Lookup = %q, %v", v, err)
	}
	if _, err := s.Lookup(context.Background(), "nope"); err == nil {
		t.Fatal("no error for a missing key")
	}
}
