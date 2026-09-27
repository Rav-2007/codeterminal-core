# IsPalindrome

## Goal
Add `strutil.IsPalindrome(s string) bool`, with a test.

## Behaviour
Reports whether s reads the same forwards and backwards, ignoring letter case and spaces.

## Scope
**In:** IsPalindrome and its test in package strutil. **Out:** ignoring punctuation.

## Edge cases
- The empty string is a palindrome.

## Success criteria
- [ ] C1: `IsPalindrome("Never odd or even")` is true -- check: go test ./strutil
- [ ] C2: `IsPalindrome("abc")` is false and `IsPalindrome("")` is true -- check: go test ./strutil
- [ ] C3: a test in strutil/ calls IsPalindrome -- check: go test ./strutil

## Tasks
- [ ] write the test
- [ ] implement IsPalindrome
- [ ] go test ./... passes
