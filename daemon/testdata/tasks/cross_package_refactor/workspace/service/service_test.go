package service

import (
	"strings"
	"testing"
)

func TestGreet(t *testing.T) {
	if got, err := Greet("u1"); err != nil || got != "Hello, Ada" {
		t.Errorf("Greet(u1) = %q, %v", got, err)
	}
}

func TestDirectory(t *testing.T) {
	if got := Directory(); !strings.HasPrefix(got, "Ada <ada@example.com>") {
		t.Errorf("Directory() = %q", got)
	}
}
