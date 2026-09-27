package report

import (
	"fmt"
	"strings"
)

// SalesTable lays out sales as a table.
func SalesTable(sales []Sale) string {
	headers := []string{"Region", "Units", "Revenue"}
	var rows [][]string
	for _, s := range sales {
		rows = append(rows, []string{s.Region, fmt.Sprint(s.Units), fmt.Sprintf("%.2f", s.Revenue)})
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	for i, h := range headers {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(h + strings.Repeat(" ", widths[i]-len(h)))
	}
	b.WriteString("\n")
	for i := range headers {
		if i > 0 {
			b.WriteString("-+-")
		}
		b.WriteString(strings.Repeat("-", widths[i]))
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i, cell := range r {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// StockTable lays out stock as a table, flagging lines to reorder.
func StockTable(items []Item) string {
	headers := []string{"SKU", "Name", "On hand", "Reorder"}
	var rows [][]string
	for _, it := range items {
		flag := ""
		if it.Reorder {
			flag = "yes"
		}
		rows = append(rows, []string{it.SKU, it.Name, fmt.Sprint(it.OnHand), flag})
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	for i, h := range headers {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(h + strings.Repeat(" ", widths[i]-len(h)))
	}
	b.WriteString("\n")
	for i := range headers {
		if i > 0 {
			b.WriteString("-+-")
		}
		b.WriteString(strings.Repeat("-", widths[i]))
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i, cell := range r {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// StaffTable lays out the rota as a table, with a total line.
func StaffTable(people []Person) string {
	headers := []string{"Name", "Role", "Hours"}
	var rows [][]string
	total := 0.0
	for _, p := range people {
		rows = append(rows, []string{p.Name, p.Role, fmt.Sprintf("%.1f", p.Hours)})
		total += p.Hours
	}
	rows = append(rows, []string{"TOTAL", "", fmt.Sprintf("%.1f", total)})
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, cell := range r {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	for i, h := range headers {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(h + strings.Repeat(" ", widths[i]-len(h)))
	}
	b.WriteString("\n")
	for i := range headers {
		if i > 0 {
			b.WriteString("-+-")
		}
		b.WriteString(strings.Repeat("-", widths[i]))
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i, cell := range r {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
		}
		b.WriteString("\n")
	}
	return b.String()
}
