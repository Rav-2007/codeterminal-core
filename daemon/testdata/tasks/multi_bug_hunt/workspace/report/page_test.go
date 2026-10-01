package report

import "testing"

func TestPaginateFullPages(t *testing.T) {
	pages := Paginate([]string{"a", "b", "c", "d", "e", "f"}, 3)
	if len(pages) != 2 || len(pages[1]) != 3 {
		t.Errorf("Paginate(6 items, 3) = %v", pages)
	}
}
