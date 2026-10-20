// Command mochiii-tui builds Mochiii's terminal client on its own, without
// the daemon and helper roles the `mochiii` binary carries. It is for tests
// and development; with no mochiii-daemon beside it, it reaches only a daemon
// that is already running.
package main

import (
	"os"

	"mochiii/clients/tui"
)

// version is stamped with -ldflags "-X main.version=<version>"; "dev" means a
// local build.
var version = "dev"

func main() {
	tui.Version = version
	tui.Main(os.Args[1:])
}
