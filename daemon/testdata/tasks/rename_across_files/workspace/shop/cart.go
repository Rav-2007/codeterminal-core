package shop

// Item is one line in a cart.
type Item struct {
	Name  string
	Price int
	Qty   int
}

// CalcTotal returns the cart's total price.
func CalcTotal(items []Item) int {
	total := 0
	for _, it := range items {
		total += it.Price * it.Qty
	}
	return total
}
