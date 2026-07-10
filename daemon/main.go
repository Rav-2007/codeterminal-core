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
	flag.Parse()

	apiBase := os.Getenv("CODETERMINAL_API_BASE")
	apiKey := os.Getenv("CODETERMINAL_API_KEY")
	// CODETERMINAL_USE_PROXY names the already-existing "point apiBase at a
	// proxy and leave apiKey empty" mode explicitly (see streamCompletion in
	// provider.go, which already omits the Authorization header whenever
	// apiKey == "") -- it changes nothing about request behavior, only which
	// startup log line is printed, so an operator can tell "intentionally
	// proxied" apart from "forgot to set the key" at a glance. Unset (the
	// default), it's a no-op: every existing direct-to-OpenRouter deployment
	// keeps behaving exactly as before.
	useProxy := os.Getenv("CODETERMINAL_USE_PROXY") == "true"
	if apiBase == "" {
		logger.Fatal("CODETERMINAL_API_BASE must be set")
	}
	switch {
	case useProxy && apiKey != "":
		logger.Print("warning: CODETERMINAL_USE_PROXY is set but CODETERMINAL_API_KEY is also set; the key will still be sent to the proxy needlessly -- the proxy holds its own OpenRouter key. Unset CODETERMINAL_API_KEY when using a proxy.")
	case useProxy:
		logger.Printf("proxy mode: forwarding inference through %s; this daemon holds no model API key", apiBase)
	case apiKey == "":
		logger.Print("warning: CODETERMINAL_API_KEY is not set; requests will be sent without an Authorization header")
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Fatal(err)
	}
	model := cfg.ResolvedSlug()
	if *modelOverride != "" {
		model = *modelOverride
	}

	systemPromptBytes, err := os.ReadFile(*systemPromptPath)
	if err != nil {
		logger.Fatalf("reading system prompt %s: %v", *systemPromptPath, err)
	}
	systemPrompt := string(systemPromptBytes)

	embedder, store, lexicalStore, stopRetrieval, retrievalTopK, contextBudgetChars := setupRetrieval(
		cfg, *workspace, *noContext, logger, newActiveEmbedder)
	defer stopRetrieval()

	memoryStore := setupMemoryStore(logger)
	if memoryStore != nil {
		defer memoryStore.Close()
	}

	// Resolved independently of setupRetrieval (which does the same Abs
	// call internally but doesn't expose it) purely so it can be reported
	// to clients via GroundingInfo even when retrieval itself is disabled.
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		absWorkspace = *workspace // best-effort label; setupRetrieval already disabled retrieval in this case
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
	if err := os.WriteFile(lockPath, lockBytes, 0600); err != nil {
		logger.Fatalf("writing lockfile %s: %v", lockPath, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Printf("tier=%s slug=%s", cfg.DefaultTier, model)
	logger.Printf("listening on %s (base=%s)", socketPath, apiBase)

	srv := &Server{
		apiBase:            apiBase,
		apiKey:             apiKey,
		cfg:                cfg,
		modelOverride:      *modelOverride,
		systemPrompt:       systemPrompt,
		logger:             logger,
		embedder:           embedder,
		store:              store,
		lexicalStore:       lexicalStore,
		retrievalTopK:      retrievalTopK,
		contextBudgetChars: contextBudgetChars,
		debugContext:       *debugContext,
		rerankDisabled:     *noRerank || cfg.Retrieval.RerankDisabled,
		workspace:          absWorkspace,
		memory:             memoryStore,
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
