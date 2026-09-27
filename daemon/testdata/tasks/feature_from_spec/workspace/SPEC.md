# Slugify

`slug.Slugify(title string) string` turns a title into a URL slug.

Rules, applied in this order:

1. Lowercase everything.
2. Spaces and underscores become hyphens.
3. Any character other than a-z, 0-9 and hyphen is removed.
4. Runs of hyphens collapse into one.
5. Leading and trailing hyphens are removed.

Examples:

- "Hello World" -> "hello-world"
- "  Go_is FUN!! " -> "go-is-fun"
- "C++ & Rust" -> "c-rust"
- "---" -> ""
