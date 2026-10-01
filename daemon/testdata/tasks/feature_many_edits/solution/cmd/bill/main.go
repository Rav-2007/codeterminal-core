package main

import (
	"fmt"
	"os"

	"example.com/billing/invoice"
	"example.com/billing/money"
	"example.com/billing/report"
)

func main() {
	out, err := report.Render([]invoice.Line{{Desc: "consulting", Price: money.Money{Cents: 12000, Currency: "EUR"}, Qty: 3}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(out)
}
