package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
)

// `mcp list` -- the whole tool path, proved without the model -- and `mcp add`
// / `mcp remove`, which keep the user's own servers (mcpuser.go).
//
// It loads the real config, registers the real built-in tools, spawns the real
// configured MCP servers, asks each for its real tool list, and prints what
// WOULD be offered to the model along with the policy that would apply. No
// inference, no billing, no socket.
//
// That is deliberate. The tool surface is the part of agent mode that decides
// what may run on a user's machine, and until this existed the only way to see
// it was to start a turn and watch. A user should be able to answer "what have
// I actually authorised?" by asking, and an operator debugging a server that
// will not start should not have to spend a model call to find out why.
func runMCPCommand(args []string, logger *log.Logger) error {
	// The subcommand is taken BEFORE flag parsing, not after. Go's flag package
	// stops at the first non-flag argument, so `mcp list --config x` would
	// otherwise parse zero flags and silently use the defaults -- reporting on
	// a different config file than the one the user named, which is a worse
	// failure than an error.
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return runMCPList(args, logger)
	case "add":
		return runMCPAdd(args, logger)
	case "remove", "enable", "disable":
		return runMCPNamed(sub, args, logger)
	case "set-key":
		return runMCPSetKey(args, logger)
	case "get":
		return runMCPNamed(sub, args, logger)
	case "check":
		return runMCPCheck(args, logger)
	case "add-json":
		return runMCPAddJSON(args, logger)
	case "auth":
		return runMCPAuth(args, logger)
	case "sign-out":
		return runMCPNamed(protocol.MCPActionSignOut, args, logger)
	case "approve":
		return runMCPApprove(args, logger)
	case "reject":
		return runMCPNamed(protocol.MCPActionReject, args, logger)
	}
	return fmt.Errorf("unknown mcp subcommand %q (the subcommands are list, get, check, add, add-json, remove, "+
		"enable, disable and set-key)", sub)
}

// mcpUsage is the one usage text for every subcommand.
func mcpUsage() {
	fmt.Fprintf(os.Stderr, "usage:\n"+
		"  %[1]s mcp list   [--config models.json] [--workspace .]\n"+
		"  %[1]s mcp add    <name> [--env NAME | --env NAME=value]... [--in-project] [--acknowledge-unconfined] -- <command> [args...]\n"+
		"  %[1]s mcp add    <name> --transport http|sse [--header \"Name: value\"]... <url>\n"+
		"  %[1]s mcp set-key <name> <VARIABLE> [--forget] [--header]   (the value is read from stdin, not the command line)\n"+
		"  %[1]s mcp add-json <name> '<json>'   (one server's entry from another MCP client's \"mcpServers\")\n"+
		"  %[1]s mcp get    <name>\n"+
		"  %[1]s mcp check  [<name>] [--timeout seconds]\n"+
		"  %[1]s mcp auth   <name>      sign in to a remote server in your browser\n"+
		"  %[1]s mcp sign-out <name>\n"+
		"  %[1]s mcp approve <name> | reject <name>   a server this project's .mcp.json proposes\n"+
		"  %[1]s mcp remove <name>\n"+
		"  %[1]s mcp disable <name>\n"+
		"  %[1]s mcp enable <name>\n\n"+
		"list shows every tool agent mode would offer the model, with the policy that applies to it.\n"+
		"It starts the configured MCP servers to ask them, and shuts them down again.\n\n"+
		"add stores an MCP server of your own in ~/.mochiii/%[2]s; remove drops one; disable\n"+
		"pauses one without forgetting it, and enable resumes it. The server is\n"+
		"started in a private folder of its own unless --in-project is given. A running\n"+
		"daemon picks the change up at the next question. In the terminal client the same\n"+
		"commands are /mcp-server, /mcp-server add, remove, disable and enable.\n", os.Args[0], userMCPFileName)
}

func runMCPList(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	workspace := fs.String("workspace", ".", "workspace root to ground built-in tools against")
	fs.Usage = mcpUsage

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	// Same resolution as the daemon itself, so `mcp list` reports on the config
	// the daemon would actually load rather than on whatever the CWD happens to
	// hold. Resolved before use so the "agent mode is OFF" line below can name
	// the real file.
	//
	// "WOULD LOAD" IS NOT "DID LOAD". A daemon already running was started with
	// whatever --config it was given, which this command cannot know -- so the
	// terminal client no longer asks it; /mcp-server asks the running daemon
	// (mcp_handler.go). This remains the way to check a config with no daemon up.
	resolvedConfigPath := *configPath
	if resolvedConfigPath == "" {
		resolvedConfigPath = resolveConfigPath()
	}
	cfg, err := LoadConfig(resolvedConfigPath)
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings() {
		logger.Printf("config warning: %s", w)
	}

	// Same resolver every other workspace-touching path uses, so the built-in
	// tools are confined against the identical root the edit pipeline would.
	root, err := editapply.ResolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return fmt.Errorf("resolving workspace %s: %w", *workspace, err)
	}

	// A Server with just enough state for the built-in tools to close over.
	// search_code will report retrieval as unavailable here, which is honest:
	// this command does not start the embedder.
	srv := &Server{
		cfg: cfg, workspace: root, logger: logger,
		serverExtras: serverExtras{
			configPath: absoluteOrAsGiven(resolvedConfigPath), userServersPath: userServersPathOrNone(logger),
			mcpRunDir: mcpRunDirOrNone(logger), mcpSecrets: mcpSecretStoreOrNone(logger),
		},
	}
	// The servers the user added count, exactly as they do for a running
	// daemon (mcpuser.go).
	effective, problems := srv.agentConfig()
	for _, problem := range problems {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", problem)
	}

	if !effective.MCP.Enabled {
		fmt.Print(agentModeOffReport(resolvedConfigPath))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), listDeadline(effective))
	defer cancel()
	registry, connectErrs := srv.buildRegistry(ctx, logger, &proposalSink{}, "")
	defer func() {
		if err := registry.Close(); err != nil {
			logger.Printf("shutting down MCP servers: %v", err)
		}
	}()
	// Each server's health first, as `claude mcp list` gives it, then the tools.
	_, _ = registry.Advertised(ctx)
	srv.mcpStates.listed(registry.Listed())
	writeServerStatus(os.Stdout, srv.serverInfos(effective))
	return writeToolReport(ctx, os.Stdout, os.Stderr, effective, resolvedConfigPath, registry, connectErrs)
}

// writeServerStatus prints one status line per configured server.
func writeServerStatus(w io.Writer, infos []protocol.MCPServerInfo) {
	if len(infos) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "MCP servers:")
	listed := false
	for _, info := range infos {
		_, _ = fmt.Fprintf(w, "  %s: %s - %s\n", info.Name, info.Command, info.StatusLine)
		listed = listed || (!info.Disabled && info.State == protocol.MCPStateRunning && info.ToolsListed && info.ToolCount > 0)
	}
	// The count above is each server's whole list, not what a turn offers: the menu
	// has a cap, the daemon's own tools come first, and the places left depend on
	// the mode and the other servers, so no number of places is printed. The
	// extension's server view says the same (FIND_TOOLS_HINT, mcpServersView.ts).
	if listed {
		_, _ = fmt.Fprintln(w, "  "+findToolsHint)
	}
	_, _ = fmt.Fprintln(w)
}

// findToolsHint is said under the servers whenever one has listed its tools.
const findToolsHint = "A turn's tool list may not hold all of a server's tools at once; the model adds the ones it needs with find_tools."

// agentModeOffReport is what a list says when there are no tools to list.
func agentModeOffReport(configPath string) string {
	return "Agent mode is OFF (mcp.enabled is not true in " + configPath + ").\n" +
		"No tools would be offered and no servers would be started.\n"
}

// writeToolReport prints what agent mode would offer the model: every tool,
// its lane, whether it is confined and the policy that applies, then the plain
// statements about the unconfined ones. Warnings about servers that did not
// answer go to warn.
//
// ONE FUNCTION FOR THE COMMAND AND THE SOCKET. `mcp list` prints this for a
// config, and the running daemon sends the same text for itself
// (mcp_handler.go) -- two copies of this table would be two descriptions of
// what may run on a user's machine, free to disagree.
func writeToolReport(ctx context.Context, out, warn io.Writer, cfg *Config, configPath string, registry *mcp.Registry, connectErrs []error) error {
	tools, listErrs := registry.Advertised(ctx)

	// Name the file, exactly as the OFF line does. `mcp list` reads a config
	// resolved next to the daemon binary (or --config); a daemon that was
	// started separately -- the editor ADOPTS one a terminal left running --
	// may have loaded a different file, and then this report is about a config
	// that is not the one answering prompts. Printing the path is what lets a
	// reader notice that. The running daemon's own report names its file too.
	_, _ = fmt.Fprintf(out, "Agent mode is ON (mcp.enabled is true in %s). %d tool(s) would be offered to the model.\n\n",
		configPath, len(tools))

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	// Writes to a tabwriter: nothing here can recover from a failed write, and
	// a half-printed table is already visible to the reader.
	_, _ = fmt.Fprintln(w, "TOOL\tLANE\tCONFINED\tPOLICY\tDESCRIPTION")
	for _, tool := range tools {
		_, policy, err := registry.Lookup(ctx, tool.QualifiedName())
		if err != nil {
			policy = mcp.PolicyDeny
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			tool.QualifiedName(), tool.Lane, confinedLabel(tool), policy,
			firstLine(tool.Description))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if dropped := registry.Dropped(); len(dropped) > 0 {
		_, _ = fmt.Fprintf(out, "\n%d tool(s) were NOT offered because mcp.budget.max_advertised_tools is %d:\n  %s\n",
			len(dropped), cfg.MCP.Budget.resolvedMaxAdvertisedTools(), strings.Join(dropped, "\n  "))
		_, _ = fmt.Fprintln(out, "Every tool on the menu costs tokens on every request, and a wider menu made the model choose "+
			"worse on a borderline request (docs/TOOL_MENU_SIZE_2026-08-01.md). Raise the number in the config to offer these.")
	}

	// The unconfined half, said plainly and once, rather than left implicit in
	// a column the reader may skim.
	//
	// TWO COUNTS, NOT ONE, because "not confined" stopped meaning one thing.
	// It used to be synonymous with "an external subprocess", so a single
	// sentence about subprocesses covered every tool the column marked NO. A
	// network tool is also not confined and is not a subprocess at all -- it
	// spawns nothing and touches nothing local -- so folding it into that
	// sentence would print something plainly false about web_search, in the
	// alarming direction. Overstating a risk is not the safe error: it is how a
	// warning stops being read, and the sentence about real subprocesses is one
	// that needs to keep working.
	if external := countExternal(tools); external > 0 {
		_, _ = fmt.Fprintf(out, "\n%d of these run in external MCP servers, which this product CANNOT confine:\n"+
			"they are subprocesses with your full privileges, and the edit pipeline's gates\n"+
			"constrain only this daemon's own writer. Consent and the audit log are the protection.\n",
			external)
	}
	// A THIRD COUNT, because the sentence above was being said about tools it
	// is not true of. Seen in the real app (2026-10-11): a daemon with NO
	// external server configured reported "3 of these run in external MCP
	// servers". The three were this daemon's own -- the two that query a
	// language server, and the AST edit -- which are marked NO because they
	// can start another program, and were counted with the population the
	// column used to mean. They get the sentence that is true of them.
	if own := countOwnUnconfined(tools); own > 0 {
		_, _ = fmt.Fprintf(out, "\n%d of this daemon's own tools are marked NO as well: they can start or run another\n"+
			"program (a language server, or a command where no sandbox is available), which is not\n"+
			"confined the way its file reads and edit proposals are. The approval prompt says when a call would.\n",
			own)
	}
	if networked := countNetworked(tools); networked > 0 {
		_, _ = fmt.Fprintf(out, "\n%d of these reach the open internet. They run no program and touch no file here;\n"+
			"what they do is send text the model chose to a third party and bring a reply back.\n"+
			"Secrets are stripped outbound and replies are treated as untrusted data, never instructions.\n",
			networked)
	}

	for _, err := range append(connectErrs, listErrs...) {
		_, _ = fmt.Fprintf(warn, "\nWARNING: %v\n", err)
		if errors.Is(err, mcp.ErrServerUnavailable) {
			_, _ = fmt.Fprintln(warn, "Its tools are missing from the list above. Agent mode would still run, "+
				"reporting this as a degraded subsystem.")
		}
	}
	return nil
}

// envNames collects repeated --env flags.
type envNames []string

func (e *envNames) String() string     { return strings.Join(*e, ",") }
func (e *envNames) Set(v string) error { *e = append(*e, v); return nil }

// mcpIn and mcpOut are where `mcp add` reads its confirmation and writes its
// answers; variables so a test can stand in for the terminal.
var (
	mcpIn  io.Reader = os.Stdin
	mcpOut io.Writer = os.Stdout
)

// runMCPAdd is `mcp add`: store a server of the user's own, with no daemon
// needed. A daemon that is running reads the file at its next agent turn.
//
// IT GOES THROUGH THE SAME CODE AS THE SOCKET (Server.mcpAdd), on a Server with
// just enough state for it -- one set of rules for what may be added, one
// warning, one trial start -- so the command and /mcp-server add cannot drift.
func runMCPAdd(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp add", flag.ExitOnError)
	var env envNames
	fs.Var(&env, "env", "a variable the server reads (repeatable): NAME to pass it through from the environment, "+
		"or NAME=value to keep a value for this server in ~/.mochiii/mcp-secrets.json (owner-only)")
	ack := fs.Bool("acknowledge-unconfined", false, "confirm, without being asked, that the server runs with your full privileges")
	inProject := fs.Bool("in-project", false, "start the server inside the project being worked on, instead of a private folder of its own")
	transport := fs.String("transport", "", "http or sse for a remote server reached at a URL; leave it out for a program")
	var headers envNames
	fs.Var(&headers, "header", "a header for a remote server, \"Name: value\" (repeatable); a value is kept as a key, "+
		"a ${VAR} in it is kept as written")
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage

	// <name> comes before the flags, like the subcommand before it.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp add needs a name for the server")
	}
	name, args := args[0], args[1:]
	if err := fs.Parse(args); err != nil {
		return err
	}
	// flag stops at "--", leaving the command and its arguments.
	rest := fs.Args()
	if len(rest) == 0 {
		mcpUsage()
		if *transport != "" {
			return fmt.Errorf("mcp add --transport %s needs the server's URL", *transport)
		}
		return fmt.Errorf("mcp add needs the command to run, after --")
	}
	headerMap, err := parseHeaderFlags(headers)
	if err != nil {
		return err
	}

	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	names, values := splitEnvFlags(env)
	req := protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionAdd, Name: name,
		Command: rest[0], Args: rest[1:], Env: names, EnvValues: values, AcknowledgedUnconfined: *ack,
	}
	if *transport != "" {
		if len(rest) != 1 || len(names) > 0 || *inProject {
			return fmt.Errorf("a remote server takes its URL and --header only")
		}
		req.Command, req.Args, req.Transport, req.URL, req.Headers = "", nil, *transport, rest[0], headerMap
	} else if len(headerMap) > 0 {
		return fmt.Errorf("--header is for a remote server (--transport http <url>)")
	}
	if *inProject {
		req.Cwd = protocol.MCPCwdWorkspace
	}
	ctx := context.Background()
	resp := srv.mcpResult(ctx, req)
	if resp.NeedsAcknowledgement {
		// THE WORD IS TYPED, not implied by having run the command: the same
		// gate models.json's acknowledged_unconfined is, at the moment it applies.
		_, _ = fmt.Fprintf(mcpOut, "%s\n\nType the server's name (%s) to add it, or anything else to cancel: ", resp.Detail, name)
		typed, _ := bufio.NewReader(mcpIn).ReadString('\n')
		if strings.TrimSpace(typed) != name {
			_, _ = fmt.Fprintln(mcpOut, "Cancelled; nothing was added.")
			return nil
		}
		req.AcknowledgedUnconfined = true
		resp = srv.mcpResult(ctx, req)
	}
	return printMCPResponse(resp)
}

// runMCPNamed is `mcp remove|enable|disable <name>`: one server, by name. The
// subcommand is the protocol action's own word.
func runMCPNamed(action string, args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp "+action, flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp %s needs the name of the server", action)
	}
	name, args := args[0], args[1:]
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	return printMCPResponse(srv.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: action, Name: name,
	}))
}

// mcpAdminServer is a Server with just enough state to add or remove a server:
// the config that says which names are taken and whether agent mode is on, and
// where the user's servers are kept.
func mcpAdminServer(configPath string, logger *log.Logger) (*Server, error) {
	if configPath == "" {
		configPath = resolveConfigPath()
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	path, err := defaultUserMCPPath()
	if err != nil {
		return nil, err
	}
	// The trial start builds the menu a turn would, built-ins included, and
	// those are grounded against a workspace. Best-effort: an unresolvable
	// working directory costs the built-ins their root, not the command.
	root, _ := editapply.ResolveRealWorkspaceRoot(".")
	return &Server{
		cfg: cfg, workspace: root, logger: logger,
		serverExtras: serverExtras{
			configPath: absoluteOrAsGiven(configPath), userServersPath: path, mcpRunDir: mcpRunDirOrNone(logger),
			mcpSecrets: mcpSecretStoreOrNone(logger),
		},
	}, nil
}

// printMCPResponse writes an add's or a remove's answer, and turns a refusal
// into the command's error.
func printMCPResponse(resp protocol.MCPResponse) error {
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	_, _ = fmt.Fprintln(mcpOut, resp.Detail)
	for _, note := range resp.Notes {
		_, _ = fmt.Fprintf(mcpOut, "\nNOTE: %s\n", note)
	}
	return nil
}

// confinedLabel names the honest answer for one tool.
//
// THREE VALUES, NOT TWO. A bare "NO" told the reader that web_search sits in
// the same category as an unconfined third-party subprocess, which is the
// column's whole purpose and is wrong here in the direction that matters: the
// reader's next action after seeing NO is to think about what the tool can do
// to their machine, and the answer for this one is "nothing".
func confinedLabel(tool mcp.Tool) string {
	switch {
	case tool.ReachesNetwork:
		return "network"
	case tool.Confined:
		return "yes"
	default:
		return "NO"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 60
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// countOwnUnconfined counts this daemon's own tools that are not confined and
// not network tools: the ones that can start or run another program.
func countOwnUnconfined(tools []mcp.Tool) int {
	n := 0
	for _, tool := range tools {
		if !tool.Confined && !tool.ReachesNetwork && tool.Lane == protocol.LaneFirstParty {
			n++
		}
	}
	return n
}

// countExternal counts the tools the subprocess warning is about: the ones
// running in a third-party MCP server.
//
// IT KEYS ON THE LANE, NOT ON CONFINEMENT. "Unconfined" was read as "somebody
// else's subprocess" here, but the two parted company exactly as they did for
// the network tools above: a FIRST-PARTY built-in can be unconfined too --
// propose_ast_edit and the compiler queries all report Confined=false -- and
// none of those runs in an external server. Counting by `!Confined` therefore
// told the user "N of these run in external MCP servers" with N inflated by
// this daemon's own tools, which is the alarming direction and plainly false.
// The lane is the only field that answers the question the sentence asks.
func countExternal(tools []mcp.Tool) int {
	n := 0
	for _, tool := range tools {
		if tool.Lane == protocol.LaneThirdParty {
			n++
		}
	}
	return n
}

// countNetworked counts tools that are unconfined because they leave the
// machine.
func countNetworked(tools []mcp.Tool) int {
	n := 0
	for _, tool := range tools {
		if tool.ReachesNetwork {
			n++
		}
	}
	return n
}

// splitEnvFlags splits --env flags into the names a server reads and the values
// given for some of them (NAME=value), as Claude Code's --env takes them.
func splitEnvFlags(flags []string) (names []string, values map[string]string) {
	for _, f := range flags {
		name, value, hasValue := strings.Cut(f, "=")
		name = strings.TrimSpace(name)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
		if hasValue {
			if values == nil {
				values = map[string]string{}
			}
			values[name] = value
		}
	}
	return names, values
}

// runMCPSetKey is `mcp set-key <name> <VARIABLE>`: keep a value for one server.
//
// THE VALUE IS NEVER AN ARGUMENT. An argument is in the shell's history and in
// every process listing while the command runs. It is read from stdin -- typed
// with echo off at a terminal, or piped (`pass show x | mochiii-daemon mcp
// set-key ...`).
func runMCPSetKey(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp set-key", flag.ExitOnError)
	forget := fs.Bool("forget", false, "forget the stored value instead of setting one")
	header := fs.Bool("header", false, "the name is a remote server's header (Authorization), not a variable")
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	// Names and flags in any order: `set-key github --header Authorization`
	// and `set-key github Authorization --header` both read naturally.
	var names, flagArgs []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--config" || a == "-config":
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				flagArgs, i = append(flagArgs, args[i+1]), i+1
			}
		case strings.HasPrefix(a, "-"):
			flagArgs = append(flagArgs, a)
		default:
			names = append(names, a)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(names) != 2 {
		mcpUsage()
		return fmt.Errorf("mcp set-key takes the server's name and the variable's (or, with --header, the header's) " +
			"name; the value is read from stdin, never the command line")
	}
	name, variable := names[0], names[1]
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	value := ""
	if !*forget {
		if value, err = readSecretLine(mcpIn, mcpOut, fmt.Sprintf("Value for %s (%s), not shown: ", variable, name)); err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("no value was given; nothing was stored (use --forget to remove one)")
		}
	}
	req := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionSetEnv, Name: name, EnvName: variable, EnvValue: value}
	if *header {
		req.EnvName, req.HeaderName = "", variable
	}
	return printMCPResponse(srv.mcpResult(context.Background(), req))
}

// parseHeaderFlags reads repeated --header "Name: value" flags. The error names
// the header, never its value.
func parseHeaderFlags(flags []string) (map[string]string, error) {
	var out map[string]string
	for _, f := range flags {
		name, value, ok := strings.Cut(f, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("--header takes \"Name: value\"")
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = strings.TrimSpace(value)
	}
	return out, nil
}

// runMCPCheck is `mcp check [<name>] [--timeout N]`: start the server (or all of
// them) now and say how each fared.
func runMCPCheck(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp check", flag.ExitOnError)
	timeout := fs.Int("timeout", 0, fmt.Sprintf("seconds to wait for a server's first start, at most %d", maxConnectTimeoutSeconds))
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	return printMCPResponse(srv.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionCheck, Name: name, ConnectTimeoutSeconds: *timeout,
	}))
}

// runMCPAddJSON is `mcp add-json <name> '<json>'`, add's two steps from an entry
// written for another MCP client. Its env values are kept as keys, not in the
// server list (mcpjson.go).
func runMCPAddJSON(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp add-json", flag.ExitOnError)
	ack := fs.Bool("acknowledge-unconfined", false, "confirm, without being asked, that the server runs with your full privileges")
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	if len(args) < 2 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp add-json needs a name and the JSON")
	}
	name, entry := args[0], args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	req := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAddJSON, Name: name, JSON: []byte(entry), AcknowledgedUnconfined: *ack}
	ctx := context.Background()
	resp := srv.mcpResult(ctx, req)
	if resp.NeedsAcknowledgement {
		_, _ = fmt.Fprintf(mcpOut, "%s\n\nType the server's name (%s) to add it, or anything else to cancel: ", resp.Detail, name)
		typed, _ := bufio.NewReader(mcpIn).ReadString('\n')
		if strings.TrimSpace(typed) != name {
			_, _ = fmt.Fprintln(mcpOut, "Cancelled; nothing was added.")
			return nil
		}
		req.AcknowledgedUnconfined = true
		resp = srv.mcpResult(ctx, req)
	}
	return printMCPResponse(resp)
}

// runMCPAuth is `mcp auth <name>`: sign in to a remote server. The address is
// printed for the user to open -- this command opens nothing itself -- and the
// browser comes back to this process, which keeps the token in the key file
// that every daemon reads.
func runMCPAuth(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp auth", flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp auth needs the server's name")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	ctx := context.Background()
	begun := srv.mcpResult(ctx, protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthStart, Name: name})
	if begun.Error != "" {
		return errors.New(begun.Error)
	}
	if begun.AuthState != protocol.MCPAuthPending {
		return printMCPResponse(begun)
	}
	_, _ = fmt.Fprintf(mcpOut, "Open this address in your browser to sign in to %s:\n\n  %s\n\nWaiting (up to %d minutes)…\n",
		name, begun.AuthURL, begun.ExpiresInSeconds/60)
	for {
		resp := srv.mcpResult(ctx, protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAuthWait, AuthID: begun.AuthID})
		if resp.AuthState != protocol.MCPAuthPending {
			return printMCPResponse(resp)
		}
	}
}

// runMCPApprove is `mcp approve <name>`: the warning for a server the
// project's .mcp.json proposes, then its name typed, then the approval -- of
// the definition the warning described (its digest), and no other.
func runMCPApprove(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp approve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp approve needs the server's name")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	ctx := context.Background()
	req := protocol.MCPRequest{MCP: true, Action: protocol.MCPActionApprove, Name: name}
	first := srv.mcpResult(ctx, req)
	if !first.NeedsAcknowledgement {
		return printMCPResponse(first)
	}
	_, _ = fmt.Fprintf(mcpOut, "%s\n\nType the server's name (%s) to approve it, or anything else to cancel: ", first.Detail, name)
	typed, _ := bufio.NewReader(mcpIn).ReadString('\n')
	if strings.TrimSpace(typed) != name {
		_, _ = fmt.Fprintln(mcpOut, "Cancelled; it stays pending.")
		return nil
	}
	req.AcknowledgedUnconfined, req.Digest = true, first.Digest
	return printMCPResponse(srv.mcpResult(ctx, req))
}
