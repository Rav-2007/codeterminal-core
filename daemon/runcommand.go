package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// A REAL TERMINAL COMMAND (2026-10-14: the user asked for it).
//
// sandbox_exec runs four programs -- go, npm, make, cargo -- confined where the
// host allows, in the turn's private copy. That cannot install a Python package,
// run a script, or use git, and the user wanted the agent to do what they would
// do in their own terminal. run_command is that, and it is said as plainly as it
// is:
//
//   - IT IS NOT SANDBOXED. It runs through the user's shell, in their REAL
//     project, with their full access and network. What it changes is not
//     reviewed. So EVERY CALL ASKS, whatever the config says (alwaysAskTools),
//     the prompt shows the exact command and says it runs in the real project,
//     and a "yes for this turn" covers that exact command only (ExecutesCode
//     binds the grant to the arguments).
//   - THIS PRODUCT'S KEYS ARE NOT IN ITS ENVIRONMENT. It gets the daemon's
//     environment minus every credential the daemon holds (the model key, the
//     proxy key) -- and MCP servers' keys were never in it.
//   - NOTHING CAN BE TYPED INTO IT. Its input is empty, so a command that waits
//     for an answer gets end-of-file instead of hanging the turn; the model is
//     told to pass non-interactive flags, and sudo cannot prompt.
//   - IT IS BOUNDED: a timeout (default runCommandTimeout, at most
//     maxRunCommandTimeout, and never past the turn's own deadline), the whole
//     process tree killed when it ends, and its output kept to the last
//     execMaxOutputBytes.
//   - Withheld in plan, spec and check modes, and in long tasks, which run
//     with nobody there to answer.

const (
	runCommandToolName   = "run_command"
	runCommandTimeout    = 5 * time.Minute
	maxRunCommandTimeout = 30 * time.Minute
	// runCommandDrain is how long pipes a backgrounded child still holds may
	// keep the call open after the command itself has ended.
	runCommandDrain = 5 * time.Second
)

func (s *Server) runCommandTools(mode string) []mcp.Builtin {
	if isPlanMode(mode) || isCheckMode(mode) || isSpecMode(mode) || isLongTaskMode(mode) {
		return nil
	}
	return []mcp.Builtin{{
		Tool: mcp.Tool{
			Name: runCommandToolName,
			Description: "Run a shell command in the user's REAL project folder, as if they typed it in their own " +
				"terminal: install packages (pip, npm, cargo, brew...), run scripts, use git, start a build. The user " +
				"is asked first, every time, and sees the exact command. It is NOT sandboxed: it runs with their full " +
				"access and network, and what it changes is not reviewed. Edits you proposed this turn are not in the " +
				"real project until the user accepts them -- to test your edits, use sandbox_exec. Nothing can be typed " +
				"into it: use non-interactive flags (-y, --yes); sudo cannot ask for a password, so tell the user to run " +
				"such a command themselves. Output is kept to its last part. Times out after timeout_seconds (default " +
				fmt.Sprintf("%d, at most %d).", int(runCommandTimeout/time.Second), int(maxRunCommandTimeout/time.Second)),
			Schema: schema(`{"type":"object","properties":{` +
				`"command":{"type":"string","description":"The command line, run by the user's shell."},` +
				`"cwd":{"type":"string","description":"A folder inside the project to run it in; the project root when left out."},` +
				`"timeout_seconds":{"type":"integer","minimum":1}},` +
				`"required":["command"],"additionalProperties":false}`),
			ExecutesCode: true,
			Confined:     false,
		},
		Handler: s.builtinRunCommand,
		// Said on the prompt: it runs in the real project, not a copy.
		InPlace: func() bool { return true },
	}}
}

// runCommandEnv is the daemon's environment without this product's
// credentials, and with the switches that keep installers from waiting on a
// prompt nobody can answer.
func runCommandEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if mcp.IsForbiddenEnvName(name) || strings.HasPrefix(strings.ToUpper(name), "MOCHIII_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "DEBIAN_FRONTEND=noninteractive", "NONINTERACTIVE=1",
		"PIP_NO_INPUT=1", "npm_config_yes=true")
}

// runCommandDir is where the command runs: the project root, or a folder in it.
func (s *Server) runCommandDir(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" || cwd == "." {
		return s.workspace, nil
	}
	path, err := editapply.ResolveSafeTargetPath(s.workspace, cwd)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder in the project", cwd)
	}
	return path, nil
}

func (s *Server) builtinRunCommand(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Command        string `json:"command"`
		Cwd            string `json:"cwd"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return toolError("no command was given")
	}
	dir, err := s.runCommandDir(args.Cwd)
	if err != nil {
		return toolError("not run: %v", err)
	}
	timeout := runCommandTimeout
	if args.TimeoutSeconds > 0 {
		timeout = min(time.Duration(args.TimeoutSeconds)*time.Second, maxRunCommandTimeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(runCtx, args.Command) // its environment: runCommandEnv
	cmd.Dir = dir
	cmd.Stdin = nil // reads as end-of-file: nothing can be typed into it
	cmd.WaitDelay = runCommandDrain
	var buf tailBuffer
	buf.max = execMaxOutputBytes
	cmd.Stdout, cmd.Stderr = &buf, &buf

	start := time.Now()
	s.logger.Printf("agent: run_command in %s: %s", dir, truncateForClient(args.Command, 200))
	err = cmd.Run()
	took := time.Since(start).Round(100 * time.Millisecond)
	out := commandDigest(buf.String(), s.execDigestLimit())
	if out == "" {
		out = "(no output)"
	}
	switch {
	case runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		return mcp.Result{Content: fmt.Sprintf("The command was stopped after %s, its time limit. Output so far:\n%s",
			humanDuration(timeout), out)}, nil
	case ctx.Err() != nil:
		return mcp.Result{Content: "The command was stopped because the turn ended. Output so far:\n" + out}, nil
	case err == nil:
		return mcp.Result{Content: fmt.Sprintf("Exit code 0 (%s).\n%s", took, out)}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// A failing command is a RESULT for the model to read, not a tool failure.
		return mcp.Result{Content: fmt.Sprintf("Exit code %d (%s).\n%s", exit.ExitCode(), took, out)}, nil
	}
	return toolError("the command could not be run: %v", err)
}

// shellPath is the user's shell when it reads -lc as bash does, so a login
// profile's PATH (nvm, pyenv, cargo) is there as in their terminal.
func shellPath() (shell string, login bool) {
	if sh := os.Getenv("SHELL"); sh != "" {
		switch filepath.Base(sh) {
		case "bash", "zsh":
			return sh, true
		}
	}
	return "/bin/sh", false
}
