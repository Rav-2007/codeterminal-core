package tui

import "mochiii/protocol"

// runCommandVerb is how run_command's question begins: where THIS call runs.
// Inside the project that is the turn's working copy, which has the agent's
// edits (the daemon's runplace.go). git, a package installer, and every
// command of a turn with no copy run in the real project, as the user's own
// terminal would -- the daemon says so with ToolApprovalRequest.InPlace, which
// also puts inPlaceRisk under the question. Kept word for word with
// clients/vscode/media/main.js.
func runCommandVerb(req protocol.ToolApprovalRequest) string {
	if req.InPlace {
		return "Run in your terminal"
	}
	return "Run in this turn's working copy"
}
