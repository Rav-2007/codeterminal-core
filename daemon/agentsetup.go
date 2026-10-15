package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// maxSetupToolsPerServer bounds the tool names listed for one server as on
// the menu; maxSetupFindablePerServer, those listed as findable. Names only,
// a few tokens each: the schemas are what the menu cap exists to save.
const (
	maxSetupToolsPerServer    = 12
	maxSetupFindablePerServer = 40
)

// evidenceRule is said whenever a server is configured. FOUND 2026-10-15: told
// two of Supabase's 29 tools, the model said "there is no active project" and
// then -- having called nothing at all -- "there's no Supabase project ID in
// the daemon logs or config", while the account had projects and
// supabase__list_projects was one find_tools call away.
const evidenceRule = "\n- Before you tell the user that something does not exist, is not set up, or cannot be " +
	"found on one of these servers, look it up with that server's tool for it -- if the tool is not on your " +
	"list, add it with find_tools first. Say only what a tool result showed: a call that failed (an error, a " +
	"missing key, a refused permission, an unreachable server) is a failure to report as such, never evidence " +
	"that the thing is absent, and an empty result means only that this connection returned nothing. Ask the " +
	"user for an identifier only when no tool can look it up, or when several were found and you need to know " +
	"which one they mean -- never pick one of several yourself."

// setupTools is what the section says about each server's tools this turn:
// offered are on the model's menu, findable are left off it but can be added
// with find_tools.
type setupTools struct {
	offered  map[string][]string
	findable map[string][]string
}

// agentSetup is the section. servers are this turn's configured servers
// (serverInfos, read after the registry was built, so State is this turn's);
// tools are the third-party tools offered and findable; withheld is set in a
// mode that connects no third-party server; hints are project facts read from
// configuration (projectHints).
func agentSetup(version, model, base, workspace string, servers []protocol.MCPServerInfo, tools setupTools, withheld bool, hints ...string) string {
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
		case len(tools.offered[s.Name])+len(tools.findable[s.Name]) > 0:
			if s.URL != "" {
				fmt.Fprintf(&b, "a remote server at %s (a call SENDS its arguments there); ", remoteHost(s.URL))
			}
			offered, findable := tools.offered[s.Name], tools.findable[s.Name]
			if len(findable) == 0 {
				fmt.Fprintf(&b, "running; its tools: %s.", someOf(offered, maxSetupToolsPerServer))
				break
			}
			// ALL of its tools, not only the menu's. Listing only the offered
			// ones is what made the model tell the user Supabase had two.
			fmt.Fprintf(&b, "running, with %d tools.", len(offered)+len(findable))
			if len(offered) > 0 {
				fmt.Fprintf(&b, " On your tool list now: %s.", someOf(offered, maxSetupToolsPerServer))
			}
			fmt.Fprintf(&b, " Also available, not on your list until you call find_tools with what you need: %s.",
				someOf(findable, maxSetupFindablePerServer))
		default:
			b.WriteString("running, but it offers no tools you can call this turn.")
		}
	}
	for _, h := range hints {
		b.WriteString("\n- " + h)
	}
	b.WriteString(evidenceRule)
	return b.String()
}

// someOf is names joined, cut at max with a count of the rest.
func someOf(names []string, max int) string {
	if len(names) > max {
		return strings.Join(names[:max], ", ") + fmt.Sprintf(" and %d more", len(names)-max)
	}
	return strings.Join(names, ", ")
}

// A SUPABASE PROJECT REF is twenty lower-case letters or digits: the
// <ref> in https://supabase.com/dashboard/project/<ref> and
// https://<ref>.supabase.co. Checked before one is ever repeated to the model,
// so a malformed value -- or something that is not a ref at all, such as a key
// pasted into the wrong flag -- is never offered as one.
var supabaseRefPattern = regexp.MustCompile(`^[a-z0-9]{20}$`)

// maxProjectRefFileBytes bounds the read of supabase/.temp/project-ref,
// which holds one ref and a newline.
const maxProjectRefFileBytes = 64

// projectHints are the project facts this turn can state from configuration,
// so the model does not ask the user for what is already set:
//
//   - a server started with --project-ref is SCOPED to that one project (the
//     Supabase server then offers no account-level tools such as
//     list_projects), and its tools act on it;
//   - a folder linked with `supabase link` records the project in
//     supabase/.temp/project-ref.
//
// Only a valid ref is repeated. Nothing else from a server's arguments is: they
// can hold an --access-token.
func projectHints(workspace string, servers []protocol.MCPServerInfo) []string {
	var hints []string
	supabase := false
	for _, s := range servers {
		if s.Disabled || s.Approval == protocol.MCPApprovalPending || s.Approval == protocol.MCPApprovalRejected {
			continue
		}
		if strings.Contains(strings.ToLower(s.Name+" "+s.Program+" "+strings.Join(s.Args, " ")), "supabase") {
			supabase = true
		}
		ref, found := flagValue(s.Args, "--project-ref")
		switch {
		case !found:
		case supabaseRefPattern.MatchString(ref):
			hints = append(hints, fmt.Sprintf("%s is started with --project-ref %s: it is limited to that one "+
				"project, its tools act on it, and it cannot list the account's other projects.", s.Name, ref))
		default:
			hints = append(hints, fmt.Sprintf("%s is started with a --project-ref that is not a valid project "+
				"reference (twenty lower-case letters and digits), so do not use it; the user can fix it with "+
				"Change on the server in Customize > MCP.", s.Name))
		}
	}
	if supabase && workspace != "" {
		if ref, ok := linkedSupabaseRef(workspace); ok {
			hints = append(hints, fmt.Sprintf("This project folder is linked to Supabase project %s "+
				"(supabase/.temp/project-ref, written by `supabase link`). Use it when the user means "+
				"\"this project\" and has not named another.", ref))
		}
	}
	return hints
}

// flagValue finds --flag value or --flag=value in args.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// linkedSupabaseRef reads the ref `supabase link` wrote into the workspace,
// if it is there and valid. Only a small REGULAR file is read: Lstat, so a
// link planted in the workspace cannot point this at a device or a file
// outside it.
func linkedSupabaseRef(workspace string) (string, bool) {
	path := filepath.Join(workspace, "supabase", ".temp", "project-ref")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxProjectRefFileBytes {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	ref := strings.TrimSpace(string(data))
	return ref, supabaseRefPattern.MatchString(ref)
}

// withAgentSetup returns messages with the setup section added to the system
// message, for the servers and tools this turn really has.
func (s *Server) withAgentSetup(ctx context.Context, registry *mcp.Registry, mode, model string, messages []chatMessage) []chatMessage {
	cfg, _ := s.agentConfig()
	tools := setupTools{offered: map[string][]string{}, findable: map[string][]string{}}
	// THE SAME MENU THE LOOP WILL OFFER: ranked against the same request
	// (menuQuery), so "on your list" here is the list the model gets.
	offered, _ := registry.AdvertisedFor(ctx, menuQuery(messages))
	// The listing is also each server's health: how many tools it offered.
	s.mcpStates.listed(registry.Listed())
	for _, t := range offered {
		if t.Server != mcp.BuiltinServerName {
			tools.offered[t.Server] = append(tools.offered[t.Server], t.Name)
		}
	}
	// What the cap left off is findable -- the loop offers find_tools exactly
	// when there is any -- except what this turn's mode withholds anyway.
	for _, t := range registry.DroppedTools() {
		if t.Server != mcp.BuiltinServerName && !modeWithholds(mode, t) && !modeWithholdsLaneB(mode) {
			tools.findable[t.Server] = append(tools.findable[t.Server], t.Name)
		}
	}
	servers := s.serverInfos(cfg)
	_, base := s.credentials()
	return withSystemNote(messages, agentSetup(daemonVersion, model, base, s.workspace,
		servers, tools, modeWithholdsLaneB(mode), projectHints(s.workspace, servers)...))
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
