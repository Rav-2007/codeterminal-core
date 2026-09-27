# semver.Compare

## Goal
Compare two semantic versions with `semver.Compare(a, b string) (int, error)`, exactly as SPEC.md in the project root describes.

## Behaviour
Returns -1, 0 or 1; an error naming the input for anything that is not a valid version. Build metadata is ignored; a pre-release is lower than its release; pre-release identifiers compare numerically when both are numeric, in ASCII order otherwise, numeric below non-numeric, and more identifiers are higher when the rest are equal.

## Scope
**In:** `Compare` in package semver, and its tests. **Out:** parsing into a struct for callers, version ranges.

## Edge cases
- Numbers longer than 64 bits (`99999999999999999999.0.0`) compare correctly.
- `v1.0.0` equals `1.0.0+build.7`.
- `1.0.0-01` and `1.0.0-alpha..1` are errors.

## Success criteria
- [ ] C1: the pre-release chain in SPEC.md orders correctly in every pair -- check: go test ./semver
- [ ] C2: build metadata and a leading v do not change the result -- check: go test ./semver
- [ ] C3: every invalid example in SPEC.md returns an error -- check: go test ./semver
- [ ] C4: numbers longer than 64 bits compare correctly -- check: go test ./semver

## Tasks
- [ ] write semver/semver_test.go covering the criteria
- [ ] implement Compare in semver/semver.go
- [ ] go test ./... passes
