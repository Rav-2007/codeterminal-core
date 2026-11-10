package main

import (
	"context"
	"strings"
	"testing"

	"mochiii/daemon/trace"
)

// THE COST OF THE TRACE ON THE MODEL-TOKEN HOT PATH (D4).
//
// The token callback runs once per streamed token. In the loop it is
//
//	func(token string) error {
//		if mc != nil { mc.Token() }   // the D4 line
//		full.WriteString(token)
//		return guard.show(...)
//	}
//
// and these benchmarks run exactly that shape. Run all of them, with and without
// the tag, to read the three cases the PR reports:
//
//	go test -run '^$' -bench TokenCallback -benchtime 2s -count 6 .             # trace code compiled OUT
//	go test -tags trace -run '^$' -bench TokenCallback -benchtime 2s -count 6 . # compiled IN: off, and on
//
// Baseline has no trace line at all (the code before this change). WithTraceLine
// has it, and in a default build `trace.From` is a constant nil so the line is
// removed; with the tag and the switch off, `mc` is nil and the line is one
// compare. (The "on" case is in trace_bench_on_test.go.)

func benchTokens(b *testing.B, mc *trace.ModelCall, withLine bool) {
	var full strings.Builder
	next := func(string) error { return nil }
	cb := func(token string) error {
		if withLine && mc != nil {
			mc.Token()
		}
		full.WriteString(token)
		return next(token)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb("tok")
		if full.Len() > 1<<16 {
			full.Reset()
		}
	}
}

func BenchmarkTokenCallbackBaseline(b *testing.B) {
	benchTokens(b, nil, false)
}

func BenchmarkTokenCallbackWithTraceLineOff(b *testing.B) {
	mc := trace.From(context.Background()).BeginModelCall() // nil: no turn on this context
	benchTokens(b, mc, true)
}
