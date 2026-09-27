package config

import (
	"testing"
	"time"
)

func TestHiddenTimeout(t *testing.T) {
	c := Default()
	if c.Timeout != 30*time.Second {
		t.Errorf("Default().Timeout = %v, want 30s", c.Timeout)
	}
	if got := c.String(); got != "localhost:8080 (timeout 30s)" {
		t.Errorf("String() = %q", got)
	}
}
