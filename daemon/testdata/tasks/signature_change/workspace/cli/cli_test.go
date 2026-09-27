package cli

import (
	"strings"
	"testing"

	"example.com/kv/store"
)

func TestGetAndDump(t *testing.T) {
	s := store.New()
	s.Put("a", "1")
	var b strings.Builder
	if err := Get(&b, s, "a"); err != nil || b.String() != "1\n" {
		t.Fatalf("Get wrote %q, %v", b.String(), err)
	}
	b.Reset()
	Dump(&b, s, []string{"a", "b"})
	if b.String() != "a=1\n" {
		t.Fatalf("Dump wrote %q", b.String())
	}
}
