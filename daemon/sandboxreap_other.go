//go:build !linux

package main

// reapOrphanedSandboxScopes is a no-op off Linux: only the Landlock backend puts
// commands in named transient scopes, and that backend exists only on Linux, so
// there is never anything to reap. See sandboxreap_linux.go for the real pass.
func (s *Server) reapOrphanedSandboxScopes() {}
