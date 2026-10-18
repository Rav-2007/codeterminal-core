// Package quokka is the project the install smoke test indexes and asks about.
// Its one distinctive name, quokkaChecksum, is what the test looks for in the
// request the stand-in provider receives: finding it there proves the code
// search ran and its result reached the prompt.
package quokka

// quokkaChecksum folds a burrow survey into one number, so two surveys of the
// same burrow can be compared at a glance.
func quokkaChecksum(survey []int) int {
	sum := 17
	for _, count := range survey {
		sum = sum*31 + count
	}
	return sum
}
