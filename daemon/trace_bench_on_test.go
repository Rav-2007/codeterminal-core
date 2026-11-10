//go:build trace && unix

package main

import (
	"context"
	"os"
	"testing"

	"mochiii/daemon/trace"
	"mochiii/protocol"
)

// The recorder on, for context: the first token writes one line, every later
// token is one compare-and-swap that fails.
func BenchmarkTokenCallbackWithTraceLineOn(b *testing.B) {
	b.Setenv("XDG_RUNTIME_DIR", shortTempDirB(b))
	trace.Reset()
	trace.Enable(trace.Config{Dir: protocol.SocketDir, Open: openNoFollow})
	b.Cleanup(trace.Reset)
	ctx := trace.With(context.Background(), trace.NewTurn(""))
	mc := trace.From(ctx).BeginModelCall()
	if mc == nil {
		b.Fatal("the recorder is not on")
	}
	benchTokens(b, mc, true)
}

func shortTempDirB(b *testing.B) string {
	b.Helper()
	dir, err := os.MkdirTemp("", "ctb")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
