// Command codeterminal-daemon is the long-running background process that
// holds the model API credentials and proxies prompts to it over a Unix
// domain socket. It never listens on a network port.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"codeterminal/protocol"
)

// staleSocketProbeTimeout bounds how long startup waits when checking
// whether an existing socket file has a live daemon behind it.
const staleSocketProbeTimeout = 500 * time.Millisecond

func main() {
	logger := log.New(os.Stderr, "codeterminal-daemon: ", log.LstdFlags)

	// "index", "retrieve", "download-model", "helper-smoketest", "skills",
	// and "edits" are one-shot subcommands, not flags: they run and exit,
	// deliberately separate from the long-running serve path below (which
	// they leave entirely untouched — none of them is invoked automatically
	// on daemon start or per-prompt). Checked before flag.Parse() because
	// the daemon's own flags (e.g. --config) don't apply to them.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "index":
			if err := runIndexCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		case "retrieve":
			if err := runRetrieveCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		case "download-model":
			if err := runDownloadModelCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		case "helper-smoketest":
			if err := runHelperSmoketestCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		case "skills":
			if err := runSkillsCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		case "edits":
			if err := runEditsCommand(os.Args[2:], logger); err != nil {
				logger.Fatal(err)
			}
			return
		}
	}

	configPath := flag.String("config", "./models.json", "path to models.json")
	modelOverride := flag.String("model", "", "override the resolved model slug (testing only; config is the source of truth)")
	systemPromptPath := flag.String("system-prompt", "daemon/prompts/system.txt", "path to the system prompt file")
	workspace := flag.String("workspace", ".", "workspace root containing an existing .codeterminal/index for retrieval-augmented context")
	noContext := flag.Bool("no-context", false, "disable automatic retrieval-augmented context injection (default: enabled)")
	debugContext := flag.Bool("debug-context", false, "additionally log the full content of every retrieved chunk (verbose)")
	noRerank := flag.Bool("no-rerank", false, "bypass file-class re-ranking; use raw vector-similarity order (A/B comparison, default: re-ranking enabled)")
	noScrub := flag.Bool("no-scrub", false, "disable heuristic scrubbing of secret-shaped text from the prompt before it's sent to the model API (default: scrubbing enabled)")
	flag.Parse()

	apiBase := os.Getenv("CODETERMINAL_API_BASE")
	apiKey := os.Getenv("CODETERMINAL_API_KEY")
	// CODETERMINAL_USE_PROXY names the "point apiBase at the managed proxy"
	// mode explicitly. In this mode apiKey is populated from
	// CODETERMINAL_MOCHIII_KEY (below) instead of CODETERMINAL_API_KEY, since
	// the proxy authenticates callers by a per-user Mochiii key and holds its
	// own OpenRouter key server-side. streamCompletion in provider.go is
	// unchanged: it already sends whatever apiKey it's given as
	// "Authorization: Bearer <apiKey>" and omits the header when apiKey == "".
	// Unset (the default), CODETERMINAL_USE_PROXY is a no-op: every existing
	// direct-to-OpenRouter deployment keeps behaving exactly as before.
	useProxy := os.Getenv("CODETERMINAL_USE_PROXY") == "true"
	if apiBase == "" {
		logger.Fatal("CODETERMINAL_API_BASE must be set")
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
		logger.Print("warning: CODETERMINAL_USE_PROXY is set but CODETERMINAL_API_KEY is also set; the key will still be sent to the proxy needlessly -- the proxy holds its own OpenRouter key. Unset CODETERMINAL_API_KEY when using a proxy.")
	case useProxy:
		apiKey = os.Getenv("CODETERMINAL_MOCHIII_KEY")
		if apiKey == "" {
			logger.Fatal("CODETERMINAL_USE_PROXY is set but CODETERMINAL_MOCHIII_KEY is empty; the proxy requires a Mochiii key")
		}
		logger.Printf("proxy mode: forwarding inference through %s with a Mochiii key", apiBase)
	case apiKey == "":
		logger.Print("warning: CODETERMINAL_API_KEY is not set; requests will be sent without an Authorization header")
	}

	cfg, err := LoadConfig(*configPath)
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

	systemPromptBytes, err := os.ReadFile(*systemPromptPath)
	if err != nil {
		logger.Fatalf("reading system prompt %s: %v", *systemPromptPath, err)
	}
	systemPrompt := string(systemPromptBytes)

	// absWorkspace (resolved and checked by validateWorkspace above) is passed
	// in place of the raw flag: setupRetrieval would only re-derive the same
	// absolute path, and threading the validated one keeps a single answer to
	// "which directory is this daemon grounded against" across retrieval, the
	// warn sink, and GroundingInfo.
	retrieval := setupRetrieval(cfg, absWorkspace, *noContext, logger, newActiveEmbedder)
	defer retrieval.Stop()

	memoryStore := setupMemoryStore(logger)
	if memoryStore != nil {
		defer memoryStore.Close()
	}

	if _, err := protocol.SocketDir(); err != nil {
		logger.Fatalf("creating runtime dir: %v", err)
	}
	socketPath := protocol.SocketPath()
	lockPath := protocol.LockPath()

	if err := reclaimStaleSocket(socketPath); err != nil {
		logger.Fatal(err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		logger.Fatalf("listening on %s: %v", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		logger.Fatalf("restricting socket permissions: %v", err)
	}

	lock := protocol.LockFile{SocketPath: socketPath, PID: os.Getpid()}
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
	logger.Printf("listening on %s (base=%s)", socketPath, apiBase)

	srv := &Server{
		apiBase:                 apiBase,
		apiKey:                  apiKey,
		cfg:                     cfg,
		modelOverride:           *modelOverride,
		systemPrompt:            systemPrompt,
		logger:                  logger,
		embedder:                retrieval.Embedder,
		store:                   retrieval.Store,
		lexicalStore:            retrieval.LexicalStore,
		retrievalTopK:           retrieval.TopK,
		contextBudgetChars:      retrieval.ContextBudgetChars,
		retrievalDisabledReason: retrieval.DisabledReason,
		debugContext:            *debugContext,
		rerankDisabled:          *noRerank || cfg.Retrieval.RerankDisabled,
		workspace:               absWorkspace,
		memory:                  memoryStore,
		// Durable warn-mode sink under the workspace's already-gitignored
		// .codeterminal state dir (same convention as index/ and backups/), so
		// the log-only fire-rate data survives daemon restarts instead of
		// vanishing with stderr. Local file only — no network egress.
		warnSink: newWarnSink(filepath.Join(absWorkspace, ".codeterminal", "logs", "warnmode.jsonl")),
	}
	go srv.Serve(ln)

	// Block here so cleanup runs exactly once, in this goroutine, instead of
	// racing a background goroutine's os.Exit against main() returning.
	sig := <-sigCh
	logger.Printf("received %s, shutting down", sig)
	ln.Close()
	os.Remove(socketPath)
	os.Remove(lockPath)
}

// reclaimStaleSocket checks whether a file already exists at path. If a
// daemon is actually listening there, it refuses to start. If the file is
// left over from a previous crash (nothing answers), it removes it so this
// startup isn't blocked.
func reclaimStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking existing socket %s: %w", path, err)
	}

	conn, err := net.DialTimeout("unix", path, staleSocketProbeTimeout)
	if err == nil {
		conn.Close()
		return fmt.Errorf("a daemon is already listening on %s; stop it before starting a new one", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("removing stale socket %s: %w", path, err)
	}
	return nil
}
