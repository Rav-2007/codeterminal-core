package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// WHAT MOCHIII ITSELF IS SET UP WITH, SAID TO THE AGENT.
//
// FOUND 2026-10-14, by a user asking it "which mcp server have you connected".
// It answered "no external tools are connected -- the only lane active on this
// project is the builtin lane", while Customize listed two servers as Active.
// Shown a screenshot of that page it agreed, and then told the user it "has no
// access to Mochiii's own configuration". All three answers came from the same
// omission machinereach.go was written for: nothing told the model about the
// setup it runs in, so it guessed -- and borrowed the one internal word it
// could see ("builtin", from its own tool names) to describe it.
//
// The truth was worse than either answer: both servers were failing on every
// start for want of a key, and the only place that said so was the daemon log.
//
// So the setup is stated once per turn, built from what THIS turn really did:
// the servers it started and the tools they offered, the ones that failed and
// the reason each gave, the ones paused. Like the reach section, it ends with
// the instruction to answer from it.

const agentSetupHeader = "ABOUT THIS MOCHIII -- the setup the user sees in Mochiii's own pages " +
	"(Account, Customize, Settings), as it is for this turn. When the user asks about Mochiii, its " +
	"version or model, or which MCP servers or tools you have, answer from this list. Never call " +
	"your tools \"builtin\" or a \"lane\", and never show their internal names (builtin__read_file): " +
	"say what they do."

// maxSetupToolsPerServer bounds the tool names listed for one server.
const maxSetupToolsPerServer = 12

// agentSetup is the section. servers are this turn's configured servers
// (serverInfos, read after the registry was built, so State is this turn's);
// tools are the third-party tools offered; withheld is set in a mode that
// connects no third-party server.
func agentSetup(version, model, base, workspace string, servers []protocol.MCPServerInfo, tools map[string][]string, withheld bool) string {
	var b strings.Builder
	b.WriteString(agentSetupHeader)

	fmt.Fprintf(&b, "\n- Mochiii version %s.", version)
	if model != "" {
		via := ""
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			via = " through " + u.Host
		}
		fmt.Fprintf(&b, " Model: %s%s.", model, via)
	}
	if workspace != "" {
		fmt.Fprintf(&b, "\n- The project folder is %s.", workspace)
	}

	if len(servers) == 0 {
		b.WriteString("\n- MCP servers: none added. The user adds them in Customize > MCP (its Marketplace, " +
			"or Add Server).")
		return b.String()
	}
	b.WriteString("\n- MCP servers the user added (Customize > MCP):")
	for _, s := range servers {
		fmt.Fprintf(&b, "\n  - %s: ", s.Name)
		switch {
		case s.Approval == protocol.MCPApprovalPending:
			b.WriteString("PROPOSED by this project's .mcp.json and not approved, so it is not started. Only the " +
				"user can approve it (Approve on it in Customize > MCP, or `mcp approve`); you cannot.")
		case s.Approval == protocol.MCPApprovalRejected:
			b.WriteString("proposed by this project and rejected by the user, so it is not started.")
		case s.Disabled:
			b.WriteString("paused by the user, so its tools are not offered.")
		case withheld:
			b.WriteString("not started in this mode, so its tools are not offered this turn.")
		case s.State == protocol.MCPStateNeedsAuth:
			b.WriteString("NEEDS THE USER TO SIGN IN, so none of its tools are available. Only the user can: with " +
				"Sign in on the server in Customize > MCP, which opens their browser. Tell them so.")
		case s.State == protocol.MCPStateFailed:
			fmt.Fprintf(&b, "FAILED TO START this turn, so none of its tools are available. It said: %q.", s.StateDetail)
			if len(s.EnvMissing) > 0 {
				fmt.Fprintf(&b, " It has no value for %s: the user sets it with Set key on the server in "+
					"Customize > MCP.", strings.Join(s.EnvMissing, ", "))
			}
		case len(s.HeadersMissing) > 0 && s.State != protocol.MCPStateRunning:
			fmt.Fprintf(&b, "has no value for its %s header, so it will likely refuse every call; the user sets it "+
				"with Set key on the server in Customize > MCP.", strings.Join(s.HeadersMissing, ", "))
		case len(tools[s.Name]) > 0:
			names := tools[s.Name]
			if s.URL != "" {
				fmt.Fprintf(&b, "a remote server at %s (a call SENDS its arguments there); ", remoteHost(s.URL))
			}
			more := ""
			if len(names) > maxSetupToolsPerServer {
				more = fmt.Sprintf(" and %d more", len(names)-maxSetupToolsPerServer)
				names = names[:maxSetupToolsPerServer]
			}
			fmt.Fprintf(&b, "running; its tools: %s%s.", strings.Join(names, ", "), more)
		default:
			b.WriteString("running, but it offers no tools you can call this turn.")
		}
	}
	return b.String()
}

// withAgentSetup returns messages with the setup section added to the system
// message, for the servers and tools this turn really has.
func (s *Server) withAgentSetup(ctx context.Context, registry *mcp.Registry, mode, model string, messages []chatMessage) []chatMessage {
	cfg, _ := s.agentConfig()
	tools := map[string][]string{}
	offered, _ := registry.Advertised(ctx)
	// The listing is also each server's health: how many tools it offered.
	s.mcpStates.listed(registry.Listed())
	for _, t := range offered {
		if t.Server != mcp.BuiltinServerName {
			tools[t.Server] = append(tools[t.Server], t.Name)
		}
	}
	_, base := s.credentials()
	return withSystemNote(messages, agentSetup(daemonVersion, model, base, s.workspace,
		s.serverInfos(cfg), tools, modeWithholdsLaneB(mode)))
}

// unavailableServerDetail is the notice for one server that would not start:
// which one, the reason it gave, and -- when it is missing a key -- where the
// key goes. It used to be the same sentence for every server, naming none.
func (s *Server) unavailableServerDetail(err error) string {
	cfg, _ := s.agentConfig()
	for _, info := range s.serverInfos(cfg) {
		if info.State != protocol.MCPStateFailed || !strings.HasPrefix(err.Error(), "server "+info.Name+":") {
			continue
		}
		detail := fmt.Sprintf("MCP server %s did not start (%s), so its tools are missing from this turn.", info.Name, info.StateDetail)
		if len(info.EnvMissing) > 0 {
			detail += fmt.Sprintf(" It needs %s: set it with Set key on %s in Customize > MCP.", strings.Join(info.EnvMissing, ", "), info.Name)
		}
		return detail
	}
	return "a configured MCP server is unavailable, so the tools it provides are " +
		"missing from this turn. The answer may decline to do something you know it can do."
}
