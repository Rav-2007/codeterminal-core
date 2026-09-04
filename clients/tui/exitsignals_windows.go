//go:build windows

package main

// Windows has no SIGHUP, SIGQUIT or SIGPIPE, and the console teardown Bubble
// Tea already performs covers the ways a program ends there. Both of these are
// no-ops rather than absent so that main.go and oneshot.go stay a single,
// unbranched story on every platform -- the alternative is build tags around
// the call sites, which is where the last per-platform divergence in this
// package hid a bug for three weeks.
func installExitSignals(quit func()) (finish func()) { return func() {} }

func ignoreSIGPIPE() (restore func()) { return func() {} }
