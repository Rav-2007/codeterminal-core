# ParseSize

## Goal
Implement `units.ParseSize(s string) (int64, error)` so the existing tests in units/units_test.go pass.

## Behaviour
Parses a human-readable size into bytes. Units are B, KB, MB, GB and TB, powers of 1024, in any letter case; a space between number and unit is allowed; surrounding space is ignored; a bare number is bytes.

## Scope
**In:** ParseSize. **Out:** changing units_test.go.

## Edge cases
- Fractional values are allowed only when the result is a whole number of bytes (`"1.5MB"` yes, `"1.5B"` no).
- Empty, negative, non-numeric or unknown-unit input is an error.

## Success criteria
- [ ] C1: every case in units_test.go passes, unchanged -- check: go test ./units
- [ ] C2: `ParseSize("4 kb")` is 4096 and `ParseSize("0.5KB")` is 512 -- check: go test ./units

## Tasks
- [ ] read units_test.go
- [ ] implement ParseSize
- [ ] go test ./... passes
