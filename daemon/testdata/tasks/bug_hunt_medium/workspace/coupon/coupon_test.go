package coupon

import "testing"

func TestFind(t *testing.T) {
	r := Default()
	if c, ok := r.Find("save10"); !ok || c.Value != 10 {
		t.Fatalf("Find(save10) = %+v, %v", c, ok)
	}
	if _, ok := r.Find("nope"); ok {
		t.Fatal("found a coupon that does not exist")
	}
}

func TestDuplicateCode(t *testing.T) {
	if _, err := NewRegistry(Coupon{Code: "A"}, Coupon{Code: "a"}); err == nil {
		t.Fatal("codes differing only in case were both accepted")
	}
}
