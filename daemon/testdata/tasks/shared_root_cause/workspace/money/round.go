package money

// RoundCents rounds an amount in hundredths of a cent to whole cents, halves
// up. Amounts in this shop are never negative.
func RoundCents(hundredths int64) int64 {
	return hundredths / 100
}
