package tax

import "testing"

func TestOn(t *testing.T) {
	got, err := On(2700, "EU")
	if err != nil || got != 567 {
		t.Fatalf("On(2700, EU) = %d, %v", got, err)
	}
	if _, err := On(100, "XX"); err == nil {
		t.Fatal("an unknown region was accepted")
	}
}
