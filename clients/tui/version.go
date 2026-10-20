package tui

// Version is this client's release version. The program that runs Main sets
// it from its own stamp (release.yml's -X on `mochiii`, or -X main.version on
// cmd/mochiii-tui); "dev" means a local build. `mochiii --version` prints it,
// which is how the installers check that the binary they just unpacked runs
// and is the one they meant to install.
var Version = "dev"
