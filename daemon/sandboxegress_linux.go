//go:build linux

package main

import (
	"context"
	"log"
	"os"

	"mochiii/daemon/mcp"

	"golang.org/x/sys/unix"
)

// The daemon side of the egress firewall for one sandbox_exec call: a socketpair
// whose helper end travels to the sandboxed helper (which hands its
// connect-trapping listener back over it), and a supervisor goroutine that owns
// the listener and answers every trapped connect until the call ends. The whole
// mechanism -- fd handoff, cross-process reads, connect-on-behalf -- is proven by
// mcp.EgressFilterUsable; this just wires it to a real command.

type egressWiring struct {
	daemonFd  int      // raw, blocking: the supervisor receives the listener here
	helperEnd *os.File // travels to the helper as its fd 3 (via cmd.ExtraFiles)
	cancel    context.CancelFunc
	lfdCh     chan int // the listener fd the goroutine received, for teardown
}

// maybeSetupEgress turns egress filtering on for cfg when the backend is Landlock
// and the firewall actually enforces here, and starts the supervisor. It returns
// nil (and leaves cfg untouched) otherwise -- today's disclosed-but-open network.
func maybeSetupEgress(cfg *mcp.SandboxConfig, logger *log.Logger) *egressWiring {
	if mcp.ResolveMode(*cfg) != mcp.SandboxLandlock || !mcp.EgressFilterUsable() {
		return nil
	}
	sp, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		logger.Printf("mcp: egress socketpair: %v (this command's network is unfiltered)", err)
		return nil
	}
	cfg.EgressFilter = true
	ctx, cancel := context.WithCancel(context.Background())
	w := &egressWiring{
		daemonFd:  sp[0],
		helperEnd: os.NewFile(uintptr(sp[1]), "egress-helper"),
		cancel:    cancel,
		lfdCh:     make(chan int, 1),
	}
	go func() {
		lfd, err := mcp.RecvListenerFD(w.daemonFd)
		if err != nil {
			w.lfdCh <- -1
			return
		}
		w.lfdCh <- lfd
		mcp.RunEgressSupervisor(ctx, lfd, false)
	}()
	return w
}

// egressFilterUsable reports whether the firewall enforces on this host. The
// prompt asks through here rather than mcp directly, because the whole mechanism
// is Linux-only and the prompt is built on every platform.
func egressFilterUsable() bool { return mcp.EgressFilterUsable() }

// extraFile is the helper end to add to the command's ExtraFiles, or nil.
func (w *egressWiring) extraFile() *os.File {
	if w == nil {
		return nil
	}
	return w.helperEnd
}

// close tears the firewall down for this call: stop the supervisor, close both
// socketpair ends (which also unblocks a supervisor still waiting for a listener
// the command never sent), and close the listener the goroutine received.
func (w *egressWiring) close() {
	if w == nil {
		return
	}
	w.cancel()
	_ = unix.Close(w.daemonFd)
	if w.helperEnd != nil {
		_ = w.helperEnd.Close()
	}
	if lfd := <-w.lfdCh; lfd >= 0 {
		_ = unix.Close(lfd)
	}
}
