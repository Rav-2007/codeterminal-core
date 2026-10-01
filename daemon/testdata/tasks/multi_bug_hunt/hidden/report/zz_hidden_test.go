package report

import "testing"

func TestHiddenPartialLastPage(t *testing.T) {
	pages := Paginate([]string{"a", "b", "c", "d", "e", "f", "g"}, 3)
	if len(pages) != 3 || len(pages[2]) != 1 || pages[2][0] != "g" {
		t.Errorf("Paginate(7 items, 3) = %v, want three pages ending with [g]", pages)
	}
}
