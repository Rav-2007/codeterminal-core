package report

// Paginate splits items into pages of at most size items, in order.
func Paginate(items []string, size int) [][]string {
	if size <= 0 {
		return nil
	}
	pages := make([][]string, 0, (len(items)+size-1)/size)
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		pages = append(pages, items[i:end])
	}
	return pages
}
