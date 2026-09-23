//go:build !linux

package main

import (
	"log"
	"os"

	"mochiii/daemon/mcp"
)

// The egress firewall is a no-op off Linux: it rides on the Landlock backend,
// which is Linux-only. See sandboxegress_linux.go.

type egressWiring struct{}

func maybeSetupEgress(*mcp.SandboxConfig, *log.Logger) *egressWiring { return nil }

func egressFilterUsable() bool { return false }

func (w *egressWiring) extraFile() *os.File { return nil }

func (w *egressWiring) close() {}
