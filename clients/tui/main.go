// Package tui is Mochiii's terminal client, branded "Mochiii": the default
// role of the one `mochiii` binary, and cmd/mochiii-tui on its own. Run with
// no arguments (and no piped stdin) from an actual
// terminal to launch the interactive chat UI. Pass --prompt, or pipe text
// on stdin, to keep the original one-shot behavior instead: connect, send
// one prompt, print the streamed answer, exit — the same behavior this
// client has always had, unchanged, for scripting and tests.
package tui

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"mochiii/editapply"
	"mochiii/protocol"
)

// Main runs the client with its command-line arguments (without the program
// name). It exits the process itself on every path but a plain return.
func Main(args []string) {
	// FIRST, before flags, before anything can draw. See renderprofile.go: the
	// colour profile is otherwise decided by whatever the environment happens to
	// say at the moment of the first Style.Render, wherever in the program that
	// turns out to be. Resolving it here makes rendering a function of state.
	pinColorProfile()

	fs := flag.NewFlagSet("mochiii", flag.ExitOnError)
	promptFlag := fs.String("prompt", "", "prompt text to send (reads stdin if omitted); with this flag (or piped stdin), runs one-shot instead of launching the chat UI")
	workspaceFlag := fs.String("workspace", ".", "workspace to ground chat against; sent to the daemon so it can confirm this matches its own configured grounding workspace (one-shot mode ignores this — it predates grounding and stays unchanged)")
	versionFlag := fs.Bool("version", false, "print this client's version and exit")
	indexFlag := fs.Bool("index", false, "index (or re-index) this folder for code search, then open the chat")
	noIndexFlag := fs.Bool("no-index", false, "do not offer to index this folder (also MOCHIII_NO_INDEX=1)")
	stopFlag := fs.Bool("stop", false, "stop Mochiii's background service for this folder, then exit")
	_ = fs.Parse(args) // ExitOnError: a bad flag has already exited

	if *versionFlag {
		fmt.Println("mochiii", Version)
		return
	}
	launch := launchOptions{forceIndex: *indexFlag, noIndex: *noIndexFlag || os.Getenv("MOCHIII_NO_INDEX") == "1"}
	if *stopFlag {
		os.Exit(runStop(*workspaceFlag))
	}

	if *promptFlag == "" && isatty.IsTerminal(os.Stdin.Fd()) {
		runChat(*workspaceFlag, launch)
		return
	}

	prompt := readPrompt(*promptFlag)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt given (use --prompt \"...\" or pipe text on stdin)")
		os.Exit(1)
	}
	// interactive is what decides whether this run may declare CapToolApproval,
	// and it is deliberately strict. A PIPED prompt means stdin is already spent
	// on the prompt itself, so there is nothing left to read an answer from --
	// and a --prompt run from a script has no person at the other end either.
	// Only --prompt from a real terminal leaves a human who can be asked.
	// Declaring the capability without one would hang every tool call until the
	// daemon's five-minute deadline expired. See oneshot.go.
	interactive := *promptFlag != "" && isatty.IsTerminal(os.Stdin.Fd())
	// --workspace selects WHICH DAEMON to talk to, in one-shot mode too. That is
	// a different question from grounding, which one-shot still does not send
	// (see this flag's help): the lockfile is per workspace, so a client with no
	// root resolves to the per-user name no daemon writes any more. Best-effort
	// -- an unresolvable path leaves the root empty, which is the documented
	// fallback rather than a reason to refuse to run.
	if realRoot, err := editapply.ResolveRealWorkspaceRoot(*workspaceFlag); err == nil {
		setDaemonWorkspaceRoot(realRoot)
		prepareDaemon(realRoot, launch, false)
	}
	os.Exit(runOneShotPrompt("mochiii-tui", prompt, oneShotIO{
		in: os.Stdin, out: os.Stdout, err: os.Stderr, interactive: interactive,
	}))
}

// runChat launches Mochiii's interactive chat UI. Before ever drawing the
// alt-screen, it preflights the daemon connection so a down daemon produces
// one clean stderr message and a non-zero exit, not a TUI the user has to
// type into first just to discover it can't reach anything. It also
// resolves the real (symlink-resolved) workspace root up front, the same
// way the CLI's `edits apply` does (editapply.ResolveRealWorkspaceRoot) —
// applying an edit approved during review confines to this root, so a bad
// --workspace value fails fast here rather than mid-review.
//
// workspace is separately resolved to a plain absolute path (no symlink
// resolution) for display/grounding purposes, so the daemon's own absolute
// grounding workspace (see daemon/main.go) can be compared against it
// meaningfully — see protocol.GroundingInfo.WorkspaceMismatch.
func runChat(workspace string, launch launchOptions) {
	// RESOLVED BEFORE THE PREFLIGHT, not after. The daemon's lockfile is named
	// from this root, so connecting without it looks for a file nothing writes.
	// It used to sit below the connection, where it served only to fail fast on
	// a bad --workspace before review.
	workspaceRoot, err := editapply.ResolveRealWorkspaceRoot(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	setDaemonWorkspaceRoot(workspaceRoot)
	sayIfNotAProject(os.Stderr, workspaceRoot)
	prepareDaemon(workspaceRoot, launch, true)

	preflight, err := connectToDaemon("mochiii-tui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	// Cross-session conversation memory (see protocol.HandshakeResponse.
	// PersistedHistory and daemon/memory.go) hydrates here and ONLY here:
	// this one-time preflight connection, before the chat UI even starts.
	// The streaming path (stream.go) never reads this field, so a later
	// per-prompt connection can't re-hydrate turns the TUI already has.
	persistedHistory := preflight.handshake.PersistedHistory
	// Read here, beside the other handshake field, rather than after Close below.
	needsAPIKey := preflight.handshake.NeedsAPIKey
	daemonSavesChats := hasFeature(preflight.handshake, protocol.FeatureSavedChats)
	// The revision of the shared chat persistedHistory is; empty from a daemon
	// with no memory, or one older than revisions (see chatsync.go).
	chatRev := preflight.handshake.ChatRevision
	// Ignored deliberately, and it is the same reasoning at all five close
	// sites in this client -- see the note on daemonSession.Close.
	_ = preflight.Close()

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		absWorkspace = workspace // best-effort label; still sent as-is
	}

	model := newChatModel("mochiii-tui", absWorkspace, workspaceRoot, persistedHistory)
	// Carried from the handshake rather than asked for here. It holds a question
	// back until a key is in force (beginConnectForPrompt in connect.go), and
	// decides which screen the client opens on, just below.
	model.needsAPIKey = needsAPIKey
	model.chatSync, model.chatRev = chatRev != "", chatRev
	// Who is running this, and on which machine: asked of the system once, here,
	// and from then on only drawn, on the row under the prompt (whoami.go).
	model.whoami = lookupWhoami()
	// NO KEY IN FORCE: open on the setup -- provider, key, model -- instead of
	// the splash (setup.go, owner 2026-10-11). Esc there goes to the prompt,
	// which still asks for a key when the first question needs one.
	if needsAPIKey {
		model = model.withFirstRunSetup()
	}
	// Said once, first thing: a daemon older than this client cannot save
	// chats, and the user should hear it before /history refuses.
	if !daemonSavesChats {
		model.appendTurn(turn{role: roleSystem, text: "⚠ " + errDaemonPredatesHistory.Error() +
			" (until then /history does not work)"})
	} else if len(persistedHistory) > 0 {
		// Under the turns it describes, so it is the line beside the prompt.
		if note := model.continuedChatNote(len(persistedHistory), time.Now()); note != "" {
			model.appendTurn(turn{role: roleSystem, text: note})
		}
	}

	// Mouse capture ON: the wheel scrolls the transcript as mouse events, which
	// leaves up/down free for prompt history. Selection is shift+drag, and the
	// idle hint says so. See chatModel.mouseCaptured and docs/ADR-001.
	// Focus reports: coming back to this terminal from the editor is when the
	// shared chat may have moved on (chatsync.go).
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithReportFocus())
	// SIGHUP and SIGQUIT reach Bubble Tea's own shutdown through here; without
	// it SIGHUP killed the process with the alternate screen still up. finish
	// also re-raises a caught SIGQUIT, which is why it runs before the error
	// check below -- see exitsignals_unix.go.
	finish := installExitSignals(p.Quit)
	_, err = p.Run()
	finish()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func readPrompt(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	fmt.Fprintln(os.Stderr, "(reading prompt from stdin; press Ctrl-D when done)")
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading stdin: %v\n", err)
		os.Exit(1)
	}
	return strings.TrimSpace(string(data))
}
