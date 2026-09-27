// Package report renders plain-text tables for the weekly summary email.
package report

// Sale is one region's sales for the week.
type Sale struct {
	Region  string
	Units   int
	Revenue float64
}

// Item is one stock line.
type Item struct {
	SKU     string
	Name    string
	OnHand  int
	Reorder bool
}

// Person is one member of staff on the rota.
type Person struct {
	Name  string
	Role  string
	Hours float64
}
