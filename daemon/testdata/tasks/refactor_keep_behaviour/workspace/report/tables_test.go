package report

import (
	"strings"
	"testing"
)

func TestSalesTableHasEveryRegion(t *testing.T) {
	out := SalesTable([]Sale{{"North", 12, 340.5}, {"South", 3, 99}})
	for _, want := range []string{"Region", "North", "South", "340.50"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestStaffTableHasATotal(t *testing.T) {
	out := StaffTable([]Person{{"Ana", "cook", 30}, {"Bo", "host", 12.5}})
	if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "42.5") {
		t.Errorf("no total in:\n%s", out)
	}
}
