package main

import (
	"fmt"

	"example.com/rename/shop"
)

func main() {
	items := []shop.Item{{Name: "tea", Price: 4, Qty: 2}}
	fmt.Println(shop.CalcTotal(items))
	fmt.Println(shop.Receipt(items))
}
