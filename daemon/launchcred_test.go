package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// The launch credential: one JSON line on stdin instead of a key in the
// environment (launchcred.go). These run on every platform CI tests, Windows
// included -- TestLaunchCredentialOverARealPipe is the one that reads a real
// OS pipe there. The Linux-only half, a real daemon whose /proc environ is
// inspected, is launchcred_environ_linux_test.go.

// A fake value, long enough for the literal scrubber (credMinLen) and shaped
// like no provider's key.
const launchFake = "FAKE-stdin-handoff-0451-not-real"

func TestLaunchCredentialParsesWhatALauncherSends(t *testing.T) {
	p := protocol.Providers()[0]
	cases := []struct {
		name, line string
		want       launchCredential
	}{
		{"nothing to hand over", `{}`, launchCredential{}},
		{"a key, trimmed", `{"api_key":"  ` + launchFake + `  "}`, launchCredential{APIKey: launchFake}},
		{"a key and its base", `{"api_key":"` + launchFake + `","api_base":"https://example.test/v1"}`,
			launchCredential{APIKey: launchFake, APIBase: "https://example.test/v1"}},
		{"a provider names the base", `{"api_key":"` + launchFake + `","provider":"` + strings.ToUpper(p.ID) + `"}`,
			launchCredential{APIKey: launchFake, APIBase: p.APIBase, Provider: strings.ToUpper(p.ID)}},
		{"a provider and its own base agree", `{"api_key":"` + launchFake + `","provider":"` + p.ID + `","api_base":"` + p.APIBase + `/"}`,
			launchCredential{APIKey: launchFake, APIBase: p.APIBase + "/", Provider: p.ID}},
		{"a proxy key in its own field", `{"proxy_key":"` + launchFake + `"}`, launchCredential{ProxyKey: launchFake}},
		{"surrounding whitespace", "  \t{}\t ", launchCredential{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseLaunchCredential([]byte(c.line))
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// FAIL CLOSED, and never by quoting the line: each input below carries the fake
// key, and no error may contain it.
func TestLaunchCredentialRefusesMalformedLinesWithoutEchoingThem(t *testing.T) {
	p := protocol.Providers()[0]
	other := "https://elsewhere.example/v1"
	cases := []struct{ name, line, want string }{
		{"not JSON", "api_key=" + launchFake, "not a JSON object"},
		{"an array", `["` + launchFake + `"]`, "not a JSON object"},
		{"null", "null", "not a JSON object"},
		{"a misspelt field", `{"apikey":"` + launchFake + `"}`, `unknown field "apikey"`},
		{"data after the object", `{"api_key":"` + launchFake + `"} {"api_key":"` + launchFake + `"}`, "data after the JSON object"},
		{"cut off", `{"api_key":"` + launchFake, "not valid"},
		{"a number for the key", `{"api_key":4510}`, `field "api_key" must be a string`},
		{"a space inside the key", `{"api_key":"` + launchFake + ` x"}`, "api_key on stdin contains whitespace"},
		{"a control character in the key", `{"api_key":"` + launchFake + `\u0007"}`, "api_key on stdin contains whitespace or a control character"},
		{"a header break in the proxy key", `{"proxy_key":"` + launchFake + `\r\nX-Evil: 1"}`, "proxy_key on stdin contains"},
		{"an unknown provider holding the key", `{"provider":"` + launchFake + `"}`, "provider on stdin is not one this daemon knows"},
		{"provider and base disagree", `{"api_key":"` + launchFake + `","provider":"` + p.ID + `","api_base":"` + other + `"}`, "disagree"},
		{"a base with no scheme", `{"api_key":"` + launchFake + `","api_base":"example.test/v1"}`, `api_base on stdin "example.test/v1" has no scheme`},
		{"a base with the wrong scheme", `{"api_key":"` + launchFake + `","api_base":"ftp://example.test"}`, "unsupported scheme"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseLaunchCredential([]byte(c.line))
			if err == nil {
				t.Fatal("accepted a malformed line")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
			if strings.Contains(err.Error(), launchFake) {
				t.Errorf("the error quotes the key: %q", err)
			}
		})
	}
}

func TestLaunchCredentialReadsOneLineAndNoMore(t *testing.T) {
	t.Run("stops at the first newline", func(t *testing.T) {
		r := strings.NewReader(`{"api_key":"` + launchFake + `"}` + "\n" + `{"api_key":"second"}` + "\n")
		got, err := readLaunchCredential(r, time.Second)
		if err != nil || got.APIKey != launchFake {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a line with no newline before EOF", func(t *testing.T) {
		got, err := readLaunchCredential(strings.NewReader(`{"api_key":"`+launchFake+`"}`), time.Second)
		if err != nil || got.APIKey != launchFake {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("a Windows line ending", func(t *testing.T) {
		got, err := readLaunchCredential(strings.NewReader(`{"api_key":"`+launchFake+`"}`+"\r\n"), time.Second)
		if err != nil || got.APIKey != launchFake {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("EOF with no line", func(t *testing.T) {
		if _, err := readLaunchCredential(strings.NewReader(""), time.Second); err == nil ||
			!strings.Contains(err.Error(), "without a credentials line") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a line over the cap", func(t *testing.T) {
		long := `{"api_key":"` + strings.Repeat("A", launchCredentialMaxBytes) + `"}`
		r := &countingReader{r: strings.NewReader(long + "\n" + strings.Repeat("B", 1<<20))}
		_, err := readLaunchCredential(r, time.Second)
		if err == nil || !strings.Contains(err.Error(), "longer than") {
			t.Fatalf("got %v", err)
		}
		// The cap is a cap on what is READ, not only on what is accepted.
		if r.n > launchCredentialMaxBytes+1 {
			t.Errorf("read %d bytes off stdin; the cap is %d", r.n, launchCredentialMaxBytes+1)
		}
	})
	t.Run("a launcher that never writes", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close()
		start := time.Now()
		_, err := readLaunchCredential(pr, 150*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "no credentials line arrived on stdin within") {
			t.Fatalf("got %v", err)
		}
		if waited := time.Since(start); waited > 5*time.Second {
			t.Errorf("the timeout did not bound the wait (%s)", waited)
		}
	})
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// THE PRECEDENCE: the environment wins, then the launcher's line in the stored
// slot. resolveLaunchSlot is the whole rule; main.go only calls it.
func TestLaunchCredentialSitsBelowTheEnvironment(t *testing.T) {
	const envKey = "FAKE-from-the-environment-0451"
	b1, b2 := "https://one.example/v1", "https://two.example/v1"
	cases := []struct {
		name             string
		envKey, envBase  string
		launch           launchCredential
		wantKey, wantBas string
		wantInForce      bool
		wantLog          string
	}{
		{"nothing anywhere", "", "", launchCredential{}, "", "", false, ""},
		{"only the launcher's key", "", "", launchCredential{APIKey: launchFake}, launchFake, "", true, "supplied on stdin"},
		{"MOCHIII_API_KEY still wins", envKey, "", launchCredential{APIKey: launchFake}, envKey, "", false, "outranks the key supplied on stdin"},
		{"the line's base fills an unset one", "", "", launchCredential{APIKey: launchFake, APIBase: b1}, launchFake, b1, true, "supplied on stdin"},
		{"the environment's base wins over the line's", "", b2, launchCredential{APIBase: b1}, "", b2, false, ""},
		{"a key bound to another base is withheld", "", b2, launchCredential{APIKey: launchFake, APIBase: b1}, "", b2, false, "not using the key supplied on stdin"},
		{"a key bound to the same base, spelt differently", "", b1 + "/", launchCredential{APIKey: launchFake, APIBase: b1}, launchFake, b1 + "/", true, "supplied on stdin"},
		{"an unbound key goes with the base in force", "", b2, launchCredential{APIKey: launchFake}, launchFake, b2, true, "supplied on stdin"},
		{"a base without a key fills only the base", "", "", launchCredential{APIBase: b1}, "", b1, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logged strings.Builder
			logf := func(f string, a ...any) { fmt.Fprintf(&logged, f+"\n", a...) }
			key, base, inForce := resolveLaunchSlot(c.envKey, c.envBase, c.launch, logf)
			if key != c.wantKey || base != c.wantBas || inForce != c.wantInForce {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", key, base, inForce, c.wantKey, c.wantBas, c.wantInForce)
			}
			if c.wantLog != "" && !strings.Contains(logged.String(), c.wantLog) {
				t.Errorf("log %q does not say %q", logged.String(), c.wantLog)
			}
			if strings.Contains(logged.String(), launchFake) {
				t.Errorf("the log carries the raw key: %q", logged.String())
			}
		})
	}
}

// A key handed over on stdin must OUTRANK a /connect-stored one exactly as an
// environment key does, or connecting in VS Code would report success and then
// revert at the next restart. See launcherKeyWins.
func TestALaunchKeyIsReportedAsALauncherOverride(t *testing.T) {
	t.Setenv("MOCHIII_API_KEY", "")
	t.Setenv("MOCHIII_USE_PROXY", "")
	prev := launchKeySupplied.Load()
	t.Cleanup(func() { launchKeySupplied.Store(prev) })

	launchKeySupplied.Store(false)
	if launcherKeyWins() || !keyReplaceable(ClassAuth) {
		t.Fatal("with no launcher key, a connected key should be the one in force")
	}
	launchKeySupplied.Store(true)
	if !launcherKeyWins() {
		t.Error("a key read from stdin is not reported as outranking a stored one")
	}
	if keyReplaceable(ClassAuth) {
		t.Error("a key read from stdin is reported as replaceable through Connect, which it is not until restart")
	}
}

// Prompt C's registry: a value handed over on stdin is scrubbed like any other
// credential the daemon holds -- including one that lost to MOCHIII_API_KEY.
func TestLaunchSecretsAreRegisteredWithTheScrubber(t *testing.T) {
	t.Setenv("MOCHIII_API_KEY", "")
	t.Setenv("MOCHIII_PROXY_KEY", "")
	s := &Server{launchSecrets: launchCredential{APIKey: launchFake}.secrets()}
	s.rebuildCredScrubber()
	out, n := s.credRedact("the tool printed " + launchFake + " here")
	if n == 0 || strings.Contains(out, launchFake) {
		t.Fatalf("a stdin key was not redacted: %q (%d)", out, n)
	}
}

// THE REAL PIPE. The tests above feed a strings.Reader; this one is the daemon
// code reading os.Stdin from a pipe the OS made, which on Windows is a pipe
// HANDLE with its own blocking and EOF behaviour. The test binary re-executes
// itself as the reader (TestLaunchCredentialPipeHelper below).
const launchCredHelperEnv = "LAUNCHCRED_PIPE_HELPER"

func TestLaunchCredentialPipeHelper(t *testing.T) {
	raw := os.Getenv(launchCredHelperEnv)
	if raw == "" {
		return // not the helper; an ordinary run
	}
	timeout := launchCredentialTimeout
	if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	c, err := readLaunchCredential(os.Stdin, timeout)
	if err != nil {
		fmt.Println("ERR " + err.Error())
		os.Exit(3)
	}
	// Masked: the parent asserts the key arrived without it crossing stdout.
	fmt.Printf("OK key=%s base=%s proxy=%t\n", maskKey(c.APIKey), c.APIBase, c.ProxyKey != "")
	os.Exit(0)
}

func TestLaunchCredentialOverARealPipe(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot re-execute the test binary: %v", err)
	}
	line := `{"api_key":"` + launchFake + `","api_base":"https://example.test/v1","proxy_key":"` + launchFake + `"}`
	cases := []struct {
		name, write string
		keepOpen    bool
		timeoutMS   int
		want        string
	}{
		{"a line, then the pipe closes", line + "\n", false, 5000, "OK key=" + maskKey(launchFake) + " base=https://example.test/v1 proxy=true"},
		{"no newline before the close", line, false, 5000, "OK key=" + maskKey(launchFake)},
		{"a CRLF line", line + "\r\n", false, 5000, "OK key=" + maskKey(launchFake)},
		{"closed with nothing written", "", false, 5000, "ERR stdin closed without a credentials line"},
		{"held open with nothing written", "", true, 400, "ERR no credentials line arrived on stdin within"},
		{"a line over the cap", strings.Repeat("x", launchCredentialMaxBytes+64), false, 5000, "ERR the credentials line on stdin is longer than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=^TestLaunchCredentialPipeHelper$")
			cmd.Env = append(os.Environ(), launchCredHelperEnv+"="+strconv.Itoa(c.timeoutMS))
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// In a goroutine: past the cap the reader stops, and a write into a
			// full pipe nobody drains must not hang the test.
			go func() {
				_, _ = io.WriteString(stdin, c.write)
				if !c.keepOpen {
					_ = stdin.Close()
				}
			}()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the reader did not finish; output so far:\n%s", out.String())
			}
			_ = stdin.Close()
			got := out.String()
			if !strings.Contains(got, c.want) {
				t.Errorf("reader said:\n%s\nwant it to contain %q", got, c.want)
			}
			if strings.Contains(got, launchFake) {
				t.Errorf("the raw key crossed the reader's output:\n%s", got)
			}
		})
	}
}
