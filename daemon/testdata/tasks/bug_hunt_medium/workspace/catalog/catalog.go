// Package catalog holds the products that can be ordered.
package catalog

import (
	"fmt"

	"example.com/shop/money"
)

// Product is one thing that can be ordered.
type Product struct {
	SKU   string
	Name  string
	Price money.Cents
}

// Catalog finds products by SKU.
type Catalog struct {
	bySKU map[string]Product
}

// New builds a catalog from products; a repeated SKU is an error.
func New(products ...Product) (*Catalog, error) {
	c := &Catalog{bySKU: map[string]Product{}}
	for _, p := range products {
		if _, dup := c.bySKU[p.SKU]; dup {
			return nil, fmt.Errorf("catalog: duplicate SKU %q", p.SKU)
		}
		c.bySKU[p.SKU] = p
	}
	return c, nil
}

// Lookup returns the product with sku.
func (c *Catalog) Lookup(sku string) (Product, bool) {
	p, ok := c.bySKU[sku]
	return p, ok
}
