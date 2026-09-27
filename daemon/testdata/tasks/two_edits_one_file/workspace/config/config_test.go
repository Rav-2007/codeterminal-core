package config

import "testing"

func TestDefault(t *testing.T) {
	c := Default()
	if c.Host != "localhost" || c.Port != 8080 {
		t.Errorf("Default() = %+v", c)
	}
}
