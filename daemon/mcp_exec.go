package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
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

// execMaxOutputBytes bounds what one command may produce, AT SOURCE.
//
// cmd.CombinedOutput() is unbounded: it grows a buffer until the process stops.
// The downstream egress cap in dispatchToolCall bounds what reaches the MODEL,
// which is the privacy quantity, but it cannot bound what this daemon
// ALLOCATES first -- a `go build` over a large tree, a test suite with verbose
// logging, or a runaway loop inside a Makefile recipe all produce output at
// whatever rate they like, for the full 30 seconds.
//
// 1 MiB, against a default max_tool_result_bytes of 32 KiB: far more than any
// build output a model can use, far less than a number that matters to the
// host. The tail is kept rather than the head, because a build's ERROR is at
// the end and the head is banner text.
const execMaxOutputBytes = 1 << 20

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
// sandboxExecImage names the container image the docker backend would use.
//
// Empty, and that is the whole point: an empty image means DockerUsable is
// false, so SandboxAuto skips the docker backend instead of building an
// invocation that cannot run. Exposed as a function rather than inlined so the
// tests can ask the same question the tool does, instead of guessing from
// whether a docker binary happens to be on PATH.
func sandboxExecImage() string { return "" }

// sandboxExecConfig is the confinement sandbox_exec asks for.
//
// ONE FUNCTION, TWO CALLERS, and that is the point. The approval prompt has to
// tell the user whether this call will really be confined, and the handler has
// to actually confine it. Deriving those from two different expressions is how a
// prompt ends up describing a sandbox the command does not get; both now build
// the same cfg and ask mcp.ResolveMode the same question.
// The bounds sandbox_exec asks for. Two of the three, and the omission is a
// decision rather than an oversight:
//
//	MEMORY — 2 GiB. Enough to link a large Go binary or run a webpack build,
//	  far below what it takes to make a desktop unusable. Measured: a process
//	  allocating 512 MiB under a 64 MiB cap dies at the cap (exit 137), and
//	  dies the same way through the full systemd-run -> env -> bwrap stack.
//
//	PIDS — 512. This is the one that stops a fork bomb, and no memory cap
//	  substitutes for it: each shell in `:(){ :|:& };:` is tiny, so the pids
//	  limit is reached long before the memory one. Measured: a 300-fork loop
//	  under TasksMax=32 stopped dead at 31 and the scope was killed, while the
//	  same loop under TasksMax=400 completed all 300.
//
//	CPU — deliberately unset. A build should use the machine it is on; the
//	  30-second timeout already bounds how long it can, and a quota buys
//	  slower builds in exchange for a bound that is already held elsewhere.
const (
	execMemoryLimitMB = 2048
	execPidsLimit     = 512
)

// sandboxExecHome is the host directory bound in as the command's HOME.
//
// PER WORKSPACE, like every other piece of per-project state this daemon keeps
// (the lock, the socket, the index all key on protocol.WorkspaceTag). The
// alternative -- one shared cache -- reuses downloads across projects and lets
// a hostile postinstall script in one repository leave something behind for a
// build in another. Per-workspace costs disk and some re-downloading, which is
// the cheap side of that trade.
//
// In the user CACHE directory rather than in the workspace: this is a cache by
// definition, it can be deleted at any time, and it should not turn up in the
// user's repository, their backups or their `du`.
//
// Returns "" if the cache directory cannot be determined, which simply means no
// HomeDir is set and behaviour is what it was before -- never a failed turn.
func (s *Server) sandboxExecHome() string {
	root, err := sandboxHomeRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, protocol.WorkspaceTag(s.workspace))
}

func (s *Server) sandboxExecConfig() mcp.SandboxConfig {
	return mcp.SandboxConfig{
		MemoryLimitMB: execMemoryLimitMB,
		PidsLimit:     execPidsLimit,
		HomeDir:       s.sandboxExecHome(),
		// Auto: bwrap preferred, docker next, then landlock; none of the three
		// degrades to host execution rather than failing the call -- and the
		// approval prompt SAYS SO when it degrades.
		Mode:          mcp.SandboxAuto,
		WorkspaceRoot: s.workspace,
		Image:         sandboxExecImage(),
		// This tool's commands need only the system, their toolchain, the
		// workspace and HomeDir -- exactly what the landlock policy grants -- so
		// it is the one caller that opts in. See mcp.SandboxConfig.LandlockFallback
		// for why third-party MCP servers do not.
		LandlockFallback: true,
		// A build writes: compiled artifacts, node_modules, target/. Read-only
		// would refuse the tool's whole purpose.
		ReadOnlyWorkspace: false,
		// `go build`, `npm install` and `cargo fetch` all resolve dependencies
		// over the network, so this cannot be false without breaking the tool.
		// It is the widest hole left open here, and it is the reason the
		// environment scrub is not optional: a command that can reach the
		// network AND read the daemon's credentials is an exfiltration path.
		AllowNetwork: true,
	}
}

// sandboxExecConfined answers, for THIS host and right now, the question the
// approval prompt is really asking: will this command be inside anything?
func (s *Server) sandboxExecConfined() bool { return mcp.Confines(s.sandboxExecConfig()) }

// sandboxExecDescription is what the approving human reads, built from the same
// two questions the handler is about to answer.
//
// F-01 WAS THIS EXACT MISTAKE ON THE OTHER AXIS: a static string said "Runs in
// a restricted sandbox" while the handler ran on the host. The fix was to derive
// the claim from the selection. Resource bounds are a SECOND axis with the same
// failure available -- a host with no user systemd gets bwrap confinement and no
// memory or pids cap at all -- so the sentence about limits is derived too,
// rather than written once and true on the machine it was written on.
//
// The two are reported separately because they are separate: a command can be
// inside a namespace and able to exhaust the host, or bounded and unconfined.
func (s *Server) sandboxExecDescription() string {
	cfg := s.sandboxExecConfig()
	confinement := sandboxConfinementSentence(cfg)

	limits := fmt.Sprintf("Capped at %d MiB of memory and %d processes.", cfg.MemoryLimitMB, cfg.PidsLimit)
	if !mcp.LimitsApply(cfg) {
		limits = "NO memory or process limit on this host, so a runaway build can exhaust it."
	}

	return "Run a build or test command (go, npm, make, cargo) in the workspace root, with a 30s timeout. " +
		confinement + " " + limits + " It can reach the network, which its purpose requires. " +
		"These tools execute project-supplied scripts (Makefile recipes, package.json scripts, build.rs), " +
		"so approving a call approves whatever the project's build files do. " +
		"This daemon's API credentials are never passed to it."
}

// sandboxConfinementSentence says what the backend ResolveMode chose will and
// will not hold, and -- when it chose nothing -- why, in the words a person
// deciding whether to approve needs.
//
// "Confined to this workspace" opens every confined sentence and no other, so
// the phrase keeps meaning exactly mcp.Confines (sandboxconsent_test.go and
// sandboxlimits_test.go hold it to that).
//
// THE LANDLOCK SENTENCE STATES ITS LIMITS. It is a real boundary for files and
// for local services, and it is not a private machine: without a PID namespace
// the command can see the user's other programs, and before Landlock ABI 6 it
// can signal them too. Saying so is what keeps "confined" from overclaiming.
func sandboxConfinementSentence(cfg mcp.SandboxConfig) string {
	switch mcp.ResolveMode(cfg) {
	case mcp.SandboxNone:
		why := "nothing here can confine it"
		if reasons := mcp.PassedOver(cfg); len(reasons) > 0 {
			why = strings.Join(reasons, "; ")
		}
		return "NOT confined on this host (" + why + "), so it runs with your full privileges."
	case mcp.SandboxLandlock:
		sentence := "Confined to this workspace by Landlock on this host: it can read the system and its " +
			"toolchain, and write only this workspace and its own cache, not your home folder or /tmp. " +
			"It cannot open Unix sockets, so it cannot reach your desktop session, ssh-agent or this daemon. " +
			"Unlike a full sandbox, it can see your other running programs"
		if mcp.LandlockABI() < 6 {
			sentence += ", and send them signals"
		}
		return sentence + "."
	default:
		return "Confined to this workspace on this host."
	}
}

// sandboxExecBackendSummary is the startup log's one line about sandbox_exec:
// which backend it will use, and why each one ranked above it was passed over.
// The same daemon can land on different backends depending on what launched it
// (an AppArmor profile is inherited), and this is where that becomes visible
// before anyone is asked to approve anything.
func (s *Server) sandboxExecBackendSummary() string {
	cfg := s.sandboxExecConfig()
	mode := mcp.ResolveMode(cfg)
	head := "confined by " + string(mode)
	if mode == mcp.SandboxNone {
		head = "NOT confined"
	}
	if reasons := mcp.PassedOver(cfg); len(reasons) > 0 {
		return head + " (" + strings.Join(reasons, "; ") + ")"
	}
	return head
}

// builtinRepoMap answers the repo_map tool.
//
// Built on demand rather than cached: the workspace changes under this daemon
// (the user edits, and this product's own edit path writes), and a stale map is
// worse than a slow one -- it is the same "invented path" failure arriving from
// the other direction. Measured at ~55ms over 515 files, which is cheaper than
// the round trip that asked for it.
func (s *Server) builtinRepoMap(ctx context.Context) (mcp.Result, error) {
	if s.workspace == "" {
		return toolError("this daemon has no workspace to map")
	}
	m, err := buildRepoMap(ctx, s.workspace)
	if err != nil {
		// Client-safe by construction: buildRepoMap's errors name the workspace
		// root, which the caller already knows.
		return toolError("could not read the workspace: %v", err)
	}
	return mcp.Result{Content: m.Render()}, nil
}

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

	// Ask for a sandbox; take what the host can give. The SAME cfg the approval
	// prompt was built from -- see sandboxExecConfig.
	//
	// NO IMAGE, so the docker backend is deliberately unavailable. Choosing one
	// is a product decision this code cannot make: the image has to carry the
	// user's Go, Node, Make or Cargo toolchain at the versions their project
	// expects, and guessing wrong breaks the build in a way that looks like the
	// tool is broken. Until that is decided, docker is off rather than
	// half-configured -- which is what produced `docker run ... make` asking
	// Docker for an image named "make".
	cfg := s.sandboxExecConfig()
	execBin, execArgs, err := mcp.WrapCommand(bin, parts[1:], cfg)
	if err != nil {
		return toolError("preparing a sandbox for %q: %v", bin, err)
	}

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, execBin, execArgs...)
	cmd.Dir = s.workspace

	// Created here rather than in sandboxExecConfig, because that function is
	// also what the APPROVAL PROMPT is built from and asking "will this be
	// confined?" must not create directories on disk.
	//
	// A failure is not fatal: bwrap would refuse to bind a path that does not
	// exist, so the bind is dropped and the command runs with the tmpfs HOME it
	// had before. Slower, not broken.
	if cfg.HomeDir != "" {
		if err := os.MkdirAll(cfg.HomeDir, 0o700); err != nil {
			s.logger.Printf("mcp: sandbox home %s: %v (falling back to an ephemeral HOME)", cfg.HomeDir, err)
			cfg.HomeDir = ""
			if execBin, execArgs, err = mcp.WrapCommand(bin, parts[1:], cfg); err != nil {
				return toolError("preparing a sandbox for %q: %v", bin, err)
			}
			cmd = exec.CommandContext(execCtx, execBin, execArgs...)
			cmd.Dir = s.workspace
		} else if err := recordSandboxHomeUse(cfg.HomeDir, s.workspace); err != nil {
			// Whose folder this is, and that it was just used (register item
			// 37), so the reclaim pass can tell a live project's cache from an
			// abandoned one. Not fatal: without the record the folder is still
			// reclaimed, just by age alone.
			s.logger.Printf("mcp: recording sandbox home use for %s: %v", cfg.HomeDir, err)
		}
	}

	// NEVER nil. A nil Env inherits everything this daemon holds, which is where
	// OPENROUTER_API_KEY and MOCHIII_PROXY_KEY live — a key that can
	// spend the user's money and one that can spend their quota. ServerEnv is
	// the same scrubber Lane B subprocesses get: PATH and HOME pass through,
	// ForbiddenEnvNames can never be granted, and nothing else is passed at all.
	//
	// TWO FUNCTIONS, NOT ONE WITH A FLAG. LimiterEnv adds the session-bus
	// variables systemd-run needs to reach the user manager, and the limiter
	// prefix strips them again before the command starts (see
	// limiterRuntimeEnvNames -- a command that can reach that bus can ask
	// systemd to run something outside its own sandbox). When no limiter is in
	// play there is nothing to strip them, so they must never be added: hence
	// the condition here is mcp.LimitsApply, the same one that decided whether
	// the prefix is there at all.
	if mcp.LimitsApply(cfg) {
		cmd.Env = mcp.LimiterEnv(nil)
	} else {
		cmd.Env = mcp.ServerEnv(nil)
	}
	// HOME last, so it wins over the pass-through copy. Same path inside the
	// namespace as out (WrapCommand binds it identity-wise), so this one
	// assignment is correct in every backend.
	if cfg.HomeDir != "" {
		cmd.Env = append(cmd.Env, "HOME="+cfg.HomeDir)
	}
	// LANDLOCK HAS NO PRIVATE /tmp. bwrap mounts a fresh tmpfs there; landlock
	// cannot, and the shared /tmp holds other programs' files and sockets, so
	// the policy does not grant it. The command gets a directory of its own
	// inside HomeDir instead, gone when it finishes -- the lifetime bwrap's
	// tmpfs has. Go, npm and cargo all take it from TMPDIR.
	if cfg.HomeDir != "" && mcp.ResolveMode(cfg) == mcp.SandboxLandlock {
		tmp, err := sandboxCallTempDir(cfg.HomeDir)
		if err != nil {
			return toolError("preparing a temporary directory for %q: %v", bin, err)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		cmd.Env = append(cmd.Env, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp)
	}

	// Bounded at source. See execMaxOutputBytes.
	var buf tailBuffer
	buf.max = execMaxOutputBytes
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	result := buf.String()

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

// sandboxCallTempDir makes one command's private temporary directory, under
// <home>/.tmp so the landlock policy's grant of home covers it and the
// sandbox-home reclaim takes any a crash leaves behind.
func sandboxCallTempDir(home string) (string, error) {
	parent := filepath.Join(home, ".tmp")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, "call-")
}

// tailBuffer keeps at most max bytes, discarding from the FRONT.
//
// The tail is what matters for a build: the compiler error, the failing
// assertion and the exit summary are all at the end, while the head is banner
// text and dependency resolution. A head-keeping cap would reliably throw away
// the only part the model needs.
//
// It is an io.Writer rather than a post-hoc truncation so the bytes are never
// all resident at once -- which is the point of capping at source rather than
// downstream.
type tailBuffer struct {
	max  int
	buf  []byte
	lost int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.max > 0 && len(p) > b.max {
		b.lost += len(p) - b.max
		p = p[len(p)-b.max:]
	}
	b.buf = append(b.buf, p...)
	if b.max > 0 && len(b.buf) > b.max {
		drop := len(b.buf) - b.max
		b.lost += drop
		b.buf = b.buf[drop:]
	}
	// The full length is reported written: the command is not failing, its
	// output is being bounded, and an exec.Cmd that sees a short write kills the
	// process with io.ErrShortWrite instead.
	return n, nil
}

func (b *tailBuffer) String() string {
	if b.lost == 0 {
		return string(b.buf)
	}
	return fmt.Sprintf("[... %d earlier byte(s) of output dropped; this daemon keeps the last %d ...]\n%s",
		b.lost, b.max, string(b.buf))
}

// logSandboxExecBackend logs sandboxExecBackendSummary once at startup, when
// sandbox_exec can be called at all.
func (s *Server) logSandboxExecBackend() {
	if s.cfg == nil || !s.cfg.MCP.Enabled || s.cfg.MCP.Builtin.Disabled ||
		s.cfg.MCP.Builtin.policyFor("sandbox_exec") == PolicyDeny {
		return
	}
	s.logger.Printf("sandbox_exec: %s", s.sandboxExecBackendSummary())
}
