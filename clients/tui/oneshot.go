// The one-shot client path: connect, send one prompt, print the answer, exit.
//
// It predates the chat UI and is what scripts and tests drive, so its default
// behaviour is deliberately unchanged. What is new is that it can now answer a
// tool-approval prompt -- but only when there is genuinely a person there to
// answer it.
//
// THAT CONDITION IS THE WHOLE POINT OF THIS FILE. CapToolApproval is a promise:
// the daemon runs the agentic loop only for a client that declares it, and then
// suspends a turn waiting for a reply. A client that declares it and cannot
// reply leaves every tool call hanging until the five-minute human deadline
// expires, and then denied -- so a piped, scripted, or otherwise non-interactive
// run must not declare it, and gets the ordinary single-turn path it always had.
//
// Same reasoning as the CLI's `edits apply`, which refuses to confirm an edit
// when it cannot reach a terminal (daemon/apply_cmd.go).
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"

	"mochiii/protocol"
)

// oneShotIO is the one-shot run's environment, injected so the whole path
// including the approval prompt is testable without a terminal.
type oneShotIO struct {
	in  io.Reader
	out io.Writer
	err io.Writer
	// interactive reports whether in is a real terminal that a person is sitting
	// at. It gates the capability declaration, so nothing else in here has to
	// re-derive "can we actually ask?".
	interactive bool
}

// brokenPipeWriter notices the far end of stdout going away.
//
// MEASURED before this existed: `mochiii-tui --prompt ... | head` exited
// 141 -- the Go runtime kills a process that writes to a closed fd 1 unless
// SIGPIPE is handled. Someone reading the first few lines of an answer is
// doing an ordinary thing, and being killed by a signal for it is not a clean
// exit.
//
// Once the pipe is broken every later write is swallowed rather than retried:
// the loop is on its way out, and a second EPIPE has nothing to add.
type brokenPipeWriter struct {
	w      io.Writer
	broken bool
}

func (b *brokenPipeWriter) Write(p []byte) (int, error) {
	if b.broken {
		return len(p), nil
	}
	n, err := b.w.Write(p)
	if errors.Is(err, syscall.EPIPE) {
		b.broken = true
	}
	return n, err
}

// runOneShotPrompt sends one prompt and prints the streamed answer, answering
// any tool approvals from io.in. It returns a process exit code rather than
// calling os.Exit, so it can be tested.
func runOneShotPrompt(clientName, prompt string, env oneShotIO) int {
	var caps []string
	if env.interactive {
		caps = append(caps, protocol.CapToolApproval)
	}

	sess, err := connectToDaemon(clientName, caps...)
	if err != nil {
		say(env.err, "error: %v\n", err)
		return 1
	}
	defer func() { _ = sess.Close() }() // see daemonSession.Close

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
	}); err != nil {
		say(env.err, "error: sending prompt: %v\n", err)
		return 1
	}

	// The reader going away is normal for a pipeline, so it must not be fatal
	// and must not be a signal death. See ignoreSIGPIPE.
	restoreSIGPIPE := ignoreSIGPIPE()
	defer restoreSIGPIPE()
	stdout := &brokenPipeWriter{w: env.out}
	env.out = stdout

	reader := bufio.NewReader(env.in)
	// ONE-SHOT WRITES TO A TERMINAL TOO. `mochiii-tui --prompt ...` run at
	// a shell prompt puts model bytes straight on the screen with no viewport
	// in between, so it needs the same filter the chat UI has (sanitize.go).
	// Stateful and hoisted out of the loop for the same reason it is in the
	// chat model: one token can end mid-sequence and the next completes it.
	//
	// UNCONDITIONAL, NOT GATED ON isatty, and that is the whole point. An
	// escape written to a file is not defused, it is DEFERRED: it executes the
	// moment anyone cats or lesses that file, and one-shot output landing in a
	// log that someone later greps is exactly that path. Deciding by whether
	// stdout happens to be a terminal today would leave the sequence armed for
	// whoever reads it tomorrow.
	//
	// This does change the bytes a caller piping stdout receives, which the
	// note below the loop used to promise it would not. The promise was wrong:
	// what changes is only control sequences, never the text of the answer.
	// See docs/RELEASE_NOTES.md.
	var sani escSanitizer
	for {
		var tok protocol.TokenResponse
		if err := sess.dec.Decode(&tok); err != nil {
			if err == io.EOF {
				break
			}
			say(env.err, "\nerror: reading token stream: %v\n", err)
			return 1
		}
		if tok.Error != "" {
			say(env.err, "\nerror from daemon: %s\n", sanitizeText(tok.Error))
			return 1
		}
		// Activity and approvals go to STDERR, never stdout. Stdout carries
		// the answer and nothing else.
		//
		// WHAT IS GUARANTEED ABOUT STDOUT, precisely, because this used to
		// promise more than it should have: the TEXT of the answer is
		// byte-stable -- every printable character, newline and tab the model
		// produced, in order, unchanged. Terminal control sequences are
		// removed (see sanitize.go), except colour, which is preserved. A
		// pipeline reading an answer gets the answer.
		if tok.ToolActivity != nil {
			if line := oneShotActivityLine(*tok.ToolActivity); line != "" {
				say(env.err, "%s\n", sanitizeText(line))
			}
		}
		if tok.ToolApproval != nil {
			decision := askOneShotApproval(*tok.ToolApproval, reader, env.err)
			if err := sess.enc.Encode(protocol.ToolApprovalResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Approval:        decision == protocol.ApprovalApprove || decision == protocol.ApprovalApproveForTurn,
				CallID:          tok.ToolApproval.CallID,
				ArgumentsSHA256: tok.ToolApproval.ArgumentsSHA256,
				Decision:        decision,
			}); err != nil {
				say(env.err, "\nerror: sending approval: %v\n", err)
				return 1
			}
		}
		if tok.Token != "" {
			say(env.out, "%s", sani.Write(tok.Token)) // unbuffered, so streaming stays visible
		}
		if stdout.broken {
			// Nobody is reading any more. Exit 0: `| head` is a request for
			// part of the answer, not an error to report.
			return 0
		}
		if tok.Done {
			if tok.Incomplete != nil {
				say(env.err, "\nnote: %s\n", sanitizeText(tok.Incomplete.Detail))
			}
			break
		}
	}
	// Whatever the filter was still holding, before the trailing newline.
	say(env.out, "%s", sani.Flush())
	say(env.out, "\n")
	return 0
}

// askOneShotApproval prints one pending call and reads a decision.
//
// Only a literal "y" or "a" approves -- the same strict default-deny confirm as
// the CLI's `[y/N]` for edits, and as the chat UI's approval modal. Anything
// else, including EOF from a stdin that has nothing more to give, is a denial:
// this is the point where "we could not ask" and "they said no" have to resolve
// the same way, and the safe way.
func askOneShotApproval(req protocol.ToolApprovalRequest, reader *bufio.Reader, out io.Writer) string {
	say(out, "\n--- run %s__%s? (step %d of at most %d) ---\n",
		sanitizeText(req.Server), sanitizeText(req.Tool), req.Iteration, req.MaxIterations)
	// The consent surface, same as the chat UI's approval panel: the request
	// stays byte-exact because the daemon binds approval to a digest of it,
	// and only what reaches the screen is filtered.
	say(out, "arguments: %s\n", sanitizeText(req.Arguments))
	switch {
	case req.ReachesNetwork:
		// CHECKED FIRST, because BOTH of the other branches are wrong for a
		// network call and they are wrong in opposite directions. Confined
		// would say "anything it changes goes through the same review you use
		// for edits" -- true, and not the question. The unconfined branch below
		// would say "a separate program running with your full access" -- and
		// that is simply false: web_search spawns nothing and has no access to
		// anything local. Crying wolf here is not the safe error; it is how a
		// prompt gets trained out of being read.
		say(out, "LEAVES YOUR MACHINE: this sends the text above to a third party over the internet\n")
		say(out, "and brings a reply back into the conversation. Secrets are stripped on the way out and\n")
		say(out, "the reply is treated as untrusted data — but nothing here can vouch for the far end.\n")
	case req.Confined:
		say(out, "this tool ships with Mochiii; anything it changes goes through the same review you use for edits\n")
	default:
		// Never softened. A third-party MCP server is an ordinary subprocess
		// with the user's full access, and this approval is the only thing in
		// front of it.
		say(out, "NOT SANDBOXED: this is a separate program running with your full access.\n")
		say(out, "Mochiii cannot limit what it reads or changes — your approval is the only thing in its way.\n")
	}
	if req.Destructive {
		say(out, "the server describes this tool as destructive\n")
	}
	say(out, "[y]es once / [a]llow for this task / [N]o / [q]uit: ")

	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		say(out, "\nno answer available — denied\n")
		return protocol.ApprovalDeny
	}
	switch strings.TrimSpace(line) {
	case "y":
		say(out, "approved\n")
		return protocol.ApprovalApprove
	case "a":
		say(out, "approved for this task\n")
		return protocol.ApprovalApproveForTurn
	case "q":
		say(out, "stopping the task\n")
		return protocol.ApprovalCancelTurn
	default:
		say(out, "denied\n")
		return protocol.ApprovalDeny
	}
}

// oneShotActivityLine narrates a tool step on stderr. Only phases a human
// benefits from seeing: the approval prompt already told them the call was
// requested and that they approved it.
func oneShotActivityLine(a protocol.ToolActivity) string {
	name := qualifiedActivityName(a)
	switch a.Phase {
	case protocol.ToolPhaseStep:
		return a.Detail + " — " + a.Tool
	case protocol.ToolPhaseRunning:
		return "running " + name + "…"
	case protocol.ToolPhaseSucceeded:
		return fmt.Sprintf("%s — %d bytes to the model in %dms", name, a.ResultBytes, a.DurationMS)
	case protocol.ToolPhaseFailed:
		return name + " failed" + oneShotDetail(a.Detail)
	case protocol.ToolPhaseDenied:
		return name + " not run" + oneShotDetail(a.Detail)
	}
	return ""
}

func oneShotDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

// say writes one line of chrome to w, dropping the write error.
//
// Stated once here rather than twenty times at the call sites. This is a CLI
// printing to a terminal it does not own: if stderr has gone there is nowhere
// to report that stderr has gone, and failing a turn because a progress line
// could not be printed would be a worse outcome than not printing it. The
// answer itself goes through the same helper because that is what the one-shot
// path always did (os.Stdout.WriteString, unchecked) and a piped reader that
// closed early is not this program's error to raise.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
