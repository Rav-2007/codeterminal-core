package worker

import (
	"context"
	"errors"
	"testing"

	"example.com/kv/store"
)

func TestHiddenUpperPassesItsContext(t *testing.T) {
	s := store.New()
	s.Put("a", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Upper(ctx, s, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Upper with a cancelled context: err = %v, want context.Canceled", err)
	}
	if !Exists(s, "a") {
		t.Error("Exists stopped working")
	}
}
