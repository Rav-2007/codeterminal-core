package worker

import (
	"context"
	"testing"

	"example.com/kv/store"
)

func TestUpper(t *testing.T) {
	s := store.New()
	s.Put("a", "x")
	n, err := Upper(context.Background(), s, []string{"a"})
	if err != nil || n != 1 {
		t.Fatalf("Upper = %d, %v", n, err)
	}
	if !Exists(s, "a.up") {
		t.Fatal("a.up was not written")
	}
}
