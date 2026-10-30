package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"mochiii/protocol"
)

// KEYS AT REST (mcpsecrets.go): the values a user gives for their servers'
// variables are kept in ~/.mochiii/mcp-secrets.json, owner-only, so they
// survive a restart and every client sees the same ones.

const secretSentinel = "sk-sentinel-4f1c9e2b7a"

// withKeyFile gives s a key file of its own and returns its path.
func withKeyFile(t *testing.T, s *Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), mcpSecretsFileName)
	s.mcpSecrets = newMCPSecretStore(path)
	return path
}

// keyedEchoWithFile is keyedEcho, reading echoKeyName, with a key file.
func keyedEchoWithFile(t *testing.T) (*Server, string) {
	t.Helper()
	s := keyedEcho(t)
	path := withKeyFile(t, s)
	update := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	update.Action = protocol.MCPActionUpdate
	update.Env = []string{echoKeyName}
	update.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), update); !resp.Ok {
		t.Fatalf("update: %s", resp.Error)
	}
	return s, path
}

// A key set once is in force for a daemon started later: the file, not the
// memory of the daemon that was told, is what holds it.
//
// Neuter check: make setMCPEnv skip s.mcpSecrets.setVar -- the fresh daemon's
// server exits for want of its key.
func TestAStoredKeySurvivesARestart(t *testing.T) {
	s, path := keyedEchoWithFile(t)
	if resp := s.mcpResult(context.Background(), setEnvRequest(echoKeyName, secretSentinel)); !resp.Ok {
		t.Fatalf("set_env: %s", resp.Error)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no key file was written: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the key file is mode %#o, want 0600", info.Mode().Perm())
	}

	// A new daemon: same user file, same key file, nothing in memory.
	fresh := &Server{
		cfg: s.cfg, workspace: s.workspace, logger: log.New(&bytes.Buffer{}, "", 0),
		serverExtras: serverExtras{
			configPath: s.configPath, userServersPath: s.userServersPath, mcpRunDir: s.mcpRunDir,
			mcpSecrets: newMCPSecretStore(path),
		},
	}
	if !registryOffers(t, fresh, "echo__secret") {
		t.Fatal("a daemon started after the key was stored did not give it to the server")
	}
	if info := serverInfo(t, fresh, "echo"); !slices.Equal(info.EnvStored, []string{echoKeyName}) || len(info.EnvMissing) != 0 {
		t.Errorf("env_stored %v, env_missing %v", info.EnvStored, info.EnvMissing)
	}
	if got, n := fresh.credRedact("key " + secretSentinel); n == 0 || strings.Contains(got, secretSentinel) {
		t.Errorf("a key read from the file is not scrubbed: %q", got)
	}
}

// A key file others can write is not used: a value somebody else chose is one
// an agreed-to server would trust.
func TestAKeyFileOthersCanWriteIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	s, path := keyedEchoWithFile(t)
	s.mcpResult(context.Background(), setEnvRequest(echoKeyName, secretSentinel))
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	cfg, problems := s.agentConfig()
	_ = cfg
	if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "writable by more than its owner") }) {
		t.Errorf("no problem reported for a world-writable key file: %v", problems)
	}
	if info := serverInfo(t, s, "echo"); !slices.Equal(info.EnvMissing, []string{echoKeyName}) {
		t.Errorf("a key from a refused file is still counted: env_missing %v", info.EnvMissing)
	}
}

// Another process (the CLI, another window's daemon) writing the file is seen
// at the next request, and its value is scrubbed from then on.
func TestAKeyStoredElsewhereIsPickedUpAndScrubbed(t *testing.T) {
	s, path := keyedEchoWithFile(t)
	other := newMCPSecretStore(path)
	if err := other.setVar("", echoKeyName, secretSentinel); err != nil {
		t.Fatal(err)
	}
	if info := serverInfo(t, s, "echo"); len(info.EnvMissing) != 0 {
		t.Errorf("a key stored by another process is not seen: env_missing %v", info.EnvMissing)
	}
	if got, _ := s.credRedact(secretSentinel); strings.Contains(got, secretSentinel) {
		t.Error("a key stored by another process is not scrubbed")
	}
}

// A server's own value wins over the one every server reads, and is given to
// that server only.
func TestAServersOwnKeyWinsAndStaysItsOwn(t *testing.T) {
	s, path := keyedEchoWithFile(t)
	st := newMCPSecretStore(path)
	if err := st.setVar("", echoKeyName, "global-value"); err != nil {
		t.Fatal(err)
	}
	resp := s.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionSetEnv, Name: "echo", EnvName: echoKeyName, EnvValue: "own-value-123",
	})
	if !resp.Ok {
		t.Fatalf("set_env for one server: %s", resp.Error)
	}
	if v, _ := s.mcpEnvValueFor("echo")(echoKeyName); v != "own-value-123" {
		t.Errorf("echo is given %q, want its own value", v)
	}
	if v, _ := s.mcpEnvValueFor("another")(echoKeyName); v != "global-value" {
		t.Errorf("another server is given %q, want the shared value", v)
	}

	// A server-specific value for a variable the server does not read is refused.
	resp = s.mcpResult(context.Background(), protocol.MCPRequest{
		MCP: true, Action: protocol.MCPActionSetEnv, Name: "echo", EnvName: "NOT_READ", EnvValue: "x",
	})
	if resp.Ok || !strings.Contains(resp.Error, "does not read NOT_READ") {
		t.Errorf("ok=%v err=%q", resp.Ok, resp.Error)
	}

	// Removing the server takes its own keys and leaves the shared one.
	s.mcpResult(context.Background(), manageRequest(protocol.MCPActionRemove, "echo"))
	file, _, err := readMCPSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := file.Servers["echo"]; kept {
		t.Error("a removed server's own keys were kept")
	}
	if file.Vars[echoKeyName] != "global-value" {
		t.Error("removing a server dropped a value every server reads")
	}
}

// add --env KEY=value: the value is stored for that server, and only once the
// add is agreed to.
//
// Neuter check: move storeServerEnv above the acknowledgement return -- the
// first, unagreed step stores the key.
func TestAnAddsValuesAreStoredOnlyWhenAgreedTo(t *testing.T) {
	s := mcpAdminTestServer(t)
	path := withKeyFile(t, s)
	req := addRequest("echo", buildEchoServer(t), "--require-env", echoKeyName)
	req.Env = []string{echoKeyName}
	req.EnvValues = map[string]string{echoKeyName: secretSentinel}
	t.Setenv(echoKeyName, "")

	first := s.mcpResult(context.Background(), req)
	if !first.NeedsAcknowledgement || !strings.Contains(first.Detail, "kept for this server only") {
		t.Fatalf("first step: needs_ack=%v detail=%q", first.NeedsAcknowledgement, first.Detail)
	}
	if strings.Contains(first.Detail, secretSentinel) {
		t.Fatal("the warning repeats the value")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a value was stored before the add was agreed to")
	}

	req.AcknowledgedUnconfined = true
	done := s.mcpResult(context.Background(), req)
	if !done.Ok || !strings.Contains(done.Detail, "It started and offers") {
		t.Fatalf("acknowledged add: ok=%v err=%q detail=%q", done.Ok, done.Error, done.Detail)
	}
	data, _ := os.ReadFile(path)
	var file mcpSecretsFile
	if err := json.Unmarshal(data, &file); err != nil || file.Servers["echo"].Env[echoKeyName] != secretSentinel {
		t.Fatalf("the value is not stored for echo: %v", err)
	}
	if strings.Contains(done.Detail, secretSentinel) || slices.ContainsFunc(done.Notes, func(n string) bool { return strings.Contains(n, secretSentinel) }) {
		t.Error("the answer repeats the value")
	}
}

func TestAValueForAVariableTheServerDoesNotReadIsRefused(t *testing.T) {
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	req := addRequest("echo", buildEchoServer(t))
	req.EnvValues = map[string]string{"STRAY": secretSentinel}
	resp := s.mcpResult(context.Background(), req)
	if resp.Ok || !strings.Contains(resp.Error, "STRAY, which the server does not read") || strings.Contains(resp.Error, secretSentinel) {
		t.Errorf("ok=%v err=%q", resp.Ok, resp.Error)
	}
}

func TestAnInvisibleCharacterInAKeyIsNamedNotRepeated(t *testing.T) {
	err := validMCPEnvValue("sk-abc\u200bdef")
	if err == nil || !strings.Contains(err.Error(), "U+200B") || !strings.Contains(err.Error(), "position 7") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "sk-abc") {
		t.Errorf("the error repeats the value: %v", err)
	}
}

// A field this daemon does not know (a newer one's sign-in) survives a write,
// and its strings are still scrubbed.
func TestAKeyFileKeepsFieldsItDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), mcpSecretsFileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":{"x":{"oauth":{"access_token":"`+secretSentinel+`"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newMCPSecretStore(path)
	if err := st.setVar("x", "A_KEY", "v1"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), secretSentinel) {
		t.Fatalf("a field this daemon does not know was dropped:\n%s", data)
	}
	if !slices.Contains(st.values(), secretSentinel) {
		t.Error("a string in an unknown field is not among the values the scrubber is built from")
	}
}

func TestSplitEnvFlags(t *testing.T) {
	names, values := splitEnvFlags([]string{"A", "B=1=2", "A=x"})
	if !slices.Equal(names, []string{"A", "B"}) || values["B"] != "1=2" || values["A"] != "x" {
		t.Errorf("names %v values %v", names, values)
	}
}

// `mcp set-key` reads the value from stdin, never from its arguments.
func TestSetKeyReadsTheValueFromStdin(t *testing.T) {
	var out bytes.Buffer
	mcpIn, mcpOut = strings.NewReader(secretSentinel+"\n"), &out
	t.Cleanup(func() { mcpIn, mcpOut = os.Stdin, os.Stdout })
	got, err := readSecretLine(mcpIn, mcpOut, "prompt: ")
	if err != nil || got != secretSentinel {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Contains(out.String(), secretSentinel) {
		t.Error("the value was written back out")
	}
	if err := runMCPSetKey([]string{"echo", "X_KEY", secretSentinel}, log.New(&bytes.Buffer{}, "", 0)); err == nil ||
		!strings.Contains(err.Error(), "read from stdin") {
		t.Errorf("a value given as an argument was not refused: %v", err)
	}
}

// REMOVING ONE SERVER KEEPS EVERY OTHER SERVER'S KEYS. forgetServer drops the
// removed server's own entry only, values every server reads stay, and the
// file goes away only once nothing is left in it.

// addKeyedEcho adds the echo server under name, needing variable, with value
// stored for it as `mcp add --env NAME=value` stores one.
func addKeyedEcho(t *testing.T, s *Server, name, variable, value string) {
	t.Helper()
	t.Setenv(variable, "")
	req := addRequest(name, buildEchoServer(t), "--require-env", variable)
	req.Env = []string{variable}
	req.EnvValues = map[string]string{variable: value}
	req.AcknowledgedUnconfined = true
	if resp := s.mcpResult(context.Background(), req); !resp.Ok {
		t.Fatalf("adding %s: %s %s", name, resp.Error, resp.Detail)
	}
}

// Neuter check: make forgetServer empty the whole file (`*f = mcpSecretsFile{}`)
// -- b's key and the shared value go with a's, and the file with them.
func TestRemovingOneServerKeepsTheOthersKeys(t *testing.T) {
	const keyA, keyB, shared = "MOCHIII_TEST_KEY_A", "MOCHIII_TEST_KEY_B", "MOCHIII_TEST_SHARED"
	s := mcpAdminTestServer(t)
	path := withKeyFile(t, s)
	addKeyedEcho(t, s, "a", keyA, "fake-key-a")
	addKeyedEcho(t, s, "b", keyB, "fake-key-b")
	t.Setenv(shared, "")
	if resp := s.mcpResult(context.Background(), setEnvRequest(shared, "fake-shared")); !resp.Ok {
		t.Fatalf("set_env %s: %s", shared, resp.Error)
	}

	if resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionRemove, "a")); !resp.Ok {
		t.Fatalf("removing a: %s", resp.Error)
	}

	file, info, err := readMCPSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Error("the key file is gone after removing one of two servers")
	}
	if _, kept := file.Servers["a"]; kept {
		t.Error("the removed server's key was kept")
	}
	if got := file.Servers["b"].Env[keyB]; got != "fake-key-b" {
		t.Errorf("b's stored key is %q after removing a, want fake-key-b", got)
	}
	if got := file.Vars[shared]; got != "fake-shared" {
		t.Errorf("the shared value is %q after removing a, want fake-shared", got)
	}

	if v, ok := s.mcpEnvValueFor("a")(keyA); ok {
		t.Errorf("a's key is still looked up: %q", v)
	}
	if v, _ := s.mcpEnvValueFor("b")(keyB); v != "fake-key-b" {
		t.Errorf("b is given %q, want fake-key-b", v)
	}
	if v, _ := s.mcpEnvValueFor("b")(shared); v != "fake-shared" {
		t.Errorf("b is given %q for the shared variable, want fake-shared", v)
	}
	// b's process starts only with its key (--require-env), so this is the key
	// reaching the server, not just the file holding it.
	if !registryOffers(t, s, "b__secret") {
		t.Error("b no longer starts with its key after a was removed")
	}

	added := storedServers(t, s)
	if _, ok := added["a"]; ok {
		t.Error("a is still in the added servers")
	}
	if _, ok := added["b"]; !ok {
		t.Error("b is no longer in the added servers")
	}
}

// Removing the last server with a key deletes the key file rather than leaving
// an empty one. The lock file beside it stays: it is what update() locks.
func TestRemovingTheLastKeyedServerDeletesTheKeyFile(t *testing.T) {
	const key = "MOCHIII_TEST_KEY_ONLY"
	s := mcpAdminTestServer(t)
	path := withKeyFile(t, s)
	addKeyedEcho(t, s, "only", key, "fake-key-only")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the add stored no key file: %v", err)
	}

	if resp := s.mcpResult(context.Background(), manageRequest(protocol.MCPActionRemove, "only")); !resp.Ok {
		t.Fatalf("removing only: %s", resp.Error)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		data, _ := os.ReadFile(path)
		t.Fatalf("the key file was left behind (%d bytes): %v", len(data), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != mcpSecretsFileName+".lock" {
			t.Errorf("left behind in the key file's folder: %s", e.Name())
		}
	}
	if v, ok := s.mcpEnvValueFor("only")(key); ok {
		t.Errorf("the removed server's key is still looked up: %q", v)
	}
}
