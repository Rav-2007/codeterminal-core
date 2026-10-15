package main

import (
	"fmt"
	"strings"

	"mochiii/protocol"
)

// ONE STATUS LINE PER SERVER, in the daemon's words, so the CLI, the terminal
// client and VS Code say the same thing about the same server -- the words
// Claude Code's `mcp list` uses, which a user coming from it already reads.
func mcpStatusLine(info protocol.MCPServerInfo) string {
	switch {
	case info.Approval == protocol.MCPApprovalPending:
		return "⏸ Pending approval"
	case info.Approval == protocol.MCPApprovalRejected:
		return "✘ Rejected"
	case info.Disabled:
		return "⊘ Disabled"
	case info.State == protocol.MCPStateNeedsAuth:
		return "! Needs authentication"
	case info.State == protocol.MCPStateFailed:
		line := "✘ Failed to connect"
		if info.StateDetail != "" {
			line += ": " + info.StateDetail
		}
		return line
	case len(info.EnvMissing) > 0 || len(info.HeadersMissing) > 0:
		var keys []string
		keys = append(keys, info.EnvMissing...)
		for _, h := range info.HeadersMissing {
			keys = append(keys, h+" header")
		}
		return "! Needs a key: " + strings.Join(keys, ", ")
	case info.State == protocol.MCPStateToolsFailed:
		return "! Connected · tools fetch failed"
	case info.State == protocol.MCPStateRunning && !info.ToolsListed:
		return "✔ Connected"
	case info.State == protocol.MCPStateRunning:
		return fmt.Sprintf("✔ Connected (%d tool%s)", info.ToolCount, map[bool]string{true: "", false: "s"}[info.ToolCount == 1])
	}
	return "○ Not started yet"
}
