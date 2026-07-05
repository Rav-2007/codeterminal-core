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
)

// HelperProcess manages the embedder helper subprocess end to end: spawning
// it, waiting for a readiness signal before it's used, health-checking it,
// restarting it within bounded retries if it dies unexpectedly, and
// guaranteeing a clean shutdown (no orphaned child process) when Stop is
// called.
//
// It never embeds anything itself — Embed only speaks the wire protocol in
// helperproto and forwards texts to whatever the helper process does with
// them, which in this step is a stub (see helper/embed_stub.go).
type HelperProcess struct {
	binPath string
	logger  *log.Logger

	maxRestarts   int
	restartDelay  time.Duration
	readyTimeout  time.Duration
	readyPollStep time.Duration
	stopGrace     time.Duration
	callTimeout   time.Duration

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
// it a socket path to listen on. It does not start anything yet — call
// Start.
func NewHelperProcess(binPath string, logger *log.Logger) *HelperProcess {
	return &HelperProcess{
		binPath:       binPath,
		logger:        logger,
		maxRestarts:   defaultHelperMaxRestarts,
		restartDelay:  defaultHelperRestartDelay,
		readyTimeout:  defaultHelperReadyTimeout,
		readyPollStep: defaultHelperReadyPollStep,
		stopGrace:     defaultHelperStopGrace,
		callTimeout:   defaultHelperCallTimeout,
		stopSignal:    make(chan struct{}),
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
	cmd := exec.Command(h.binPath, "--socket", h.socketPath)
	cmd.Stdout = &prefixedWriter{prefix: "[embedder-helper] ", out: os.Stderr}
	cmd.Stderr = &prefixedWriter{prefix: "[embedder-helper] ", out: os.Stderr}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting embedder helper %s: %w", h.binPath, err)
	}
	h.cmd = cmd
	return nil
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
// It does not interpret or validate the vectors' content — that's the
// caller's (and eventually the Embedder implementation's) job.
func (h *HelperProcess) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := h.call(ctx, helperproto.Request{Method: helperproto.MethodEmbed, Texts: texts})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("embedder helper returned an error: %s", resp.Error)
	}
	return resp.Vectors, nil
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
	return len(p), nil
}

// processAlive reports whether pid refers to a still-running process, using
// signal 0 (no-op: delivers nothing, just checks existence/permission).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
