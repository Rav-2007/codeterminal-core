package main

import (
	"fmt"
	"io"
)

// safeFprintf performs best‑effort user‑facing output.
// Failure to write does not affect daemon correctness, so the error is ignored.
func safeFprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
