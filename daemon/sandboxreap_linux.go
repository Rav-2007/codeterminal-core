//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"time"

	"mochiii/daemon/mcp"
)

// reapOrphanedSandboxScopes kills transient landlock sandbox scopes an EARLIER
// daemon left behind. A clean shutdown reaps its own (the deferred
// reapSandboxScope unwinds as the cancelled call returns), but a SIGKILL or a
// crash runs no deferred code, so a scope -- and any process the build
// backgrounded in it, still confined but holding workspace and network -- can
// outlive the daemon. bwrap gets this for free from its PID namespace and
// --die-with-parent; landlock has neither, so the next daemon sweeps at startup.
//
// This does not make reaping instant at the moment of death (a systemd --scope
// cannot be tied to a non-systemd parent's lifetime); it bounds an orphan's life
// to the next daemon start instead of leaving it running indefinitely. Run in
// the background, from startup, and never allowed to delay or fail the daemon --
// exactly like reclaimSandboxHomes, whose shape this mirrors.
func (s *Server) reapOrphanedSandboxScopes() {
	// Only landlock-with-a-limiter ever names a scope; on any other host the list
	// is empty and this whole pass is a no-op. Both checks are memoised.
	if !mcp.LandlockUsable() || !mcp.LimiterUsable() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemctlPath(), mcp.ScopeListArgs()...)
	// The session bus systemd-run and the teardown both need; the same
	// environment reaches the manager here.
	cmd.Env = mcp.LimiterEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		return // no systemd, no session bus, or nothing loaded -- all best-effort
	}

	units := mcp.ParseSandboxScopeUnits(string(out))
	orphans := selectOrphanScopes(units, os.Getpid(), processAlive)
	for _, name := range orphans {
		reapSandboxScope(name)
	}
	if len(orphans) > 0 {
		s.logger.Printf("sandbox-reap: killed %d orphaned scope(s) left by a force-killed daemon", len(orphans))
	}
}
