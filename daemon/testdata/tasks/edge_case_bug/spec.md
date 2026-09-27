# Average never returns NaN

## Goal
Users report `stats.Average` sometimes returns NaN. Make that impossible.

## Behaviour
`Average` returns the arithmetic mean, and 0 for an empty or nil slice.

## Scope
**In:** Average and its tests. **Out:** other statistics.

## Edge cases
- `Average(nil)` and `Average([]float64{})` return 0.

## Success criteria
- [ ] C1: `Average(nil)` and `Average([]float64{})` return 0 -- check: go test ./stats
- [ ] C2: a test in stats/ covers the empty case -- check: go test ./stats
- [ ] C3: `Average([]float64{1, 2, 3})` still returns 2 -- check: go test ./stats

## Tasks
- [ ] add a failing test for the empty case
- [ ] fix Average
- [ ] go test ./... passes
