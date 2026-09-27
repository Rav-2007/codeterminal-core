package shop

import "fmt"

// Receipt renders a one-line receipt.
func Receipt(items []Item) string {
	return fmt.Sprintf("%d item(s), total %d", len(items), Total(items))
}
