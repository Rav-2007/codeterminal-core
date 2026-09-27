package catalog

// Default is the catalog the command-line tool and the examples use.
func Default() *Catalog {
	c, err := New(
		Product{SKU: "NB-01", Name: "Notebook", Price: 1000},
		Product{SKU: "PN-02", Name: "Pen, pack of 3", Price: 450},
		Product{SKU: "BG-03", Name: "Canvas bag", Price: 2599},
	)
	if err != nil {
		panic(err)
	}
	return c
}
