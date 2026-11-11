//go:build trace

package main

// e2eBuildTags is the build tag the end-to-end harness gives the daemon it
// builds: the same ones this test binary has, so `go test -tags trace` runs the
// real daemon with the recorder compiled in.
const e2eBuildTags = "trace"
