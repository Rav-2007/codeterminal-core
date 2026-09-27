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
