// Socket-response error hygiene — FAIL-3 Gate 7. scrubPaths / socketSafeError
// replace daemon-side absolute workspace paths with a stable "<workspace>" token
// before an error string crosses the socket, so absolute host paths don't ride
// along in a response that could travel off-box. Scrubs only what crosses the
// socket; every caller still logs the raw error locally. See BACKLOG.md, "Gate 7
// deep audit + path-scrub / upstream-passthrough FIX" (commit 3aeb8b6).

package main

import (
	"errors"
	"os"
	"strings"
)

// workspacePathToken is what socketSafeError substitutes for a daemon-side
// absolute workspace path in an error message bound for a socket client
// (FAIL-3, Gate 7 — response hygiene). It is deliberately human-readable and
// stable so a scrubbed message stays debuggable.
const workspacePathToken = "<workspace>"

// scrubPaths replaces every occurrence of a known daemon-side workspace root in
// msg with workspacePathToken (FAIL-3, Gate 7 — response hygiene). It strips
// the caller-supplied roots (typically the resolved, symlink-followed root) and
// always also strips s.workspace (the configured root, used for the paths in
// errors that fire BEFORE resolution succeeds). The workspace-RELATIVE tail is
// preserved — "<workspace>/sub/x.go: no such file or directory" stays as
// diagnostic as the absolute form was, minus the leaky host prefix (home-dir
// layout, username, machine structure) that could ride along in a response
// later saved to a transcript, pasted into a bug report, or shipped as
// telemetry. This scrubs only what crosses the socket: the editapply core, the
// CLI/TUI, and the daemon's own local log (every caller logs the raw error
// first) all keep the full absolute path, and the confinement/resolution logic
// that produces these errors is untouched.
func (s *Server) scrubPaths(msg string, roots ...string) string {
	for _, root := range roots {
		if root != "" {
			msg = strings.ReplaceAll(msg, root, workspacePathToken)
		}
	}
	if s.workspace != "" {
		msg = strings.ReplaceAll(msg, s.workspace, workspacePathToken)
	}
	return msg
}

// socketSafeError is scrubPaths over err.Error() — the form used at every error
// encode in the Apply/Undo handlers, mirroring how the prompt path already
// rewrites ErrZDRRefused to a generic string before sending it over the socket.
// It also unifies existence and permission errors into a single shape to close
// the Gate 7 existence oracle (Item 7).
func (s *Server) socketSafeError(err error, roots ...string) string {
	msg := err.Error()

	// Unify existence and permission errors to close the Gate 7 oracle,
	// but leave backup-related errors intact (they are distinct operational errors).
	isOracleLeak := strings.Contains(msg, "does not exist; to create it") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "no such file or directory")

	if isOracleLeak && !strings.Contains(msg, "backup") {
		// Include the scrubbed path if we can extract it from os.PathError,
		// otherwise just return a generic string.
		pathStr := "file"
		var pe *os.PathError
		if errors.As(err, &pe) {
			pathStr = s.scrubPaths(pe.Path, roots...)
		} else if strings.Contains(msg, " does not exist; to create it") {
			// Extract filename from editapply's custom message: "foo.go does not exist; to create it..."
			parts := strings.SplitN(msg, " does not exist", 2)
			if len(parts) == 2 {
				pathStr = s.scrubPaths(parts[0], roots...)
			}
		}
		return pathStr + ": access denied or does not exist"
	}

	return s.scrubPaths(msg, roots...)
}
