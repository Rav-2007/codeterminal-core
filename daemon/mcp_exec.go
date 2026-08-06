package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"codeterminal/daemon/mcp"
)

// execAllowedBinaries is a first, cheap filter — NOT a security boundary, and
// the difference matters enough to state.
//
// Every entry here can run arbitrary code by design:
//
//	make <target>   a Makefile recipe IS shell
//	npm run <s>     a package.json script IS shell
//	go run ./x      compiles and runs anything in the tree
//	cargo build     executes build.rs at build time
//
// Verified, not assumed: `make leak` against a two-line Makefile printed this
// daemon's own OPENROUTER_API_KEY. So this list narrows a careless model's
// blast radius; it does not contain a determined one. What actually contains
// the command is the sandbox below and the environment scrub, and what
// authorises it is the human approving the call.
var execAllowedBinaries = map[string]bool{
	"go":    true,
	"npm":   true,
	"make":  true,
	"cargo": true,
}

const execTimeout = 30 * time.Second

// builtinSandboxExec runs a build or test command in the workspace.
//
// THIS TOOL IS ONLY AS CONFINED AS THE HOST ALLOWS, AND IT SAYS SO. When bwrap
// or docker is present, mcp.WrapCommand puts the command in the same sandbox
// Lane B servers get: a fresh session (so it cannot inject into the terminal
// via TIOCSTI), read-only binds outside the workspace, and no network. When
// neither is installed, WrapCommand falls back to running on the host, and the
// command has the user's full privileges.
//
// The previous version of this file was named for a sandbox it never used. It
// called exec.CommandContext directly while its own description told the
// approving human "Runs in a restricted sandbox", and it inherited the daemon's
// entire environment — including the inference credentials that
// mcp.ForbiddenEnvNames exists to keep out of subprocesses. Both are fixed
// here: the sandbox is the one Lane B already had, and the environment is
// scrubbed by the same function Lane B already used.
func (s *Server) builtinSandboxExec(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("arguments were not a valid JSON object: %v", err)
	}

	parts := strings.Fields(strings.TrimSpace(args.Command))
	if len(parts) == 0 {
		return toolError("command cannot be empty")
	}

	bin := parts[0]
	if !execAllowedBinaries[bin] {
		return toolError("this tool runs build and test commands only; %q is not one of go, npm, make or cargo", bin)
	}

	// Ask for a sandbox; take what the host can give. Mode is left at Auto so
	// bwrap is preferred, docker is the fallback, and neither being installed
	// degrades to host execution rather than failing the call.
	cfg := mcp.SandboxConfig{
		Mode:          mcp.SandboxAuto,
		WorkspaceRoot: s.workspace,
		// A build writes: compiled artifacts, node_modules, target/. Read-only
		// would refuse the tool's whole purpose.
		ReadOnlyWorkspace: false,
		// `go build`, `npm install` and `cargo fetch` all resolve dependencies
		// over the network, so this cannot be false without breaking the tool.
		// It is the widest hole left open here, and it is the reason the
		// environment scrub below is not optional: a command that can reach the
		// network AND read the daemon's credentials is an exfiltration path.
		AllowNetwork: true,
	}
	execBin, execArgs, err := mcp.WrapCommand(bin, parts[1:], cfg)
	if err != nil {
		return toolError("preparing a sandbox for %q: %v", bin, err)
	}

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, execBin, execArgs...)
	cmd.Dir = s.workspace

	// NEVER nil. A nil Env inherits everything this daemon holds, which is where
	// OPENROUTER_API_KEY and CODETERMINAL_MOCHIII_KEY live — a key that can
	// spend the user's money and one that can spend their quota. ServerEnv is
	// the same scrubber Lane B subprocesses get: PATH and HOME pass through,
	// ForbiddenEnvNames can never be granted, and nothing else is passed at all.
	cmd.Env = mcp.ServerEnv(nil)

	out, err := cmd.CombinedOutput()
	result := string(out)

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return mcp.Result{Content: fmt.Sprintf("Command timed out after %s. Output so far:\n%s", execTimeout, result)}, nil
		}
		// A compiler error or a failing test is a RESULT, not a tool failure:
		// the whole point is to hand the output back so the model can read it
		// and fix what it wrote.
		return mcp.Result{Content: fmt.Sprintf("Command exited with error: %v\nOutput:\n%s", err, result)}, nil
	}

	if result == "" {
		result = "(no output)"
	}
	return mcp.Result{Content: result}, nil
}
