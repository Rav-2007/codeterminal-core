package api

import "testing"

func TestHiddenProfileUnchanged(t *testing.T) {
	if code, body := Profile("u1"); code != 200 || body != "Ada,ada@example.com" {
		t.Errorf("Profile(u1) = %d %q", code, body)
	}
}
