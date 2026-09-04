//go:build race

package main

// raceEnabled is true when the binary was built with -race.
//
// Timing and allocation measurements are not comparable under the race
// detector: it adds bookkeeping to every memory access, which inflates
// wall-clock by roughly an order of magnitude and changes allocation counts.
// The repository's gate runs `go test -race`, so without this the performance
// tests would either fail there or have to be loosened until they measured
// nothing. They run in the ordinary (non-race) test pass instead, which is the
// same pass the coverage ratchet uses.
const raceEnabled = true
