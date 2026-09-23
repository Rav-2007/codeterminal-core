package main

import (
	"testing"

	"mochiii/daemon/mcp"
)

// selectOrphanScopes reaps a scope only when the daemon that made it is gone. It
// must never choose our own scopes (self) -- our calls may still be running --
// nor a scope whose pid is still alive, which belongs to a concurrent daemon's
// in-flight call. A dead pid is the sole thing that marks an orphan.
//
// This is the whole safety argument for the startup sweep, tested without a
// systemd on the host by supplying the liveness oracle directly.
//
// Neuter check: drop the `u.PID == self` guard and a live daemon's own scope is
// reaped out from under it; drop the `alive(u.PID)` guard and a concurrent
// daemon's running scope is killed.
func TestSelectOrphanScopesReapsOnlyDeadDaemonsScopes(t *testing.T) {
	const self = 1000
	units := []mcp.SandboxScopeUnit{
		{Name: "mochiii-sandbox-1000-1.scope", PID: self}, // ours -- keep
		{Name: "mochiii-sandbox-2000-1.scope", PID: 2000}, // a live sibling -- keep
		{Name: "mochiii-sandbox-3000-1.scope", PID: 3000}, // a dead daemon -- reap
		{Name: "mochiii-sandbox-3000-2.scope", PID: 3000}, // same dead daemon -- reap
		{Name: "mochiii-sandbox-4000-9.scope", PID: 4000}, // another dead daemon -- reap
	}
	live := map[int]bool{self: true, 2000: true}
	alive := func(pid int) bool { return live[pid] }

	got := selectOrphanScopes(units, self, alive)
	want := []string{
		"mochiii-sandbox-3000-1.scope",
		"mochiii-sandbox-3000-2.scope",
		"mochiii-sandbox-4000-9.scope",
	}
	if len(got) != len(want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("orphan %d = %q, want %q", i, got[i], want[i])
		}
	}

	// The all-alive case reaps nothing, so a sweep on a busy machine is a no-op
	// rather than a hazard.
	if orphans := selectOrphanScopes(units, self, func(int) bool { return true }); len(orphans) != 0 {
		t.Errorf("with every pid alive, selected %v, want none", orphans)
	}
}
