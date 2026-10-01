package api

import "testing"

func TestProfile(t *testing.T) {
	if code, body := Profile("u3"); code != 200 || body != "Sam,sam@example.com" {
		t.Errorf("Profile(u3) = %d %q", code, body)
	}
	if code, _ := Profile("x"); code != 404 {
		t.Errorf("Profile(x) = %d, want 404", code)
	}
}
