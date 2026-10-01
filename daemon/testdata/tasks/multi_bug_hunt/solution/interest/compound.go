package interest

// Compound returns principal (in cents) after years of annual compounding at
// rate, rounded to the nearest cent.
func Compound(principal int64, rate float64, years int) int64 {
	amount := float64(principal)
	for y := 0; y < years; y++ {
		amount *= 1 + rate
	}
	return int64(amount + 0.5)
}
