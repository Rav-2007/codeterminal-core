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
	case "remove":
		return runMCPRemove(args, logger)
	}
	return fmt.Errorf("unknown mcp subcommand %q (the subcommands are \"list\", \"add\" and \"remove\")", sub)
}

// mcpUsage is the one usage text for every subcommand.
func mcpUsage() {
	fmt.Fprintf(os.Stderr, "usage:\n"+
		"  %[1]s mcp list   [--config models.json] [--workspace .]\n"+
		"  %[1]s mcp add    <name> [--env NAME]... [--acknowledge-unconfined] -- <command> [args...]\n"+
		"  %[1]s mcp remove <name>\n\n"+
		"list shows every tool agent mode would offer the model, with the policy that applies to it.\n"+
		"It starts the configured MCP servers to ask them, and shuts them down again.\n\n"+
		"add stores an MCP server of your own in ~/.mochiii/%[2]s; remove drops one. A running\n"+
		"daemon picks the change up at the next question. In the terminal client the same\n"+
		"commands are /mcp-server, /mcp-server add and /mcp-server remove.\n", os.Args[0], userMCPFileName)
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
		serverExtras: serverExtras{configPath: absoluteOrAsGiven(resolvedConfigPath), userServersPath: userServersPathOrNone(logger)},
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

	ctx := context.Background()
	registry, connectErrs := srv.buildRegistry(ctx, logger, &proposalSink{}, "")
	defer func() {
		if err := registry.Close(); err != nil {
			logger.Printf("shutting down MCP servers: %v", err)
		}
	}()
	return writeToolReport(ctx, os.Stdout, os.Stderr, effective, resolvedConfigPath, registry, connectErrs)
}

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
	fs.Var(&env, "env", "NAME of an environment variable to pass through to the server (repeatable; a name, never a value)")
	ack := fs.Bool("acknowledge-unconfined", false, "confirm, without being asked, that the server runs with your full privileges")
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
		return fmt.Errorf("mcp add needs the command to run, after --")
	}

	srv, err := mcpAdminServer(*configPath, logger)
	if err != nil {
		return err
	}
	req := protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionAdd, Name: name,
		Command: rest[0], Args: rest[1:], Env: env, AcknowledgedUnconfined: *ack,
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

// runMCPRemove is `mcp remove <name>`.
func runMCPRemove(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("mcp remove", flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	fs.Usage = mcpUsage
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		mcpUsage()
		return fmt.Errorf("mcp remove needs the name of the server")
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
		MCP: true, Action: protocol.MCPActionRemove, Name: name,
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
		serverExtras: serverExtras{configPath: absoluteOrAsGiven(configPath), userServersPath: path},
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
