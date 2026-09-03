package main

import (
	"strings"
	"testing"
)

// The filter sits on the per-token path, so its cost is measured, not assumed.
// The clean case is the one that matters: ordinary prose is what almost every
// token is, and it must pass through without allocating.
func BenchmarkSanitizeCleanToken(b *testing.B) {
	var z escSanitizer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = z.Write("ordinary answer text ")
	}
}

func BenchmarkSanitizeHighlightedToken(b *testing.B) {
	var z escSanitizer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = z.Write("\x1b[38;5;204mfunc\x1b[0m ")
	}
}

func BenchmarkSanitizeHostileToken(b *testing.B) {
	var z escSanitizer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = z.Write("\x1b[2J\x1b]0;t\x07x")
	}
}

// D-1's hard ceiling names a 1MB paste as an input no Update may exceed 16ms
// on. This is the filter's share of that.
func BenchmarkSanitizeLargePaste(b *testing.B) {
	s := strings.Repeat("some pasted source line with unicode → 世界\n", 25000)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeText(s)
	}
}
