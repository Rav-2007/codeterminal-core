package store

import (
	"context"
	"errors"
	"testing"
)

func TestHiddenLoadAccount(t *testing.T) {
	var a *Account
	a, err := LoadAccount(context.Background(), "u1")
	if err != nil || a.Name != "Ada" || a.Email != "ada@example.com" {
		t.Fatalf("LoadAccount(u1) = %+v, %v", a, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadAccount(ctx, "u1"); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context gave %v, want context.Canceled", err)
	}
	if _, err := LoadAccount(context.Background(), "zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown id gave %v, want ErrNotFound", err)
	}
}
