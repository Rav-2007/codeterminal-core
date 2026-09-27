package cart

import "testing"

func TestAddMerges(t *testing.T) {
	var c Cart
	_ = c.Add("A", 1)
	_ = c.Add("A", 2)
	if len(c.Lines) != 1 || c.Lines[0].Qty != 3 {
		t.Fatalf("lines = %+v", c.Lines)
	}
}

func TestRemove(t *testing.T) {
	var c Cart
	_ = c.Add("A", 1)
	_ = c.Add("B", 1)
	c.Remove("A")
	if len(c.Lines) != 1 || c.Lines[0].SKU != "B" {
		t.Fatalf("lines = %+v", c.Lines)
	}
}
