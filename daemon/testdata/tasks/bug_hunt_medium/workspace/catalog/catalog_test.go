package catalog

import "testing"

func TestLookup(t *testing.T) {
	c := Default()
	if p, ok := c.Lookup("NB-01"); !ok || p.Price != 1000 {
		t.Fatalf("Lookup(NB-01) = %+v, %v", p, ok)
	}
	if _, ok := c.Lookup("nope"); ok {
		t.Fatal("found a product that does not exist")
	}
}

func TestDuplicateSKU(t *testing.T) {
	if _, err := New(Product{SKU: "A"}, Product{SKU: "A"}); err == nil {
		t.Fatal("a duplicate SKU was accepted")
	}
}
