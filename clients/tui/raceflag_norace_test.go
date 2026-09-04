//go:build !race

package main

// raceEnabled is false in an ordinary build. See raceflag_race_test.go.
const raceEnabled = false
