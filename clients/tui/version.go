package main

// version is this client's release version. release.yml stamps it with
// -ldflags "-X main.version=<tag without the v>"; "dev" means a local build.
// `mochiii --version` prints it, which is how the installers check that the
// binary they just unpacked runs and is the one they meant to install.
var version = "dev"
