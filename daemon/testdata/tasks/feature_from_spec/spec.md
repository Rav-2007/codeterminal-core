# Slugify

## Goal
Turn a title into a URL slug with `slug.Slugify(title string) string`, as SPEC.md in the project root describes.

## Behaviour
The rules, applied in order: lowercase everything; spaces and underscores become hyphens; any character other than a-z, 0-9 and hyphen is removed; runs of hyphens collapse into one; leading and trailing hyphens are removed.

## Scope
**In:** `Slugify` in package slug, and its tests. **Out:** transliterating non-ASCII letters.

## Edge cases
- `"---"` becomes `""`.
- `"C++ & Rust"` becomes `"c-rust"` (the removed characters leave hyphens that then collapse).

## Success criteria
- [ ] C1: `Slugify("Hello World")` returns `"hello-world"` -- check: go test ./slug
- [ ] C2: `Slugify("  Go_is FUN!! ")` returns `"go-is-fun"` -- check: go test ./slug
- [ ] C3: `Slugify("C++ & Rust")` returns `"c-rust"` and `Slugify("---")` returns `""` -- check: go test ./slug

## Tasks
- [ ] write slug/slug_test.go covering the criteria
- [ ] implement Slugify in slug/slug.go
- [ ] go test ./... passes
