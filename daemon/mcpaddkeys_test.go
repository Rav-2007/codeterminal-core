package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// A REQUIRED KEY IS ASKED FOR WHEN THE SERVER IS ADDED (askMissingKeys).
// FOUND 2026-10-30: `mcp add github --env GITHUB_PERSONAL_ACCESS_TOKEN ...`
// added the server without its token and said so only in a note.

const fakeAddKey = "fake-add-key-5b1e"

// addCommandHome is a home of its own for `mcp add`: where the added servers
// and their keys are kept, and a config with agent mode on.
func addCommandHome(t *testing.T) (cfgPath, userPath, keyPath string) {
	t.Helper()
	home := t.TempDir()
	origCred := credentialsPath
	t.Cleanup(func() { credentialsPath = origCred })
	credentialsPath = func() (string, error) { return filepath.Join(home, ".mochiii", "credentials.json"), nil }
	cfgPath = filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(cfgPath, []byte(`{"config_version":1,"default_tier":"p","tiers":{"p":{"slug":"a/b","active":true}},
		"mcp":{"enabled":true,"builtin":{"disabled":true}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	origIn, origOut := mcpIn, mcpOut
	t.Cleanup(func() { mcpIn, mcpOut = origIn, origOut })
	t.Setenv(echoKeyName, "")
	return cfgPath, filepath.Join(home, ".mochiii", userMCPFileName), filepath.Join(home, ".mochiii", mcpSecretsFileName)
}

// atTerminal makes `mcp add` treat its input as a terminal, as a person typing.
func atTerminal(t *testing.T) {
	t.Helper()
	orig := mcpKeysAskable
	t.Cleanup(func() { mcpKeysAskable = orig })
	mcpKeysAskable = func(io.Reader) bool { return true }
}

// addKeyedEchoCommand runs `mcp add echo --env KEY -- echoserver --require-env KEY`
// with in as what the user types, and returns what it printed and logged.
func addKeyedEchoCommand(t *testing.T, cfgPath string, in io.Reader) (out, logged string) {
	t.Helper()
	out, logged, err := runAddKeyedEcho(cfgPath, buildEchoServer(t), in)
	if err != nil {
		t.Fatalf("mcp add: %v\n%s", err, out)
	}
	return out, logged
}

func runAddKeyedEcho(cfgPath, echo string, in io.Reader) (out, logged string, err error) {
	var o, l bytes.Buffer
	mcpIn, mcpOut = in, &o
	args := []string{"add", "echo", "--env", echoKeyName, "--config", cfgPath, "--", echo, "--require-env", echoKeyName}
	err = runMCPCommand(args, log.New(&l, "", 0))
	return o.String(), l.String(), err
}

// The key is asked for once, kept in mcp-secrets.json for this server, and the
// add's own first start has it: the echo server exits without it.
//
// Neuter check: make askMissingKeys return at once -- nothing is asked, the
// first start fails, and nothing is stored.
func TestAnAddAsksForAMissingKeyAndTheServerStartsWithIt(t *testing.T) {
	cfgPath, _, keyPath := addCommandHome(t)
	atTerminal(t)

	out, logged := addKeyedEchoCommand(t, cfgPath, strings.NewReader("echo\n"+fakeAddKey+"\n"))

	ask := "echo reads " + echoKeyName + ", which Mochiii has no value for"
	if n := strings.Count(out, ask); n != 1 {
		t.Errorf("asked %d time(s), want once:\n%s", n, out)
	}
	if !strings.Contains(out, "Added echo. It started and offers") {
		t.Errorf("the server did not start with the key typed:\n%s", out)
	}
	if strings.Contains(out, "Mochiii has no value for "+echoKeyName) {
		t.Errorf("the add still says the key is missing:\n%s", out)
	}
	file, _, err := readMCPSecrets(keyPath)
	if err != nil || file.Servers["echo"].Env[echoKeyName] != fakeAddKey {
		t.Fatalf("the key is not stored for echo: %+v, %v", file.Servers, err)
	}
	if strings.Contains(out, fakeAddKey) || strings.Contains(logged, fakeAddKey) {
		t.Errorf("the value was printed or logged:\nout:\n%s\nlog:\n%s", out, logged)
	}
}

// Enter on its own skips the key: the server is added anyway, its answer keeps
// the note and the set-key hint, and its status line names the key it needs.
func TestEnterSkipsTheKeyAndTheServerIsMarkedAsNeedingIt(t *testing.T) {
	cfgPath, userPath, keyPath := addCommandHome(t)
	atTerminal(t)

	out, _ := addKeyedEchoCommand(t, cfgPath, strings.NewReader("echo\n\n"))

	for _, want := range []string{
		"echo reads " + echoKeyName + ", which Mochiii has no value for",
		"Skipped: echo will be added without " + echoKeyName,
		"NOTE: Mochiii has no value for " + echoKeyName + ", so the server starts without it. Set it with `mcp set-key echo " + echoKeyName + "`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the add does not say %q:\n%s", want, out)
		}
	}
	if got, _, _ := loadUserMCPServers(userPath); got["echo"].Command == "" {
		t.Fatal("skipping the key did not add the server")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("a key file was written though the key was skipped")
	}
	srv, err := mcpAdminServer(cfgPath, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	listed := srv.mcpResult(context.Background(), manageRequest(protocol.MCPActionServers, ""))
	i := slices.IndexFunc(listed.Servers, func(s protocol.MCPServerInfo) bool { return s.Name == "echo" })
	if i < 0 {
		t.Fatalf("echo is not listed: %+v", listed.Servers)
	}
	if info := listed.Servers[i]; !slices.Equal(info.EnvMissing, []string{echoKeyName}) ||
		!strings.Contains(info.StatusLine, "Needs a key: "+echoKeyName) {
		t.Errorf("listed as missing %v, status %q", info.EnvMissing, info.StatusLine)
	}
}

// With no terminal -- a script, input piped and never closed -- nothing is
// asked and nothing waits: the add finishes with today's note.
func TestANonInteractiveAddAsksNothingAndDoesNotWait(t *testing.T) {
	cfgPath, _, _ := addCommandHome(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if _, err := w.WriteString("echo\n"); err != nil { // the writer stays open: a read for a key would block
		t.Fatal(err)
	}

	echo := buildEchoServer(t)
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, _, err := runAddKeyedEcho(cfgPath, echo, r)
		done <- result{out, err}
	}()
	var out string
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("mcp add: %v\n%s", got.err, got.out)
		}
		out = got.out
	case <-time.After(90 * time.Second):
		t.Fatal("the add is still waiting for input with no terminal to give it")
	}
	if strings.Contains(out, "which Mochiii has no value for") || strings.Contains(out, "Value for ") {
		t.Errorf("a key was asked for with no terminal:\n%s", out)
	}
	if !strings.Contains(out, "Added echo") ||
		!strings.Contains(out, "NOTE: Mochiii has no value for "+echoKeyName+", so the server starts without it") {
		t.Errorf("the add did not finish with today's note:\n%s", out)
	}
}
