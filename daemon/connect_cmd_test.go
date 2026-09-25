package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A key long enough to be masked rather than length-only, and recognisable in
// output so a test can assert it is NOT there.
const testProviderKey = "sk-test-0123456789abcdefSECRET"

// connectHarness points the command at a temp credential file and captures what
// the user would have seen. Nothing here may touch the real ~/.mochiii.
func connectHarness(t *testing.T) (path string, out *bytes.Buffer, logger *log.Logger) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "credentials.json")
	origPath, origOut := credentialsPath, connectOut
	t.Cleanup(func() { credentialsPath, connectOut = origPath, origOut })
	credentialsPath = func() (string, error) { return path, nil }
	out = &bytes.Buffer{}
	connectOut = out
	// The environment decides precedence, so a stray key in the developer's own
	// shell would otherwise change what these tests observe.
	t.Setenv("MOCHIII_API_KEY", "")
	t.Setenv("MOCHIII_API_BASE", "")
	return path, out, log.New(io.Discard, "", 0)
}

// feedStdin replaces stdin with a file holding s, so readKey's non-terminal path
// reads it. A file (not a pipe) keeps the read from blocking on EOF.
func feedStdin(t *testing.T, s string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	t.Cleanup(func() { os.Stdin = orig; _ = f.Close() })
	os.Stdin = f
}

// keyServer is a provider stand-in: it answers /key and /models with the codes
// given, so every verification outcome can be produced deliberately.
func keyServer(t *testing.T, keyStatus, modelsStatus int, keyBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/key", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			// A verification that forgot the header would "pass" against a server
			// that ignores it, so the stand-in refuses an unauthenticated call.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(keyStatus)
		if keyBody != "" {
			_, _ = w.Write([]byte(keyBody))
		}
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(modelsStatus)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// THE HONESTY OF THE CHECK. Each outcome has to come from what the provider
// actually said, and the one that matters most is verifyInconclusive: an
// OpenAI-compatible base need not authenticate GET /models, and plenty serve it
// wide open. Reading a 200 from it as proof would make this a check that passes
// for any string at all, which is worse than no check because it makes a claim.
func TestVerifyKeyReportsWhatTheProviderActuallySaid(t *testing.T) {
	for _, tc := range []struct {
		name         string
		keyStatus    int
		modelsStatus int
		keyBody      string
		want         verifyOutcome
	}{
		{"a key endpoint that accepts", http.StatusOK, http.StatusOK,
			`{"data":{"label":"my-key","limit":5.0,"usage":1.25}}`, verifyAccepted},
		{"a key endpoint that refuses", http.StatusUnauthorized, http.StatusOK, "", verifyRejected},
		{"no key endpoint, models refuses", http.StatusNotFound, http.StatusUnauthorized, "", verifyRejected},
		{"no key endpoint, models is public", http.StatusNotFound, http.StatusOK, "", verifyInconclusive},
		{"no key endpoint, models is odd", http.StatusNotFound, http.StatusTeapot, "", verifyInconclusive},
		{"a key endpoint with an unparseable body still counts", http.StatusOK, http.StatusOK,
			"not json at all", verifyAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := keyServer(t, tc.keyStatus, tc.modelsStatus, tc.keyBody)
			got := verifyKey(context.Background(), srv.Client(), srv.URL, testProviderKey)
			if got.Outcome != tc.want {
				t.Errorf("outcome = %v, want %v (detail: %s)", got.Outcome, tc.want, got.Detail)
			}
			if got.Detail == "" {
				t.Error("the outcome carries no explanation for the user")
			}
		})
	}

	t.Run("a base that does not answer", func(t *testing.T) {
		srv := keyServer(t, http.StatusOK, http.StatusOK, "")
		url := srv.URL
		srv.Close() // nothing is listening now
		got := verifyKey(context.Background(), http.DefaultClient, url, testProviderKey)
		if got.Outcome != verifyUnreachable {
			t.Errorf("outcome = %v, want verifyUnreachable (detail: %s)", got.Outcome, got.Detail)
		}
	})

	t.Run("an accepted key reports what the provider said about it", func(t *testing.T) {
		srv := keyServer(t, http.StatusOK, http.StatusOK, `{"data":{"label":"laptop","limit":10.5,"usage":2}}`)
		got := verifyKey(context.Background(), srv.Client(), srv.URL, testProviderKey)
		for _, want := range []string{"laptop", "10.50"} {
			if !strings.Contains(got.Detail, want) {
				t.Errorf("detail %q does not mention %q", got.Detail, want)
			}
		}
	})
}

// A REFUSED KEY MUST NOT BE STORED. Saving it would replace a key that may be
// working with one the provider has just rejected, and the user would discover it
// at their next prompt, reported as a provider problem.
func TestConnectDoesNotSaveARefusedKey(t *testing.T) {
	path, out, logger := connectHarness(t)

	// A good key is already stored, so this also proves the refusal leaves it alone.
	if err := saveCredential(path, storedCredential{APIBase: "https://old.example/v1",
		APIKey: "sk-previous-key-0000WORKS", Verified: true}); err != nil {
		t.Fatal(err)
	}

	srv := keyServer(t, http.StatusUnauthorized, http.StatusOK, "")
	feedStdin(t, "sk-a-key-the-provider-hates\n")

	err := runConnectCommand([]string{"--api-base", srv.URL, "--stdin"}, logger)
	if err == nil {
		t.Fatal("connect reported success for a key the provider refused")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the error does not say the key was refused: %v", err)
	}

	after, _, loadErr := loadCredential(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if after.APIKey != "sk-previous-key-0000WORKS" {
		t.Errorf("the refusal overwrote the stored key: got %q", maskKey(after.APIKey))
	}
	if strings.Contains(out.String()+err.Error(), "sk-a-key-the-provider-hates") {
		t.Error("the refused key was printed back in full")
	}
}

// An unprovable key is still saved -- the user asked to store it and it may be
// perfectly good -- but the record and the report must both say it was not proven.
// "Saved" and "verified" are different facts and must never share a phrase.
func TestConnectMarksAnUnprovableKeyAsUnverified(t *testing.T) {
	path, out, logger := connectHarness(t)
	srv := keyServer(t, http.StatusNotFound, http.StatusOK, "") // public /models: proves nothing
	feedStdin(t, testProviderKey+"\n")

	if err := runConnectCommand([]string{"--api-base", srv.URL, "--stdin"}, logger); err != nil {
		t.Fatalf("connect failed on an unprovable but plausible key: %v", err)
	}
	cred, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != testProviderKey {
		t.Errorf("the key was not stored: %q", maskKey(cred.APIKey))
	}
	if cred.Verified {
		t.Error("the stored record claims the key was verified, but nothing authenticated it")
	}
	if !strings.Contains(out.String(), "NOT verified") {
		t.Errorf("the report does not tell the user it is unverified:\n%s", out)
	}
}

// A verified key records that it was, so --show cannot later imply a check that
// did not happen -- and the success path must actually work end to end.
func TestConnectStoresAVerifiedKeyAndSaysSo(t *testing.T) {
	path, out, logger := connectHarness(t)
	srv := keyServer(t, http.StatusOK, http.StatusOK, `{"data":{"label":"ci"}}`)
	feedStdin(t, testProviderKey+"\n")

	if err := runConnectCommand([]string{"--api-base", srv.URL, "--stdin"}, logger); err != nil {
		t.Fatalf("connect failed on a key the provider accepted: %v", err)
	}
	cred, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cred.Verified {
		t.Error("an accepted key was not recorded as verified")
	}
	if cred.APIBase != srv.URL {
		t.Errorf("api base = %q, want %q", cred.APIBase, srv.URL)
	}
	if !strings.Contains(out.String(), "Verified:") {
		t.Errorf("the report does not state the verification:\n%s", out)
	}
}

// --no-verify stores without asking, and must NOT claim verification. The flag
// exists for an offline machine; it does not exist to make the output nicer.
func TestConnectNoVerifyStoresWithoutClaimingVerification(t *testing.T) {
	path, out, logger := connectHarness(t)
	feedStdin(t, testProviderKey+"\n")

	if err := runConnectCommand([]string{"--stdin", "--no-verify"}, logger); err != nil {
		t.Fatalf("connect --no-verify failed: %v", err)
	}
	cred, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Verified {
		t.Error("--no-verify recorded the key as verified")
	}
	if !strings.Contains(out.String(), "NOT verified") {
		t.Errorf("--no-verify did not say the key is unverified:\n%s", out)
	}
}

// A KEY MUST NOT BE ACCEPTED AS AN ARGUMENT. In argv it is readable by every
// process on the machine via ps and is written to the shell history, so by the
// time a warning could be read the key has already leaked.
func TestConnectRefusesAKeyGivenAsAnArgument(t *testing.T) {
	_, _, logger := connectHarness(t)
	err := runConnectCommand([]string{testProviderKey}, logger)
	if err == nil {
		t.Fatal("connect accepted a key as a positional argument")
	}
	for _, want := range []string{"ps", "history"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not explain %q: %v", want, err)
		}
	}
}

// Paste accidents, caught before the key is stored or sent. A key with an
// embedded newline is refused by Go at request time, so without this the user
// would watch connect accept a key that then fails with an unrelated-looking
// error every time.
func TestConnectRejectsPasteAccidentsWithoutSavingThem(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"a line break inside the key", "sk-first-half\nsk-second-half-long", "control character"},
		{"surrounding text", "my key is sk-abcdefghijklmnop", "space"},
		{"nothing at all", "   \n", "no key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _, logger := connectHarness(t)
			feedStdin(t, tc.input)
			err := runConnectCommand([]string{"--stdin", "--no-verify"}, logger)
			if err == nil {
				t.Fatalf("connect accepted %q", tc.input)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if _, statErr := os.Stat(path); statErr == nil {
				t.Error("a rejected key was written to disk anyway")
			}
		})
	}
}

// --show is for answering "which key is in force?", so it must print enough to
// recognise the key and never enough to use it.
func TestConnectShowNeverPrintsTheKeyItself(t *testing.T) {
	path, out, logger := connectHarness(t)
	if err := saveCredential(path, storedCredential{APIBase: "https://p.example/v1",
		APIKey: testProviderKey, Verified: true}); err != nil {
		t.Fatal(err)
	}
	if err := runConnectCommand([]string{"--show"}, logger); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if strings.Contains(printed, testProviderKey) {
		t.Errorf("--show printed the key in full:\n%s", printed)
	}
	if !strings.Contains(printed, testProviderKey[len(testProviderKey)-4:]) {
		t.Errorf("--show does not show the last 4 characters, so the key cannot be recognised:\n%s", printed)
	}
	if !strings.Contains(printed, "https://p.example/v1") {
		t.Errorf("--show does not name the api base:\n%s", printed)
	}
}

// A key in the environment silently wins over a stored one. That is invisible
// otherwise, and it is exactly what someone debugging "I connected but it used
// the wrong key" needs told.
func TestConnectShowSaysWhenTheEnvironmentOverridesTheStoredKey(t *testing.T) {
	path, out, logger := connectHarness(t)
	if err := saveCredential(path, storedCredential{APIBase: "https://p.example/v1",
		APIKey: testProviderKey}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCHIII_API_KEY", "sk-environment-key-9999")
	if err := runConnectCommand([]string{"--show"}, logger); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "precedence") {
		t.Errorf("--show does not say the environment wins:\n%s", printed)
	}
	if strings.Contains(printed, "sk-environment-key-9999") {
		t.Errorf("--show printed the environment key in full:\n%s", printed)
	}
}

// --forget must leave no key behind, and must succeed when there was none: the
// flag means "there is no stored key afterwards", which is already true then.
func TestConnectForgetRemovesTheKeyAndIsIdempotent(t *testing.T) {
	path, _, logger := connectHarness(t)
	if err := saveCredential(path, storedCredential{APIKey: testProviderKey}); err != nil {
		t.Fatal(err)
	}
	if err := runConnectCommand([]string{"--forget"}, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the credential still exists after --forget (stat err: %v)", err)
	}
	if err := runConnectCommand([]string{"--forget"}, logger); err != nil {
		t.Errorf("--forget failed when there was nothing to forget: %v", err)
	}
}

// The stored file must be owner-only, and a round trip must preserve everything
// the daemon needs. A key readable by other accounts on the machine is the whole
// reason this is a file of its own rather than a line in models.json.
func TestCredentialIsOwnerOnlyAndSurvivesARoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	want := storedCredential{APIBase: "https://p.example/v1", APIKey: testProviderKey, Verified: true}
	if err := saveCredential(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("credential mode is %#o, want 0600: other accounts on this machine can read the key",
			info.Mode().Perm())
	}
	got, warn, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Errorf("a freshly written credential warned: %s", warn)
	}
	if got.APIKey != want.APIKey || got.APIBase != want.APIBase || !got.Verified {
		t.Errorf("round trip lost something: %+v", got)
	}
	if got.SavedAt == "" {
		t.Error("nothing recorded when the credential was saved")
	}
	// No temp file left behind, and a second save replaces rather than appends.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a temp file survived the save (stat err: %v)", err)
	}
	want.APIKey = "sk-replacement-key-1234"
	if err := saveCredential(path, want); err != nil {
		t.Fatal(err)
	}
	got, _, err = loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "sk-replacement-key-1234" {
		t.Errorf("the second save did not replace the key: %q", maskKey(got.APIKey))
	}
	var onDisk map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("the file is not single valid JSON after two saves: %v", err)
	}
}

// "Nobody has run connect" is an ordinary state, not an error. Returning one
// would make every key-in-the-environment deployment log a problem it lacks.
func TestLoadCredentialTreatsAMissingFileAsUnconfigured(t *testing.T) {
	cred, warn, err := loadCredential(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Errorf("a missing credential was an error: %v", err)
	}
	if warn != "" {
		t.Errorf("a missing credential warned: %s", warn)
	}
	if cred.configured() {
		t.Error("a missing credential reported itself as configured")
	}
}

// A too-open credential is REPORTED, not silently repaired: a mode that wide
// means the key may already have been read, and quietly chmod-ing it back would
// hide exactly the fact the user needs in order to rotate it.
func TestLoadCredentialWarnsWhenOthersCanReadTheKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NOT RUN: Go synthesises file modes on Windows, so this check does not apply")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := saveCredential(path, storedCredential{APIKey: testProviderKey}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	cred, warn, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if warn == "" {
		t.Fatal("a world-readable credential produced no warning")
	}
	for _, want := range []string{"owner", "rotate"} {
		if !strings.Contains(warn, want) {
			t.Errorf("the warning does not mention %q: %s", want, warn)
		}
	}
	// And it is still USED: refusing to start over a file mode would be a worse
	// outcome than starting and saying so.
	if !cred.configured() {
		t.Error("the warning discarded the credential instead of reporting it")
	}
}

// Everything that prints a key goes through maskKey, so it must never emit one.
func TestMaskKeyNeverRevealsTheKey(t *testing.T) {
	for _, key := range []string{testProviderKey, "sk-or-v1-abcdefghijklmnopqrstuvwxyz", "0123456789ab"} {
		got := maskKey(key)
		if strings.Contains(got, key) {
			t.Errorf("maskKey(%q) returned the key itself: %q", key, got)
		}
		if !strings.Contains(got, key[len(key)-4:]) {
			t.Errorf("maskKey(%q) = %q, which cannot be used to recognise the key", key, got)
		}
	}
	// A short key is closer to guessable, so it gets less, not more.
	for _, short := range []string{"abc", "sk-12345678"} {
		got := maskKey(short)
		if strings.Contains(got, short) || strings.Contains(got, short[len(short)-2:]) {
			t.Errorf("maskKey(%q) leaked part of a short key: %q", short, got)
		}
		if !strings.Contains(got, fmt.Sprint(len(short))) {
			t.Errorf("maskKey(%q) = %q, which does not say how long it is", short, got)
		}
	}
	if maskKey("") != "(none)" {
		t.Errorf("maskKey(\"\") = %q", maskKey(""))
	}
}
