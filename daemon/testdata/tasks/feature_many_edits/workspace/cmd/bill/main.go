package main

import (
	"fmt"

	"example.com/billing/invoice"
	"example.com/billing/money"
	"example.com/billing/report"
)

func main() {
	fmt.Print(report.Render([]invoice.Line{{Desc: "consulting", Price: money.Money{Cents: 12000}, Qty: 3}}))
}
