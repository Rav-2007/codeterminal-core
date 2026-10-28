//go:build linux

package main

import (
	"testing"

	"mochiii/daemon/mcp"
)

// useDaemonsEgressAnswer makes this process's egress-firewall probes answer what
// the daemon's did, for TestTheProdMenuBuilderMatchesTheRealDaemonsFirstRequest.
// Both backends' probes are set, since egressFilterUsable asks the one for the
// backend in use; both are restored when the test ends.
func useDaemonsEgressAnswer(t *testing.T, filtered bool) {
	t.Helper()
	restoreLandlock, restoreBwrap := mcp.EgressFilterUsable, mcp.BwrapEgressUsable
	t.Cleanup(func() { mcp.EgressFilterUsable, mcp.BwrapEgressUsable = restoreLandlock, restoreBwrap })
	answer := func() bool { return filtered }
	mcp.EgressFilterUsable, mcp.BwrapEgressUsable = answer, answer
}
