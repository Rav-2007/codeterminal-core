package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `--config` IS A CODE-EXECUTION ARGUMENT, not a display option.
//
// `mcp list` does not merely read a config file. Its own usage text says
// "Starts the configured MCP servers to ask them, and shuts them down again",
// and buildRegistry does exactly that. So whoever controls the config controls
// what runs.
//
// /mcp-server passed "./models.json" -- relative, therefore resolved against
// this process's working directory, which for the TUI is the repository the
// user opened. CONFIRMED BY EXECUTION 2026-08-08: a workspace models.json
// naming a script had that script run, with acknowledged_unconfined set to true
// by the attacker in the same file that is supposed to require a human to type
// it.
//
// This is the FOURTH instance of one class in these clients, and the second of
// this exact config-shaped variant -- the VS Code client's copy was fixed hours
// earlier in b7e393d and this one was not carried along, which is the same way
// the /mcp-server binary hijack survived d56e425.
//
// NEUTER CHECK: remove the filepath.IsAbs guard in runMCPServerList and
// TestRunMCPServerList_RefusesARelativeConfigPath FAILS -- measured.

func fakeDaemonEchoingArgv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var p, body string
	if runtime.GOOS == "windows" {
		p = filepath.Join(dir, "mochiii-daemon.exe.cmd")
		body = "@echo off\r\necho ARGV %*\r\n"
	} else {
		p = filepath.Join(dir, "mochiii-daemon")
		body = "#!/bin/sh\necho \"ARGV $@\"\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake daemon: %v", err)
	}
	return p
}

// The exploit: a relative path must never reach the daemon.
func TestRunMCPServerList_RefusesARelativeConfigPath(t *testing.T) {
	t.Setenv(daemonBinEnvVar, fakeDaemonEchoingArgv(t))

	got := runMCPServerList("./models.json")

	if strings.Contains(got, "./models.json") && strings.Contains(got, "ARGV") {
		t.Fatalf("a relative --config reached the daemon. `mcp list` STARTS the servers a "+
			"config names, so a repository shipping models.json would run its own command. got: %s", got)
	}
	if !strings.Contains(got, "refusing a relative") {
		t.Fatalf("expected an explicit refusal, got: %s", got)
	}
}

// Empty is what /mcp-server now passes: let the daemon resolve its own config
// from beside its own binary.
func TestRunMCPServerList_PassesNoConfigByDefault(t *testing.T) {
	t.Setenv(daemonBinEnvVar, fakeDaemonEchoingArgv(t))

	got := runMCPServerList("")

	if strings.Contains(got, "--config") {
		t.Fatalf("--config was passed when none was asked for: %s", got)
	}
	if !strings.Contains(got, "mcp list") {
		t.Fatalf("expected the daemon to be invoked with `mcp list`, got: %s", got)
	}
}

// The capability survives: an ABSOLUTE path is a developer naming a config they
// chose, which is a trusted input in the same way MOCHIII_DAEMON_BIN is.
// Refusing it too would break a real workflow to fix a different problem.
func TestRunMCPServerList_StillHonoursAnAbsoluteConfigPath(t *testing.T) {
	t.Setenv(daemonBinEnvVar, fakeDaemonEchoingArgv(t))
	abs := filepath.Join(t.TempDir(), "models.json")

	got := runMCPServerList(abs)

	if !strings.Contains(got, "--config") || !strings.Contains(got, abs) {
		t.Fatalf("an absolute --config was not honoured: %s", got)
	}
}
