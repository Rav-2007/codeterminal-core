// Command mochiii-daemon is the long-running background process that
// holds the model API credentials and proxies prompts to it over a Unix
// domain socket. It never listens on a network port.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/hex"
	"mochiii/daemon/mcp"
	"mochiii/editapply"
	"mochiii/protocol"
)

// staleSocketProbeTimeout bounds how long startup waits when checking
// whether an existing socket file has a live daemon behind it.
const staleSocketProbeTimeout = 500 * time.Millisecond

func main() {
	// FIRST, before anything else runs: when this binary was started as the
	// sandbox_exec landlock helper it restricts itself and becomes the command,
	// and nothing below may have happened in it. See mcp.MaybeRunSandboxHelper.
	mcp.MaybeRunSandboxHelper()
	// Likewise first: when started as the instant-reaper it watches the death pipe
	// and kills its scope when the daemon dies -- it must never fall through into
	// the daemon's own startup. See sandboxreaper_linux.go.
	if len(os.Args) > 1 && os.Args[1] == sandboxReaperArg {
		os.Exit(sandboxReaperMain(os.Args[2:]))
	}

	logger := log.New(os.Stderr, "mochiii-daemon: ", log.LstdFlags)

	// THE ONE-SHOT SUBCOMMANDS run and exit, deliberately separate from the
	// long-running serve path below (which they leave entirely untouched -- none of
	// them is invoked automatically on daemon start or per-prompt). Checked before
	// flag.Parse() because the daemon's own flags (e.g. --config) don't apply to
	// them. The set lives in subcommands.go, which --help reads from the same
	// table, so a subcommand cannot be dispatched without being documented.
	if len(os.Args) > 1 {
		if sc, ok := findSubcommand(os.Args[1]); ok {
			if err := sc.run(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		}
	}

	configPath := flag.String("config", "", "path to models.json (default: next to the daemon binary, then ./models.json)")
	modelOverride := flag.String("model", "", "override the resolved model slug (testing only; config is the source of truth)")
	systemPromptPath := flag.String("system-prompt", "", "path to a system prompt file (default: the copy compiled into this binary)")
	workspace := flag.String("workspace", ".", "workspace root containing an existing .mochiii/index for retrieval-augmented context")
	noContext := flag.Bool("no-context", false, "disable automatic retrieval-augmented context injection (default: enabled)")
	debugContext := flag.Bool("debug-context", false, "additionally log the full content of every retrieved chunk (verbose)")
	noRerank := flag.Bool("no-rerank", false, "bypass file-class re-ranking; use raw vector-similarity order (A/B comparison, default: re-ranking enabled)")
	noScrub := flag.Bool("no-scrub", false, "disable heuristic scrubbing of secret-shaped text from the prompt before it's sent to the model API (default: scrubbing enabled)")
	logFile := flag.String("log-file", "", "additionally append the daemon log to this file (size-rotated at 5 MiB, one .1 backup); stderr is always written too")

	// --help NAMES THE SUBCOMMANDS, not just the flags. See subcommands.go for
	// why: the default flag usage listed flags only, so someone running --help to
	// find out how to supply an API key learned that no such thing existed.
	flag.Usage = func() { writeUsage(flag.CommandLine.Output(), filepath.Base(os.Args[0])) }
	flag.Parse()

	// Swapped in before anything else is logged, so a --log-file run captures
	// startup -- which is where the config warnings and degradation notices
	// are. Tee'd, never redirected: a foreground operator keeps stderr.
	if logWriter, closeLog, err := newLogWriter(*logFile); err != nil {
		logger.Printf("warning: could not open --log-file %s (%v); continuing with stderr only", *logFile, err)
	} else {
		defer closeLog()
		logger = log.New(logWriter, "mochiii-daemon: ", log.LstdFlags)
	}

	apiBase := os.Getenv("MOCHIII_API_BASE")
	apiKey := os.Getenv("MOCHIII_API_KEY")
	// MOCHIII_USE_PROXY names the "point apiBase at the managed proxy"
	// mode explicitly. In this mode apiKey is populated from
	// MOCHIII_PROXY_KEY (below) instead of MOCHIII_API_KEY, since
	// the proxy authenticates callers by a per-user Mochiii key and holds its
	// own OpenRouter key server-side. streamCompletion in provider.go is
	// unchanged: it already sends whatever apiKey it's given as
	// "Authorization: Bearer <apiKey>" and omits the header when apiKey == "".
	// Unset (the default), MOCHIII_USE_PROXY is a no-op: every existing
	// direct-to-OpenRouter deployment keeps behaving exactly as before.
	useProxy := os.Getenv("MOCHIII_USE_PROXY") == "true"

	// THE STORED CREDENTIAL FILLS WHAT THE ENVIRONMENT DID NOT SAY, AND NEVER
	// OVERRIDES IT. `mochiii-daemon connect` writes a key so a user does not have
	// to export one; an existing deployment that exports MOCHIII_API_KEY must keep
	// behaving exactly as it did, so the environment wins every time.
	//
	// NOT CONSULTED IN PROXY MODE, and that is a credential boundary rather than a
	// tidiness rule: what connect stores is a PROVIDER key, and proxy mode sends
	// its key to the proxy. Filling the proxy's key in from this file would send a
	// provider credential to a host it was not issued for -- the same mistake the
	// fatal guard below exists to prevent, arrived at from the other direction.
	if !useProxy && (apiKey == "" || apiBase == "") {
		if path, err := credentialsPath(); err == nil {
			stored, warn, loadErr := loadCredential(path)
			if loadErr != nil {
				logger.Printf("warning: %v", loadErr)
			}
			if warn != "" {
				logger.Printf("warning: %s", warn)
			}
			if apiKey == "" && stored.configured() {
				apiKey = stored.APIKey
				verified := "unverified"
				if stored.Verified {
					verified = "verified when saved"
				}
				// Masked, always: the daemon log is tee'd to --log-file and read
				// over shoulders, and a key that reaches it has left the file.
				logger.Printf("using the key stored by `connect` (%s, %s); set MOCHIII_API_KEY to override",
					maskKey(stored.APIKey), verified)
			}
			if apiBase == "" && strings.TrimSpace(stored.APIBase) != "" {
				apiBase = stored.APIBase
			}
		}
	}

	if apiBase == "" {
		// PROXY MODE IS THE EXCEPTION, AND THE REASON IS A CREDENTIAL.
		//
		// In proxy mode apiKey comes from MOCHIII_PROXY_KEY, and the
		// proxy's address is deployment-specific -- there is no public default
		// to guess. Falling back to defaultAPIBase here would send a Mochiii
		// key to OpenRouter, which is a credential going to a host it was not
		// issued for. "This will never work" is the right verdict for an
		// unaddressed proxy, so this half stays fatal.
		if useProxy {
			logger.Fatal("MOCHIII_USE_PROXY is set but MOCHIII_API_BASE is empty; the proxy's address cannot be guessed")
		}
		apiBase = defaultAPIBase
		logger.Printf("MOCHIII_API_BASE is unset; defaulting to %s (set it, or the Mochiii: API Base setting, to change it)", apiBase)
	}
	// Structural validity, checked before the daemon claims to be ready. An
	// unparseable base can never serve a request, so failing here — naming the
	// setting — beats starting and reporting every prompt as a transient
	// provider outage. Reachability is deliberately not probed; see
	// startup_validate.go for why that is a runtime state, not a startup error.
	if err := validateAPIBase(apiBase); err != nil {
		logger.Fatal(err)
	}
	switch {
	case useProxy && apiKey != "":
		logger.Print("warning: MOCHIII_USE_PROXY is set but MOCHIII_API_KEY is also set; the key will still be sent to the proxy needlessly -- the proxy holds its own OpenRouter key. Unset MOCHIII_API_KEY when using a proxy.")
	case useProxy:
		apiKey = os.Getenv("MOCHIII_PROXY_KEY")
		if apiKey == "" {
			logger.Fatal("MOCHIII_USE_PROXY is set but MOCHIII_PROXY_KEY is empty; the proxy requires a Mochiii key")
		}
		logger.Printf("proxy mode: forwarding inference through %s with a Mochiii key", apiBase)
	case apiKey == "":
		logger.Print("warning: MOCHIII_API_KEY is not set; requests will be sent without an Authorization header")
	}

	// An explicit -config is used as given; otherwise it is found relative to
	// this binary, so the daemon no longer has to be started from the repo root.
	resolvedConfigPath := *configPath
	if resolvedConfigPath == "" {
		resolvedConfigPath = resolveConfigPath()
	}
	cfg, err := LoadConfig(resolvedConfigPath)
	if err != nil {
		logger.Fatal(err)
	}
	// Non-fatal config problems — an unrecognized config_version, a misspelled
	// key whose setting is therefore not in effect, a value clamped into range.
	// None of these makes the file unservable, so none of them stops startup;
	// all of them used to be accepted in complete silence, which is how a typo
	// like "retreival" could discard a deliberate setting with nothing said.
	// Also carried to the status surface, so an operator who missed startup can
	// still ask.
	for _, w := range cfg.Warnings() {
		logger.Printf("config warning: %s", w)
	}

	if useProxy {
		// b5: Dynamic Model Allow-List Reconciliation
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cfg.ReconcileWithProxy(ctx, apiBase, apiKey); err != nil {
			logger.Printf("warning: proxy model reconciliation failed (will fail reactively if tier is unavailable): %v", err)
		} else {
			logger.Printf("reconciled allowed models with proxy")
			// Flush any new warnings generated by reconciliation.
			for _, w := range cfg.Warnings() {
				logger.Printf("config warning: %s", w)
			}
		}
	}

	// Refuse a workspace that is not a workspace (missing, or a regular file)
	// rather than starting and then blaming the missing INDEX on every prompt —
	// advice that could not work, since indexing a nonexistent path fails too.
	// A real directory with no index yet is not an error and does not come
	// through here; that stays a reported, serviceable degraded state.
	absWorkspace, err := validateWorkspace(*workspace)
	if err != nil {
		logger.Fatal(err)
	}

	model := cfg.ResolvedSlug()
	if *modelOverride != "" {
		model = *modelOverride
	}

	// --no-scrub OR's in on top of whatever models.json already says, same
	// combining convention as --no-rerank above cfg.Retrieval.RerankDisabled:
	// either source disabling scrubbing is enough to disable it.
	if *noScrub {
		cfg.NoScrub = true
	}
	if cfg.NoScrub {
		logger.Print("secret scrubbing DISABLED (--no-scrub)")
	}

	systemPrompt, err := resolveSystemPrompt(*systemPromptPath)
	if err != nil {
		logger.Fatal(err)
	}

	// CLAIM THE ADDRESS BEFORE ACQUIRING ANYTHING EXPENSIVE.
	//
	// This block used to sit BELOW setupRetrieval, and the order was the whole
	// bug. Binding is the cheapest step in startup and the one that decides
	// whether this process may run at all; doing it last meant a daemon that had
	// already lost spawned the embedder helper (81 MB, holding the BGE model) and
	// opened the SQLite conversation store first, and then exited through
	// logger.Fatal -- which is os.Exit(1), and os.Exit does not run deferred
	// functions. The helper was reparented and never collected.
	//
	// Measured: baseline 0 helpers, daemon A running 1, daemon B lost and exited
	// leaving 2; stopping A cleanly returned to 1, not 0. With the extension's
	// pre-200308e three-second retry, that was 81 MB orphaned every three
	// seconds for as long as the window stayed open. Reproduced in
	// daemon/startuprace_test.go.
	//
	// Nothing between here and the listener needs a model, a database or a
	// subprocess, so a daemon that loses now exits having allocated nothing.
	if _, err := protocol.SocketDir(); err != nil {
		logger.Fatalf("creating runtime dir: %v", err)
	}
	// PER WORKSPACE, not per user. Two VS Code windows on two repositories used
	// to collide here: the second daemon found the first listening, exited 1,
	// and the extension restarted it every three seconds forever -- while the
	// second window's client read the same per-user lockfile and was answered
	// by the FIRST window's daemon about the wrong repository. Reproduced in
	// daemon/twoworkspaces_test.go.
	//
	// The tag is derived from the RESOLVED root, not absWorkspace, and the
	// difference is load-bearing. validateWorkspace is filepath.Abs only; it
	// never calls EvalSymlinks. A workspace reached through a symlink -- /tmp on
	// macOS, ~/work -> /mnt/data/work on Linux, an 8.3 short name on Windows --
	// would otherwise produce a DIFFERENT tag from the one a client computes
	// from the same directory, and each would quietly start its own daemon for
	// one repository. Clients mirror this with realpath; see WorkspaceTag.
	realRoot, err := editapply.ResolveRealWorkspaceRoot(absWorkspace)
	if err != nil {
		// Not fatal: fall back to the per-user address rather than refuse to
		// start over a naming detail. One daemon that works beats none.
		logger.Printf("warning: could not resolve %s for the per-workspace socket name (%v); "+
			"falling back to the shared one, so a second workspace will not start", absWorkspace, err)
		realRoot = ""
	}
	addr := protocol.DefaultAddressFor(realRoot)
	lockPath := protocol.LockPathFor(realRoot)

	// ONE STRING FOR "WHICH DIRECTORY IS THIS DAEMON GROUNDED AGAINST", and it
	// is the resolved one.
	//
	// The resolved root used to key the socket and NOTHING ELSE: retrieval, the
	// warn sink, the tool audit log, the LSP bridge and the workspace this daemon
	// REPORTS all took absWorkspace, which is filepath.Abs only. Four lines above
	// this one, the comment already says so.
	//
	// That split is invisible until two spellings of one directory meet, which is
	// exactly what adopting a shared daemon made ordinary. Window A opens
	// /tmp/proj and starts the daemon; window B opens /private/tmp/proj, computes
	// the same tag (both resolve identically), and adopts A's daemon -- as
	// designed. B then sends its own spelling with every prompt, buildGroundingInfo
	// compares it lexically against A's, and every single turn comes back flagged
	// WORKSPACE MISMATCH about a daemon serving precisely the right directory.
	//
	// macOS makes this the DEFAULT case rather than an edge one: /tmp and /var are
	// symlinks into /private, so the two spellings differ for any workspace under
	// either. Caught by twoworkspaces_test.go on the first macOS run this
	// repository has ever had.
	//
	// Falls back to absWorkspace only when resolution failed, where realRoot is ""
	// and a per-user address is already being used -- the same "one daemon that
	// works beats none" trade made above.
	groundedRoot := absWorkspace
	if realRoot != "" {
		groundedRoot = realRoot
	}

	if err := reclaimStaleSocket(addr); err != nil {
		// Losing is not failing. Another daemon already serves this exact
		// workspace, which is what SHOULD happen when a second window opens the
		// same repository -- one root, one index, one daemon. Say so with a code
		// a supervisor can act on rather than the generic 1 that an unreadable
		// models.json also produces; see exitcodes.go for the contract.
		if errors.Is(err, errAlreadyRunning) {
			logger.Print(err)
			os.Exit(exitAlreadyRunning)
		}
		logger.Fatal(err)
	}

	// Listen owns the platform difference, including the 0600 chmod a Unix
	// socket needs and a named pipe has no equivalent of -- see
	// protocol/transport_unix.go for why that step lives there and not here.
	ln, err := protocol.Listen(addr)
	if err != nil {
		// THE RACE HALF, and the reason reclaimStaleSocket above is not enough.
		// That probe closes the SEQUENTIAL window -- a daemon already up when
		// this one started. It cannot close the CONCURRENT one: two windows
		// opening the same repo at once both probe, both find nothing, and both
		// reach this line, where the kernel arbitrates. Exactly one binds; the
		// other gets EADDRINUSE, and it lost the same race for the same reason,
		// so it exits the same way.
		//
		// Without this the concurrent loser exits 1 and gets counted against the
		// supervisor's restart budget -- an ordinary two-window startup burning
		// attempts on a daemon that was never broken.
		if isAddrInUse(err) {
			logger.Printf("%v (%v)", errAlreadyRunning, err)
			os.Exit(exitAlreadyRunning)
		}
		logger.Fatalf("listening on %s: %v", addr, err)
	}

	// EXPENSIVE SETUP STARTS HERE, and only here, because the address is now
	// held: from this point on losing the race is impossible, so every resource
	// below is acquired by the daemon that will actually use it.
	//
	// The listener existing does not mean the daemon is serving -- nothing is
	// accepted until `go srv.Serve(ln)` far below. A client that connects during
	// the model load waits in the accept backlog rather than being refused,
	// which is the behaviour we want and the reason binding early is safe.
	//
	// groundedRoot, not the raw flag and not absWorkspace: see its declaration
	// above. This comment used to say absWorkspace was "resolved and checked by
	// validateWorkspace", which is not true and is contradicted a few lines
	// earlier in this same file -- validateWorkspace is filepath.Abs only. The
	// single answer to "which directory is this daemon grounded against" is now
	// actually single, and actually resolved.
	retrieval := setupRetrieval(cfg, groundedRoot, *noContext, logger, newActiveEmbedder)
	defer retrieval.Stop()

	memoryStore := setupMemoryStore(logger)
	if memoryStore != nil {
		defer memoryStore.Close()
	}

	var tcpToken string
	if addr.Transport == protocol.TransportTCP {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			logger.Fatalf("generating tcp token: %v", err)
		}
		tcpToken = hex.EncodeToString(b)
		dir, _ := protocol.SocketDir()
		tokenPath := filepath.Join(dir, "tcp_token")
		if err := writeFileNoFollow(tokenPath, []byte(tcpToken), 0600); err != nil {
			logger.Fatalf("writing tcp token %s: %v", tokenPath, err)
		}
	}

	lock := protocol.NewLockFile(addr, os.Getpid())
	lockBytes, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		logger.Fatalf("encoding lockfile: %v", err)
	}
	// O_NOFOLLOW: lockPath is a fixed runtime path, but refuse to write the
	// lockfile (which carries the socket path clients trust) through a symlink
	// pre-planted at that name. Truncates a stale regular lockfile as before.
	if err := writeFileNoFollow(lockPath, lockBytes, 0600); err != nil {
		logger.Fatalf("writing lockfile %s: %v", lockPath, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Printf("tier=%s slug=%s", cfg.DefaultTier, model)
	logger.Printf("listening on %s (base=%s)", addr, apiBase)

	// Cancelled the moment a shutdown signal arrives, BEFORE the drain wait
	// begins. Only the agent loop consults it, and only between steps: a turn
	// stops starting new tool calls as soon as this fires, which is what keeps
	// toolDrainGrace bounded to the single call already in flight rather than
	// to however many the model would have asked for next.
	shutdownCtx, beginShutdown := context.WithCancel(context.Background())
	defer beginShutdown()

	srv := &Server{
		shutdownCtx:             shutdownCtx,
		apiBase:                 apiBase,
		apiKey:                  apiKey,
		cfg:                     cfg,
		modelOverride:           *modelOverride,
		systemPrompt:            systemPrompt,
		tcpToken:                tcpToken,
		logger:                  logger,
		embedder:                retrieval.Embedder,
		store:                   retrieval.Store,
		lexicalStore:            retrieval.LexicalStore,
		retrievalTopK:           retrieval.TopK,
		contextBudgetChars:      retrieval.ContextBudgetChars,
		retrievalDisabledReason: retrieval.DisabledReason,
		debugContext:            *debugContext,
		rerankDisabled:          *noRerank || cfg.Retrieval.RerankDisabled,
		workspace:               groundedRoot,
		memory:                  memoryStore,
		// Index-freshness sweeps for the status handler, memoised. Constructed
		// here rather than lazily so a running daemon's behaviour does not
		// depend on whether status has been called before.
		//
		// 30s: long enough that polling status in a loop cannot turn a health
		// check into load, short enough that a human who re-indexes and asks
		// again sees the new answer rather than a cached complaint.
		freshness: newFreshnessCache(30 * time.Second),
		// Durable warn-mode sink under the workspace's already-gitignored
		// .mochiii state dir (same convention as index/ and backups/), so
		// the log-only fire-rate data survives daemon restarts instead of
		// vanishing with stderr. Local file only — no network egress.
		warnSink: newWarnSink(filepath.Join(groundedRoot, ".mochiii", "logs", "warnmode.jsonl")),
		// The tool-call audit log, alongside it and under the same discipline:
		// local file only, no network seam. Always constructed, not just when
		// mcp.enabled -- a daemon that starts with agent mode off and has it
		// turned on later must not be the one daemon whose calls went
		// unrecorded, and an unused sink writes nothing.
		toolAudit: newToolAuditSink(filepath.Join(groundedRoot, ".mochiii", "logs", "toolcalls.jsonl")),
		// Activity counters, reported through the existing status surface (see
		// counters.go). Built here rather than lazily so production always has
		// them; a nil set is valid and simply counts nothing.
		counters:  &counters{},
		lspBridge: NewLSPBridge(groundedRoot),
	}
	// One line per reduced subsystem, so the log and the wire agree about what
	// is degraded from the moment the daemon starts serving.
	srv.logDegradations()

	// Set before Serve, so the idle janitor -- started by the first language
	// server spawn, which only a served request can cause -- can only ever see
	// it already assigned.
	srv.lspBridge.logf = logger.Printf

	srv.startWorkspaceWatcher()
	// Which sandbox sandbox_exec will get, and why -- in the background, because
	// finding out runs a probe or two and startup waits for neither.
	go srv.logSandboxExecBackend()
	// Housekeeping for the sandbox_exec cache (register item 37): once, in the
	// background, and never allowed to delay or fail startup.
	go srv.reclaimSandboxHomes()
	// Reap any landlock sandbox scope a force-killed earlier daemon left running
	// (register item 42): also once, in the background, also never fatal.
	go srv.reapOrphanedSandboxScopes()
	go srv.Serve(ln)

	// Block here so cleanup runs exactly once, in this goroutine, instead of
	// racing a background goroutine's os.Exit against main() returning.
	sig := <-sigCh
	logger.Printf("received %s, shutting down", sig)

	// Told first, so an agent turn stops queueing further tool calls while the
	// listener is still being closed below.
	beginShutdown()

	// Stop accepting first, so the in-flight set stops growing and Serve's Accept
	// loop returns. Only then is there a fixed set of connections to wait for.
	ln.Close()

	// Wait for in-flight work. Without this, main returned here and the process
	// exited from under every handler goroutine mid-request.
	//
	// The grace is deliberately SHORT, and sized by what a cut request can
	// actually damage rather than by how long a request can take. Everything that
	// mutates the filesystem -- Apply's multi-file write plus its backup session,
	// Undo's restore -- is local disk I/O measured in milliseconds, so a few
	// seconds is generous for the cases where being cut leaves real mess behind
	// (a half-applied batch, a half-populated backup dir). A streaming prompt can
	// legitimately run for minutes, and waiting that out would make Ctrl-C feel
	// broken; a cut prompt mutates nothing and costs the user a re-ask, so it is
	// the right thing to abandon.
	if !srv.WaitForDrain(shutdownGrace) {
		logger.Printf("drain INCOMPLETE after %s -- a request was still running and is being cut. If it was an edit apply, the batch may be partly written; the backup session under .mochiii/backups is still there and `undo` can revert it", shutdownGrace)
	} else {
		logger.Print("drain complete, no requests in flight")
	}

	// A Unix socket is a filesystem entry and must be unlinked; a named pipe is
	// a kernel object that disappears with its last handle, so there is nothing
	// to remove and Remove would fail on a path that was never a file.
	if addr.Transport == protocol.TransportUnix {
		os.Remove(addr.Address)
	}
	os.Remove(lockPath)

	srv.lspBridge.Close()
}

// reclaimStaleSocket checks whether a file already exists at path. If a
// daemon is actually listening there, it refuses to start. If the file is
// left over from a previous crash (nothing answers), it removes it so this
// startup isn't blocked.
func reclaimStaleSocket(addr protocol.Address) error {
	// A named pipe leaves NO residue when its owner dies: it lives in the kernel
	// object namespace, not the filesystem, so there is nothing to stat and
	// nothing to unlink. "Stale" there means only "nothing answers", and the
	// bind itself is what refuses if something does -- go-winio creates the
	// first instance with FILE_CREATE. So the probe-and-remove dance below is
	// Unix-only, and skipping it here is not a gap.
	if addr.Transport == protocol.TransportNamedPipe {
		conn, err := protocol.DialTimeout(addr, staleSocketProbeTimeout)
		if err == nil {
			// Close failing changes nothing: the probe already answered the only
			// question asked, which is that somebody is listening.
			_ = conn.Close()
			return fmt.Errorf("%w: it is listening on %s", errAlreadyRunning, addr)
		}
		return nil
	}

	path := addr.Address
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking existing socket %s: %w", path, err)
	}

	conn, err := protocol.DialTimeout(addr, staleSocketProbeTimeout)
	if err == nil {
		// As above: the probe has already answered.
		_ = conn.Close()
		return fmt.Errorf("%w: it is listening on %s", errAlreadyRunning, path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing stale socket %s: %w", path, err)
	}
	return nil
}
