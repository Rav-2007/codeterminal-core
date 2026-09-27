// Command shop prices an order from the command line:
//
//	shop -region EU -coupon SAVE10 NB-01:3 PN-02:1
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"example.com/shop/cart"
	"example.com/shop/catalog"
	"example.com/shop/coupon"
	"example.com/shop/internal/audit"
	"example.com/shop/invoice"
)

func main() {
	region := flag.String("region", "US", "tax region")
	code := flag.String("coupon", "", "coupon code")
	flag.Parse()

	var c cart.Cart
	for _, arg := range flag.Args() {
		sku, qty, _ := strings.Cut(arg, ":")
		n, err := strconv.Atoi(qty)
		if err != nil {
			n = 1
		}
		if err := c.Add(sku, n); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	log := audit.Log{W: os.Stderr}
	log.Record("price", *region, *code)
	inv, err := invoice.Build("CLI-1", time.Now(), c, catalog.Default(), coupon.Default(), *code, *region)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(invoice.Render(inv))
}
