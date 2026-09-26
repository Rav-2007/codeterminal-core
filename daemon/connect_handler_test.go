package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// connectServer is a Server with a credential already in force, pointed at a temp
// credential file, so a request can be watched to change -- or not change -- what
// the daemon would actually send.
func connectServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	orig := credentialsPath
	t.Cleanup(func() { credentialsPath = orig })
	credentialsPath = func() (string, error) { return path, nil }
	t.Setenv("MOCHIII_API_KEY", "")
	t.Setenv("MOCHIII_API_BASE", "")
	t.Setenv("MOCHIII_USE_PROXY", "")
	return &Server{
		apiKey:  "sk-the-key-it-started-with",
		apiBase: "https://old.example/v1",
		cfg:     &Config{},
		logger:  log.New(os.Stderr, "test: ", 0),
	}, path
}

// THE WHOLE POINT OF /connect OVER THE CLI: the key is in force for the very next
// turn, with no restart. If this passes while the daemon keeps sending the old
// key, the feature is a slower way to edit a file.
func TestConnectAdoptsAVerifiedKeyWithoutARestart(t *testing.T) {
	s, path := connectServer(t)
	srv := keyServer(t, http.StatusOK, http.StatusOK, `{"data":{"label":"laptop"}}`)

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{
		Connect: true, APIKey: testProviderKey, APIBase: srv.URL,
	})

	if !resp.Ok || resp.Outcome != protocol.ConnectAccepted {
		t.Fatalf("outcome = %q ok=%v, want accepted (%s)", resp.Outcome, resp.Ok, resp.Detail)
	}
	if !resp.InUse {
		t.Error("the response says the key is not in use, but nothing overrode it")
	}
	gotKey, gotBase := s.credentials()
	if gotKey != testProviderKey {
		t.Errorf("the daemon is still sending %s: the key was stored but never adopted", maskKey(gotKey))
	}
	if gotBase != srv.URL {
		t.Errorf("api base = %q, want %q", gotBase, srv.URL)
	}
	stored, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != testProviderKey || !stored.Verified {
		t.Errorf("the credential was not persisted as verified: %+v", stored.Verified)
	}
}

// A REFUSED KEY CHANGES NOTHING. The daemon is serving turns; swapping in a key
// the provider has just rejected would break the next one for the sake of an
// instruction already known to be wrong.
func TestConnectDoesNotAdoptOrStoreARefusedKey(t *testing.T) {
	s, path := connectServer(t)
	srv := keyServer(t, http.StatusUnauthorized, http.StatusOK, "")

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{
		Connect: true, APIKey: "sk-a-key-the-provider-hates", APIBase: srv.URL,
	})

	if resp.Ok || resp.Outcome != protocol.ConnectRejected {
		t.Fatalf("outcome = %q ok=%v, want rejected", resp.Outcome, resp.Ok)
	}
	if key, _ := s.credentials(); key != "sk-the-key-it-started-with" {
		t.Errorf("a refused key replaced the one in use: now %s", maskKey(key))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused key was written to disk")
	}
	// The response is shown to a user; it must not hand the key back.
	blob, _ := json.Marshal(resp)
	if strings.Contains(string(blob), "sk-a-key-the-provider-hates") {
		t.Errorf("the response carries the key back in full: %s", blob)
	}
}

// An unprovable key IS adopted -- the user asked for it and it may be fine -- but
// the outcome must stay distinguishable from a proven one, all the way out.
func TestConnectAdoptsAnUnprovableKeyButSaysItIsUnproven(t *testing.T) {
	s, _ := connectServer(t)
	srv := keyServer(t, http.StatusNotFound, http.StatusOK, "") // public /models proves nothing

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{
		Connect: true, APIKey: testProviderKey, APIBase: srv.URL,
	})

	if resp.Outcome != protocol.ConnectUnverified {
		t.Fatalf("outcome = %q, want unverified (%s)", resp.Outcome, resp.Detail)
	}
	if !resp.Ok || !resp.InUse {
		t.Error("an unprovable key was not adopted, but the user asked for it")
	}
	if key, _ := s.credentials(); key != testProviderKey {
		t.Errorf("the key was not adopted: %s", maskKey(key))
	}
}

// A KEY IN THE DAEMON'S ENVIRONMENT SILENTLY WINS, and that is invisible from a
// client. If /connect reported success and adopted the key anyway, the daemon
// would then disagree with what a restart would do -- a credential that depends
// on whether you restarted is worse than one that is merely inconvenient.
func TestConnectRefusesToAdoptWhenTheEnvironmentOverrides(t *testing.T) {
	s, _ := connectServer(t)
	t.Setenv("MOCHIII_API_KEY", "sk-from-the-environment")
	srv := keyServer(t, http.StatusOK, http.StatusOK, "")

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{
		Connect: true, APIKey: testProviderKey, APIBase: srv.URL,
	})

	if !resp.EnvOverride {
		t.Error("the response does not say the environment takes precedence")
	}
	if resp.InUse {
		t.Error("the response claims the key is in use, but the environment overrides it")
	}
	if key, _ := s.credentials(); key != "sk-the-key-it-started-with" {
		t.Errorf("the key was adopted despite the environment override: %s", maskKey(key))
	}
}

// --show changes nothing and reports honestly; --forget removes the file but
// deliberately does NOT cut the credential out from under a running daemon.
func TestConnectShowAndForget(t *testing.T) {
	s, path := connectServer(t)
	if err := saveCredential(path, storedCredential{
		APIBase: "https://p.example/v1", APIKey: testProviderKey, Verified: true}); err != nil {
		t.Fatal(err)
	}

	shown := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, Show: true})
	if shown.Outcome != protocol.ConnectShown || !shown.Ok {
		t.Fatalf("show outcome = %q", shown.Outcome)
	}
	if shown.MaskedKey == testProviderKey {
		t.Error("show returned the key in full")
	}
	if !strings.Contains(shown.Detail, "accepted") {
		t.Errorf("show does not report that it was verified: %q", shown.Detail)
	}
	if key, _ := s.credentials(); key != "sk-the-key-it-started-with" {
		t.Error("show changed the key in use")
	}

	forgotten := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, Forget: true})
	if !forgotten.Ok || forgotten.Outcome != protocol.ConnectRemoved {
		t.Fatalf("forget outcome = %q", forgotten.Outcome)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("forget left the credential file behind")
	}
	// Still serving with what it had: a tidy-up must not become an outage.
	if key, _ := s.credentials(); key != "sk-the-key-it-started-with" {
		t.Errorf("forget cut the credential out from under a running daemon: %s", maskKey(key))
	}
}

// A malformed key is refused before anything is stored or sent.
func TestConnectRejectsAMalformedKey(t *testing.T) {
	s, path := connectServer(t)
	resp := s.connectResult(context.Background(), protocol.ConnectRequest{
		Connect: true, APIKey: "sk-first\nsk-second", NoVerify: true,
	})
	if resp.Error == "" {
		t.Fatal("a key with a line break in it was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a malformed key was written to disk")
	}
}

// The request has to be told apart from a prompt by its discriminator, exactly
// like every other typed request on this socket. Without this, {"connect":true}
// decodes as a PromptRequest with no prompt and comes back "prompt is empty".
func TestIsConnectRequestRecognisesOnlyItsOwnShape(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"connect":true,"api_key":"x"}`, true},
		{`{"connect":false}`, true},
		{`{"status":true}`, false},
		{`{"prompt":"hello"}`, false},
		{`{"connect":"yes"}`, false}, // a string is not the bool discriminator
		{`not json`, false},
	} {
		if got := isConnectRequest(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("isConnectRequest(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// FORGET MUST NOT REPORT A REMOVAL THAT DID NOT HAPPEN.
//
// forgetCredential succeeds when there was no file, because the command means
// "there must be no stored key afterwards" and that is already true. But the
// REPORT is a different question from the outcome: telling someone their stored
// key was removed when they never had one teaches them the command did something
// it did not, and it hides the real answer in the case they actually care about
// -- they ran `forget` because a key they believed was stored was not being used.
//
// The VS Code side already got this right ("No key was stored, so there was
// nothing to remove"); this is the wire and the CLI catching up.
func TestForgetDistinguishesARemovalFromThereBeingNothingToRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")

	removed, err := forgetCredential(path)
	if err != nil {
		t.Fatalf("forgetting a credential that is not there failed: %v", err)
	}
	if removed {
		t.Error("claimed to have removed a credential file that never existed")
	}

	if err := saveCredential(path, storedCredential{APIBase: "https://example.invalid", APIKey: "sk-test-key-abcdefghijkl"}); err != nil {
		t.Fatalf("saving: %v", err)
	}
	removed, err = forgetCredential(path)
	if err != nil {
		t.Fatalf("forgetting a real credential failed: %v", err)
	}
	if !removed {
		t.Error("removed a real credential file but reported that there was nothing there")
	}
}

// THE DAEMON DECIDES WHETHER A KEY IS NEEDED, because only it knows all three
// inputs. Getting this wrong is user-visible in both directions: a false positive
// asks a local-server user for a credential they deliberately do not have, and a
// false negative sends a question out to earn a provider's 401.
func TestNeedsAPIKeyAnswersForEverySetup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      string
		base     string
		useProxy bool
		want     bool
		why      string
	}{
		{
			name: "no key, remote provider", base: "https://openrouter.ai/api/v1",
			want: true, why: "a prompt would go out with no Authorization header and be refused",
		},
		{
			name: "key present", key: "sk-something", base: "https://openrouter.ai/api/v1",
			want: false, why: "there is a credential to send",
		},
		{
			name: "no key, loopback base", base: "http://127.0.0.1:8080/v1",
			want: false, why: "a local OpenAI-compatible server wanting no key is a supported setup",
		},
		{
			name: "no key, localhost by name", base: "http://localhost:1234/v1",
			want: false, why: "localhost is loopback however it is spelled",
		},
		{
			name: "no key, IPv6 loopback", base: "http://[::1]:8080/v1",
			want: false, why: "::1 is loopback too",
		},
		{
			name: "proxy mode", base: "https://proxy.example/v1", useProxy: true,
			want: false, why: "proxy mode authenticates with a proxy key, not a provider key",
		},
		{
			name: "no key, empty base",
			want: true, why: "an unknown base must be assumed to want a credential, not assumed local",
		},
		{
			name: "no key, base with no scheme", base: "openrouter.ai/api/v1",
			want: true, why: "an unparseable host must not be mistaken for loopback",
		},
		{
			name: "whitespace-only key", key: "   ", base: "https://openrouter.ai/api/v1",
			want: true, why: "spaces are not a credential",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := connectServer(t)
			s.apiKey, s.apiBase = tc.key, tc.base
			if tc.useProxy {
				t.Setenv("MOCHIII_USE_PROXY", "true")
			}
			if got := s.needsAPIKey(); got != tc.want {
				t.Errorf("needsAPIKey() = %v, want %v -- %s", got, tc.want, tc.why)
			}
		})
	}
}

// A key accepted over the socket must make the answer flip without a restart,
// because that is what lets a held question be released to the daemon that just
// took the key.
func TestNeedsAPIKeyFlipsWhenAKeyIsAccepted(t *testing.T) {
	s, _ := connectServer(t)
	s.apiKey, s.apiBase = "", "https://openrouter.ai/api/v1"

	if !s.needsAPIKey() {
		t.Fatal("a daemon with no key does not report needing one")
	}
	s.setAPIKey("sk-accepted-over-the-socket", "https://openrouter.ai/api/v1")
	if s.needsAPIKey() {
		t.Error("the daemon took a key but still reports needing one; a held question would never be released")
	}
}
