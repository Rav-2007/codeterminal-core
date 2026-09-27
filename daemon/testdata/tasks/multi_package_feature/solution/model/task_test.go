package model

import "testing"

func TestValidateNeedsATitle(t *testing.T) {
	if err := (Task{Title: "  "}).Validate(); err == nil {
		t.Fatal("a blank title was accepted")
	}
	if err := (Task{Title: "write"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePriority(t *testing.T) {
	if err := (Task{Title: "x", Priority: 3}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Task{Title: "x", Priority: 4}).Validate(); err == nil {
		t.Fatal("priority 4 was accepted")
	}
}
