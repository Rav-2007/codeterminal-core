package ledger

import (
	"example.com/ledger/interest"
	"example.com/ledger/parse"
	"example.com/ledger/report"
)

// Entry is one transaction.
type Entry struct {
	Description string
	Amount      string
}

// Total parses and sums the entries' amounts, in cents.
func Total(entries []Entry) (int64, error) {
	var total int64
	for _, e := range entries {
		cents, err := parse.ParseAmount(e.Amount)
		if err != nil {
			return 0, err
		}
		total += cents
	}
	return total, nil
}

// Projected is a balance after years of interest.
func Projected(balance int64, rate float64, years int) int64 {
	return interest.Compound(balance, rate, years)
}

// Pages lists the entries' descriptions, size per page.
func Pages(entries []Entry, size int) [][]string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Description)
	}
	return report.Paginate(names, size)
}
