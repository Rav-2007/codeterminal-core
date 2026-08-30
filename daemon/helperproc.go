package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"codeterminal/helper/helperproto"
)

// Lifecycle tuning. Exported as fields (with these as defaults) rather than
// bare constants so tests can shrink them and stay fast — see
// helperproc_test.go.
const (
	defaultHelperMaxRestarts   = 3
	defaultHelperRestartDelay  = 1 * time.Second
	defaultHelperReadyTimeout  = 5 * time.Second
	defaultHelperReadyPollStep = 50 * time.Millisecond
	defaultHelperStopGrace     = 2 * time.Second
	defaultHelperCallTimeout   = 10 * time.Second

	// Added per TEXT on a batched Embed, on top of defaultHelperCallTimeout.
	//
	// One deadline cannot serve both shapes of call this helper receives. A live
	// query embeds ONE short string and 10s is already generous. An index build
	// embeds indexEmbedBatchSize (40) chunks in a single request, which is not
	// forty times more urgent, it is forty times more work -- and the FIRST batch
	// also pays the ONNX session's cold start.
	//
	// Measured 2026-08-30: 4,704 chunks in 7m36.7s on a 12-core machine is ~97ms
	// per chunk, so a 40-chunk batch is ~4s there and several times that on a
	// small CI runner. It went over 10s on a github-hosted ubuntu-latest and
	// failed a run outright (`i/o timeout` on run 33321602030) -- and that is NOT
	// an eval-only fault: newActiveEmbedder gives the real `index` command and
	// the daemon's live retrieval this same default, so `index` on a slow or
	// loaded machine could fail the same way.
	//
	// 1s per text is deliberately far above measured cost. A deadline here exists
	// to catch a HUNG helper, not to bound legitimate work, so it should sit well
	// clear of what the work actually takes; the cost of being wrong in the tight
	// direction is a user's index failing, and in the loose direction is waiting
	// longer to notice a hang.
	defaultHelperPerTextTimeout = 1 * time.Second
)

// HelperProcess manages the embedder helper subprocess end to end: spawning
// it, waiting for a readiness signal before it's used, health-checking it,
// restarting it within bounded retries if it dies unexpectedly, and
// guaranteeing a clean shutdown (no orphaned child process) when Stop is
// called.
//
// It never embeds anything itself — Embed only speaks the wire protocol in
// helperproto and forwards texts to whatever the helper process does with
// them (real ONNX inference — see helper/onnxembedder.go). BgeEmbedder is
// the layer that turns this into an Embedder.
type HelperProcess struct {
	binPath        string
	modelDir       string // passed to the helper as --model-dir; empty means omit the flag
	onnxRuntimeLib string // passed to the helper as --onnxruntime-lib; empty means omit the flag
	logger         *log.Logger

	// extraEnv is appended to helperEnv()'s minimal allowlist when spawning
	// the helper. Always nil in production (see NewHelperProcess) -- it
	// exists solely so tests can hand the fake helper fixture a var like
	// FAKEHELPER_FAIL without widening what the real helper receives (see
	// helperproc_test.go).
	extraEnv []string

	maxRestarts   int
	restartDelay  time.Duration
	readyTimeout  time.Duration
	readyPollStep time.Duration
	stopGrace     time.Duration
	callTimeout   time.Duration
	// perTextTimeout is added per text beyond the first on a batched Embed.
	perTextTimeout time.Duration

	mu         sync.Mutex
	cmd        *exec.Cmd
	socketPath string
	stopping   bool
	restarts   int
	doneCh     chan struct{} // closed by monitor() when it returns for good

	// stopSignal is closed exactly once, by Stop, so that a waitReady call
	// blocked mid-poll for an in-flight restart notices immediately instead
	// of blindly polling for the rest of readyTimeout. Without this, Stop
	// could be blocked far longer than stopGrace if it happens to race with
	// a restart attempt that's already in its readiness-polling loop.
	stopSignal chan struct{}
}

// NewHelperProcess returns a HelperProcess that will spawn binPath, passing
// it a socket path to listen on plus modelDir/onnxRuntimeLib (only if
// non-empty — tests exercising lifecycle behavior against a fake helper
// that doesn't load a real model pass "" for both, so those flags are
// simply omitted rather than passed empty). It does not start anything
// yet — call Start.
func NewHelperProcess(binPath, modelDir, onnxRuntimeLib string, logger *log.Logger) *HelperProcess {
	return &HelperProcess{
		binPath:        binPath,
		modelDir:       modelDir,
		onnxRuntimeLib: onnxRuntimeLib,
		logger:         logger,
		maxRestarts:    defaultHelperMaxRestarts,
		restartDelay:   defaultHelperRestartDelay,
		readyTimeout:   defaultHelperReadyTimeout,
		readyPollStep:  defaultHelperReadyPollStep,
		stopGrace:      defaultHelperStopGrace,
		callTimeout:    defaultHelperCallTimeout,
		perTextTimeout: defaultHelperPerTextTimeout,
		stopSignal:     make(chan struct{}),
	}
}

// Restarts reports how many times the helper has been automatically
// restarted after an unexpected exit.
func (h *HelperProcess) Restarts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.restarts
}

// Start spawns the helper, waits for it to report healthy, and begins
// monitoring it in the background for unexpected exits (see monitor). It
// returns once the helper is confirmed ready to accept calls.
func (h *HelperProcess) Start() error {
	socketPath, err := helperproto.SocketPath(os.Getpid())
	if err != nil {
		return fmt.Errorf("resolving embedder helper socket path: %w", err)
	}

	h.mu.Lock()
	h.socketPath = socketPath
	if err := h.spawnLocked(); err != nil {
		h.mu.Unlock()
		return err
	}
	cmd := h.cmd
	h.mu.Unlock()

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	if err := h.waitReady(); err != nil {
		_ = cmd.Process.Kill()
		<-exitCh

		// PUT THE STRUCT BACK THE WAY IT WAS, and this is not tidiness.
		//
		// doneCh is created below, AFTER this point, because it is closed by
		// monitor and monitor only starts once the helper is healthy. Returning
		// here left h.cmd set and h.doneCh nil — and Stop's "was this ever
		// started?" guard is `h.cmd == nil`. So Stop would clear that guard,
		// SIGTERM a process already killed and reaped two lines above, wait out
		// stopGrace, and then receive on a nil channel, which blocks FOREVER. A
		// helper that failed to start would hang daemon shutdown.
		//
		// Creating doneCh earlier does not fix it: nobody would ever close it,
		// so Stop would block on it just the same. The honest state after a
		// failed start is the state before the start — there is no process, the
		// one we spawned has been killed and Wait()ed, and Stop's no-op path is
		// exactly right for that.
		h.mu.Lock()
		h.cmd = nil
		h.mu.Unlock()
		// The helper may have bound its socket before dying. Stop does this on
		// the path we are deliberately no longer taking, so do it here.
		_ = os.Remove(socketPath)
		return err
	}

	h.doneCh = make(chan struct{})
	go h.monitor(exitCh)
	return nil
}

// spawnLocked execs a fresh copy of the helper binary. Callers must hold
// h.mu and must arrange for the resulting h.cmd's Wait() to be read exactly
// once (by the caller for the first spawn, by monitor for every restart).
func (h *HelperProcess) spawnLocked() error {
	args := []string{"--socket", h.socketPath}
	if h.modelDir != "" {
		args = append(args, "--model-dir", h.modelDir)
	}
	if h.onnxRuntimeLib != "" {
		args = append(args, "--onnxruntime-lib", h.onnxRuntimeLib)
	}

	cmd := exec.Command(h.binPath, args...)
	cmd.Env = append(helperEnv(), h.extraEnv...)
	cmd.Stdout = &prefixedWriter{prefix: "[embedder-helper] ", out: os.Stderr}
	cmd.Stderr = &prefixedWriter{prefix: "[embedder-helper] ", out: os.Stderr}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting embedder helper %s: %w", h.binPath, err)
	}
	h.cmd = cmd
	return nil
}

// helperEnv returns the minimal environment the embedder helper subprocess
// needs. The helper takes every real input via flags and absolute paths
// (--socket, --model-dir, --onnxruntime-lib -- see helper/main.go) and reads
// no environment variables itself, so this deliberately does NOT inherit the
// daemon's full environment (exec.Cmd's default when Env is left nil): that
// would hand the helper OPENROUTER_API_KEY / CODETERMINAL_API_KEY /
// CODETERMINAL_MOCHIII_KEY for no reason -- it never touches any of them.
// Only PATH and HOME are passed through, and only if the daemon itself has
// them set: standard baseline vars a Unix subprocess (and the Go runtime /
// cgo / dynamic linker underneath it) can reasonably expect.
func helperEnv() []string {
	var env []string
	for _, name := range []string{"PATH", "HOME"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// monitor watches the currently-running helper for an unexpected exit. If
// one happens and Stop hasn't been called, it respawns the helper (after a
// fixed delay, to avoid hot-looping a helper that fails immediately every
// time) up to maxRestarts times before giving up for good. It closes
// h.doneCh exactly once, on its way out, however it exits.
func (h *HelperProcess) monitor(exitCh chan error) {
	defer close(h.doneCh)

	for {
		exitErr := <-exitCh

		h.mu.Lock()
		if h.stopping {
			h.mu.Unlock()
			return
		}
		h.restarts++
		restarts := h.restarts
		h.mu.Unlock()

		if restarts > h.maxRestarts {
			h.logger.Printf("embedder helper exited (%v) and exceeded %d restart attempts; giving up", exitErr, h.maxRestarts)
			return
		}
		h.logger.Printf("embedder helper exited unexpectedly (%v); restarting (attempt %d/%d)", exitErr, restarts, h.maxRestarts)

		time.Sleep(h.restartDelay)

		h.mu.Lock()
		if h.stopping {
			h.mu.Unlock()
			return
		}
		spawnErr := h.spawnLocked()
		cmd := h.cmd
		h.mu.Unlock()

		if spawnErr != nil {
			h.logger.Printf("embedder helper restart failed to spawn: %v", spawnErr)
			// Feed this failure back through the same bound-checked loop
			// rather than giving up after a single failed attempt: an
			// unbounded number of consecutive spawn failures is exactly the
			// hot-loop this policy exists to prevent, so it must be counted
			// against maxRestarts too, not treated as an immediate abort.
			exitCh = make(chan error, 1)
			exitCh <- spawnErr
			continue
		}

		newExitCh := make(chan error, 1)
		go func() { newExitCh <- cmd.Wait() }()

		if err := h.waitReady(); err != nil {
			h.logger.Printf("embedder helper restart did not become healthy: %v", err)
			_ = cmd.Process.Kill()
			// Same reasoning as above: a restart that never becomes healthy
			// counts as a failed attempt against the bound, not a
			// unilateral decision to stop trying. The top of the loop will
			// block on newExitCh, which resolves as soon as Kill reaps it.
			exitCh = newExitCh
			continue
		}
		h.logger.Printf("embedder helper restarted and healthy (attempt %d/%d)", restarts, h.maxRestarts)
		exitCh = newExitCh
	}
}

// waitReady polls Health until it succeeds, readyTimeout elapses, or Stop is
// called (via stopSignal) — without that last check, a Stop racing an
// in-flight restart's readiness poll would block for up to readyTimeout
// instead of the intended stopGrace, since this loop would otherwise have
// no way to notice the process it's polling for was just torn out from
// under it. Readiness itself is defined purely by the wire protocol (a
// successful health response), not by scraping the helper's logs.
func (h *HelperProcess) waitReady() error {
	deadline := time.Now().Add(h.readyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-h.stopSignal:
			return fmt.Errorf("stop requested while waiting for the embedder helper to become healthy")
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), h.readyPollStep)
		lastErr = h.Health(ctx)
		cancel()
		if lastErr == nil {
			return nil
		}
		time.Sleep(h.readyPollStep)
	}
	return fmt.Errorf("embedder helper did not become healthy within %s: %w", h.readyTimeout, lastErr)
}

// Health asks the helper if it's alive and dispatching correctly.
func (h *HelperProcess) Health(ctx context.Context) error {
	resp, err := h.call(ctx, helperproto.Request{Method: helperproto.MethodHealth})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("embedder helper reported unhealthy: %s", resp.Error)
	}
	return nil
}

// Embed sends texts to the helper and returns whatever vectors it computes.
// It does not interpret the vectors' content — that's the caller's (and
// eventually the Embedder implementation's) job — but it DOES enforce the one
// structural contract the whole pipeline relies on: exactly one vector per
// input text. This is the single point where untrusted subprocess output
// crosses into the daemon, and every downstream consumer indexes the result
// positionally (retrieveTopK's vecs[0], buildIndex/reindexFile's vecs[i])
// without re-checking. A helper that returned fewer vectors than texts — a
// bug, a partial failure, or a swapped-in embedder that ignores the contract —
// would otherwise reach an out-of-range panic in whichever handler ran, which
// (until handleConn's recover backstop) took the whole daemon down. The real
// ONNX helper cannot produce this (helper/onnxembedder.go returns exactly
// len(texts) vectors or an error), so in practice this only fires for a
// misbehaving helper; validating here is cheap and turns that class of fault
// into a clean, contained error instead of a crash (C1).
func (h *HelperProcess) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	// Size the deadline to the batch before call() falls back to its
	// one-request-shaped default. A caller that set its own deadline keeps it:
	// call() only supplies one when the context has none, and so does this.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.embedDeadline(len(texts)))
		defer cancel()
	}
	resp, err := h.call(ctx, helperproto.Request{Method: helperproto.MethodEmbed, Texts: texts})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("embedder helper returned an error: %s", resp.Error)
	}
	if len(resp.Vectors) != len(texts) {
		return nil, fmt.Errorf("embedder helper returned %d vectors for %d input texts; refusing a malformed response", len(resp.Vectors), len(texts))
	}
	return resp.Vectors, nil
}

// embedDeadline is how long a batch of n texts is allowed to take: the
// one-call budget, plus a per-text allowance for everything beyond the first.
//
// A single text is unchanged at callTimeout, so interactive latency behaves
// exactly as it did -- this only ever LENGTHENS the deadline, and only for
// calls that are doing proportionally more work.
func (h *HelperProcess) embedDeadline(n int) time.Duration {
	if n <= 1 {
		return h.callTimeout
	}
	return h.callTimeout + time.Duration(n-1)*h.perTextTimeout
}

// call performs exactly one request/response round trip: dial, encode,
// decode, close. There is no persistent connection to manage.
func (h *HelperProcess) call(ctx context.Context, req helperproto.Request) (helperproto.Response, error) {
	h.mu.Lock()
	socketPath := h.socketPath
	h.mu.Unlock()

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.callTimeout)
		defer cancel()
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return helperproto.Response{}, fmt.Errorf("dialing embedder helper: %w", err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return helperproto.Response{}, fmt.Errorf("sending request to embedder helper: %w", err)
	}

	var resp helperproto.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return helperproto.Response{}, fmt.Errorf("reading response from embedder helper: %w", err)
	}
	return resp, nil
}

// Stop terminates the helper and blocks until it's confirmed gone: SIGTERM
// first, then SIGKILL if it hasn't exited within stopGrace. It does not
// return until the monitor goroutine has observed the exit (i.e. the
// process has been Wait()'d and reaped — no zombie, no orphan), and it
// disables any further automatic restart. Calling Stop before Start, or
// twice, is a safe no-op.
func (h *HelperProcess) Stop() error {
	h.mu.Lock()
	if h.stopping || h.cmd == nil {
		h.stopping = true
		h.mu.Unlock()
		return nil
	}
	h.stopping = true
	close(h.stopSignal)
	cmd := h.cmd
	socketPath := h.socketPath
	doneCh := h.doneCh
	h.mu.Unlock()

	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-doneCh:
	case <-time.After(h.stopGrace):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-doneCh
	}

	os.Remove(socketPath)
	return nil
}

// maxLogLineBytes caps how much of a single newline-free run this writer will
// hold before emitting it anyway.
//
// A line buffer that only drains on a newline is bounded by the writer's
// politeness, and the writer here is a subprocess. The helper is this project's
// own code and would have to malfunction to reach this; the MCP variant of the
// same writer (mcpruntime.go) reads an unconfined third-party process's stderr,
// where "writes megabytes without a newline" is not a malfunction but an input.
// Both are capped, at the same place and for the same reason.
//
// 64 KiB is far above any real log line and far below a size worth worrying
// about, and what happens at the cap is a flush rather than a drop: the bytes
// were going to be logged anyway, they just stop accumulating first.
const maxLogLineBytes = 64 << 10

// prefixedWriter prefixes every line written to it before forwarding to out.
// Used to tag the helper's stdout/stderr in the daemon's own log stream.
type prefixedWriter struct {
	prefix string
	out    *os.File

	mu  sync.Mutex
	buf []byte
}

func (w *prefixedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		fmt.Fprintf(w.out, "%s%s\n", w.prefix, line)
		w.buf = w.buf[i+1:]
	}
	// No newline in sight and the buffer has grown past what a log line can
	// reasonably be: emit it and start again, rather than holding it forever.
	if len(w.buf) >= maxLogLineBytes {
		_, _ = fmt.Fprintf(w.out, "%s%s [continues]\n", w.prefix, w.buf)
		w.buf = w.buf[:0]
	}
	return len(p), nil
}
