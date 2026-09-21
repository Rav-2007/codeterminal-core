package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// `mcp list` -- the whole tool path, proved without the model.
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
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	configPath := fs.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	workspace := fs.String("workspace", ".", "workspace root to ground built-in tools against")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s mcp list [--config models.json] [--workspace .]\n\n"+
			"Lists every tool agent mode would offer the model, with the policy that applies to it.\n"+
			"Starts the configured MCP servers to ask them, and shuts them down again.\n", os.Args[0])
	}
	// The subcommand is taken BEFORE flag parsing, not after. Go's flag package
	// stops at the first non-flag argument, so `mcp list --config x` would
	// otherwise parse zero flags and silently use the defaults -- reporting on
	// a different config file than the one the user named, which is a worse
	// failure than an error.
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub != "list" {
		return fmt.Errorf("unknown mcp subcommand %q (only \"list\" exists)", sub)
	}

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

	if !cfg.MCP.Enabled {
		fmt.Println("Agent mode is OFF (mcp.enabled is not true in " + resolvedConfigPath + ").")
		fmt.Println("No tools would be offered and no servers would be started.")
		return nil
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
	srv := &Server{cfg: cfg, workspace: root, logger: logger}

	ctx := context.Background()
	registry, connectErrs := srv.buildRegistry(ctx, logger, &proposalSink{}, "")
	defer func() {
		if err := registry.Close(); err != nil {
			logger.Printf("shutting down MCP servers: %v", err)
		}
	}()

	tools, listErrs := registry.Advertised(ctx)

	fmt.Printf("Agent mode is ON. %d tool(s) would be offered to the model.\n\n", len(tools))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// Writes to a tabwriter over stdout: nothing here can recover from a
	// failed write, and a half-printed table is already visible to the reader.
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
		fmt.Printf("\n%d tool(s) were NOT offered because mcp.budget.max_advertised_tools is %d:\n  %s\n",
			len(dropped), cfg.MCP.Budget.resolvedMaxAdvertisedTools(), strings.Join(dropped, "\n  "))
		fmt.Println("A wide tool menu measurably degrades the model's choice of tool " +
			"(see docs/TOOLCALL_RELIABILITY_2026-07-31.md), which is why the cap exists.")
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
		fmt.Printf("\n%d of these run in external MCP servers, which this product CANNOT confine:\n"+
			"they are subprocesses with your full privileges, and the edit pipeline's gates\n"+
			"constrain only this daemon's own writer. Consent and the audit log are the protection.\n",
			external)
	}
	if networked := countNetworked(tools); networked > 0 {
		fmt.Printf("\n%d of these reach the open internet. They run no program and touch no file here;\n"+
			"what they do is send text the model chose to a third party and bring a reply back.\n"+
			"Secrets are stripped outbound and replies are treated as untrusted data, never instructions.\n",
			networked)
	}

	for _, err := range append(connectErrs, listErrs...) {
		fmt.Fprintf(os.Stderr, "\nWARNING: %v\n", err)
		if errors.Is(err, mcp.ErrServerUnavailable) {
			fmt.Fprintln(os.Stderr, "Its tools are missing from the list above. Agent mode would still run, "+
				"reporting this as a degraded subsystem.")
		}
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

// countExternal counts tools that are unconfined BECAUSE they are somebody
// else's subprocess -- the population the subprocess warning is about.
func countExternal(tools []mcp.Tool) int {
	n := 0
	for _, tool := range tools {
		if !tool.Confined && !tool.ReachesNetwork {
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
