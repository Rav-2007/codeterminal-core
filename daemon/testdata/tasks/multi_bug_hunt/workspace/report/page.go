package report

// Paginate splits items into pages of at most size items, in order.
func Paginate(items []string, size int) [][]string {
	if size <= 0 {
		return nil
	}
	pages := make([][]string, 0, len(items)/size)
	for i := 0; i+size <= len(items); i += size {
		pages = append(pages, items[i:i+size])
	}
	return pages
}
