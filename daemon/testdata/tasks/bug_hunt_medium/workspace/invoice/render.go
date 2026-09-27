package invoice

import (
	"fmt"
	"strings"
)

// Render lays inv out as plain text.
func Render(inv Invoice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Invoice %s  %s  region %s\n", inv.Number, inv.Issued.Format("2006-01-02"), inv.Region)
	for _, l := range inv.Lines {
		fmt.Fprintf(&b, "  %-20s x%-3d %10s\n", l.Name, l.Qty, l.Amount)
	}
	fmt.Fprintf(&b, "  %-25s %10s\n", "Subtotal", inv.Subtotal)
	if inv.Discount != 0 {
		fmt.Fprintf(&b, "  %-25s %10s\n", "Coupon "+inv.Coupon, -inv.Discount)
	}
	fmt.Fprintf(&b, "  %-25s %10s\n", "Tax", inv.Tax)
	fmt.Fprintf(&b, "  %-25s %10s\n", "Total", inv.Total)
	return b.String()
}
