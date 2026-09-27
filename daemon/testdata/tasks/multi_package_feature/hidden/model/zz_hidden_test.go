package model

import "testing"

func TestHiddenPriorityRange(t *testing.T) {
	for p := 0; p <= 3; p++ {
		if err := (Task{Title: "x", Priority: p}).Validate(); err != nil {
			t.Errorf("priority %d rejected: %v", p, err)
		}
	}
	for _, p := range []int{-1, 4, 100} {
		if err := (Task{Title: "x", Priority: p}).Validate(); err == nil {
			t.Errorf("priority %d accepted", p)
		}
	}
}
