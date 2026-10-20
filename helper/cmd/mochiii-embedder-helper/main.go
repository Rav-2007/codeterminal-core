// Command mochiii-embedder-helper builds the embedder helper on its own. The
// release ships it as a role of the one `mochiii` binary; this build is for
// tests and for comparing one binding with another (see
// helper/otherhelper_test.go).
package main

import (
	"os"

	"mochiii/helper"
)

// version is stamped with -ldflags "-X main.version=<version>"; "dev" means a
// local build.
var version = "dev"

func main() {
	helper.Version = version
	os.Exit(helper.Main(os.Args[1:]))
}
