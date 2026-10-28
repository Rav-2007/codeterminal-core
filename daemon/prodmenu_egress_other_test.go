//go:build unix && !linux

package main

import "testing"

// useDaemonsEgressAnswer has nothing to align off Linux: there egressFilterUsable
// is false on both sides (sandboxegress_other.go), so they cannot disagree.
func useDaemonsEgressAnswer(*testing.T, bool) {}
