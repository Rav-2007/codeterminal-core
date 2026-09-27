# semver.Compare

`func Compare(a, b string) (int, error)` returns -1 if a is lower than b, 0 if they
are equal, and 1 if a is higher. It returns an error, naming the bad input, if
either string is not a valid version.

## What a valid version is

1. An optional leading `v` (`v1.2.3` and `1.2.3` are the same version).
2. `MAJOR.MINOR.PATCH`: exactly three parts, each a non-negative integer with no
   leading zeros (`0` is fine, `01` is not). Numbers may have any number of
   digits, so compare them without converting to a fixed-size integer.
3. Optionally, a pre-release after a `-`: one or more identifiers separated by
   dots. Each identifier is non-empty and made of `0-9`, `A-Z`, `a-z` and `-`.
   An identifier made only of digits is numeric and must not have leading zeros.
4. Optionally, build metadata after a `+`: one or more non-empty identifiers
   separated by dots, made of `0-9`, `A-Z`, `a-z` and `-`. It is ignored when
   comparing.

## How versions compare

1. MAJOR, then MINOR, then PATCH, numerically.
2. When those are equal, a version with a pre-release is lower than one without.
3. Two pre-releases compare identifier by identifier, left to right:
   - two numeric identifiers compare numerically;
   - two other identifiers compare in ASCII order;
   - a numeric identifier is lower than a non-numeric one;
   - if every identifier is equal as far as the shorter one goes, the one with
     more identifiers is higher.

So: `1.0.0-alpha < 1.0.0-alpha.1 < 1.0.0-alpha.beta < 1.0.0-beta < 1.0.0-beta.2 <
1.0.0-beta.11 < 1.0.0-rc.1 < 1.0.0`.

## Invalid, for example

`""`, `1.0`, `1.0.0.0`, `01.0.0`, `1.0.0-`, `1.0.0-alpha..1`, `1.0.0-01`, `1.0.0+`,
`a.b.c`, `1.0.0-al$pha`.
