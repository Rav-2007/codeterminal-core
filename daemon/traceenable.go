//go:build trace

package main

import (
	"fmt"
	"os"

	"mochiii/daemon/trace"
	"mochiii/protocol"
)

// THE SECOND SWITCH. This file exists only in a build made with -tags trace; a
// default daemon has no way to turn tracing on because it has no tracing to turn
// on (daemon/trace/stub.go). In a traced build, MOCHIII_TRACE=1 at start turns the
// recorder on, and nothing else does: not a flag, not a config key, not a
// request. The recorder opens no file until the first request is traced, in
// protocol.SocketDir()/trace, through openNoFollow.
func init() {
	if os.Getenv("MOCHIII_TRACE") != "1" {
		return
	}
	trace.Enable(trace.Config{
		Dir:  protocol.SocketDir,
		Open: openNoFollow,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "mochiii-daemon: "+format+"\n", args...)
		},
	})
}
