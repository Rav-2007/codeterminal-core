// Command codeterminal-tui is CodeTerminal's terminal client, branded
// "Mochiii". Run with no arguments (and no piped stdin) from an actual
// terminal to launch the interactive chat UI. Pass --prompt, or pipe text
// on stdin, to keep the original one-shot behavior instead: connect, send
// one prompt, print the streamed answer, exit — the same behavior this
// client has always had, unchanged, for scripting and tests.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

func main() {
	promptFlag := flag.String("prompt", "", "prompt text to send (reads stdin if omitted); with this flag (or piped stdin), runs one-shot instead of launching the chat UI")
	workspaceFlag := flag.String("workspace", ".", "workspace to ground chat against; sent to the daemon so it can confirm this matches its own configured grounding workspace (one-shot mode ignores this — it predates grounding and stays unchanged)")
	flag.Parse()

	if *promptFlag == "" && isatty.IsTerminal(os.Stdin.Fd()) {
		runChat(*workspaceFlag)
		return
	}

	prompt := readPrompt(*promptFlag)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt given (use --prompt \"...\" or pipe text on stdin)")
		os.Exit(1)
	}
	runOneShot(prompt)
}

// runOneShot is the original non-interactive path: connect, send one
// prompt, print the streamed answer token-by-token, exit.
func runOneShot(prompt string) {
	sess, err := connectToDaemon("codeterminal-tui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer sess.Close()

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: sending prompt: %v\n", err)
		os.Exit(1)
	}

	for {
		var tok protocol.TokenResponse
		if err := sess.dec.Decode(&tok); err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "\nerror: reading token stream: %v\n", err)
			os.Exit(1)
		}
		if tok.Error != "" {
			fmt.Fprintf(os.Stderr, "\nerror from daemon: %s\n", tok.Error)
			os.Exit(1)
		}
		if tok.Token != "" {
			os.Stdout.WriteString(tok.Token) // written immediately, no buffering, so streaming is visible
		}
		if tok.Done {
			break
		}
	}
	os.Stdout.WriteString("\n")
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
func runChat(workspace string) {
	preflight, err := connectToDaemon("codeterminal-tui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	preflight.Close()

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		absWorkspace = workspace // best-effort label; still sent as-is
	}

	workspaceRoot, err := editapply.ResolveRealWorkspaceRoot(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	p := tea.NewProgram(newChatModel("codeterminal-tui", absWorkspace, workspaceRoot), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
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
