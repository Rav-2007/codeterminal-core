package mcp

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// MEMO ITEM 2 (R1.5). VERDICT: FIX, by the daemon/ owner, 2026-09-13.
//
// `/mcp-server` runs `mochiii-daemon mcp list`, which STARTS the configured
// servers -- that is how it enumerates tools -- and puts the combined output in
// a system turn ON THE USER'S SCREEN. A third-party server that prints a
// credential at startup therefore reached a person. It is the only path in the
// client that renders daemon stderr.
//
// THE REDACTOR IS BUILT FROM THE LITERAL BYTES THIS DAEMON HANDED THIS
// SUBPROCESS, which is the whole reason it lives here and not in the TUI. The
// client sees an opaque blob and could only match SHAPES -- and this is a coding
// assistant whose own output routinely contains key-format documentation,
// example configuration and deliberately fake fixtures. A shape matcher in the
// client mangles those AND still misses the credential that does not look like
// one. Wrong in both directions, in the one place a user reads diagnostics.
func TestRedactEnvValues_ReplacesProvisionedValuesNotNames(t *testing.T) {
	const secret = "s3cr3t-deploy-token-value"
	env := []string{"MYSERVER_TOKEN=" + secret, "PATH=/usr/bin:/bin"}

	// The line a server actually prints: its own config echoed back at startup.
	got := redactEnvValues("starting with MYSERVER_TOKEN="+secret+"\n", env, []string{"MYSERVER_TOKEN"})
	if strings.Contains(got, secret) {
		t.Errorf("the provisioned value survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:MYSERVER_TOKEN]") {
		t.Errorf("no placeholder naming the variable: %q", got)
	}

	line := "cannot start: " + secret + " was rejected"
	got = redactEnvValues(line, env, []string{"MYSERVER_TOKEN"})
	if strings.Contains(got, secret) {
		t.Errorf("the value survived mid-sentence: %q", got)
	}
}

// ALLOW-LISTED NAMES ONLY, NOT BaselineEnvNames.
//
// Redacting PATH would turn `cannot find node in /usr/bin:/bin` into
// `cannot find node in [REDACTED:PATH]` and destroy the diagnostic this path
// exists to deliver. The allow-list is where an operator puts a credential BY
// CONSTRUCTION -- it is the list they had to write in order to grant it.
func TestRedactEnvValues_LeavesBaselineVariablesAlone(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "MYSERVER_TOKEN=abcdefghijklmnop"}
	line := "cannot find node in /usr/bin:/bin"

	got := redactEnvValues(line, env, []string{"MYSERVER_TOKEN"})
	if got != line {
		t.Errorf("a baseline variable's value was redacted, destroying the diagnostic:\n got  %q\n want %q", got, line)
	}
}

// SHORT VALUES ARE SKIPPED, and the threshold is a judgement stated in the code.
// A one-character value would replace every occurrence of that character; an
// eight-character floor keeps the replacement meaningful.
func TestRedactEnvValues_SkipsShortAndEmptyValues(t *testing.T) {
	env := []string{"SHORT=abc", "EMPTY=", "LONGENOUGH=abcdefghij"}
	line := "abc happened, and abcdefghij too"

	got := redactEnvValues(line, env, []string{"SHORT", "EMPTY", "LONGENOUGH"})
	if !strings.Contains(got, "abc happened") {
		t.Errorf("a 3-character value was redacted, mangling unrelated text: %q", got)
	}
	if strings.Contains(got, "abcdefghij") {
		t.Errorf("a value at or above the floor survived: %q", got)
	}
	if strings.Contains(got, "[REDACTED:EMPTY]") {
		t.Errorf("an empty value produced a replacement: %q", got)
	}
}

// THE PARTIALITY IS ENUMERABLE, which is the whole difference from a shape
// matcher's. This pins what the control does NOT catch so the register row
// cannot quietly imply otherwise: a credential the server reads from its own
// config file or keychain (the daemon never held those bytes), and anything a
// launcher adds -- Connect's own header records handing a reference Node server
// 2 variables and the child reporting 25, the other 23 being npx's.
func TestRedactEnvValues_DoesNotTouchValuesTheDaemonNeverProvisioned(t *testing.T) {
	env := []string{"MYSERVER_TOKEN=abcdefghijklmnop"}
	line := "read key sk-fromitsownkeychain-not-ours from ~/.config"

	got := redactEnvValues(line, env, []string{"MYSERVER_TOKEN"})
	if got != line {
		t.Errorf("a value the daemon never provisioned was altered: %q", got)
	}
}

// THE WIRING, through a real subprocess. This is the test the memo specified.
//
// The unit tests above prove redactEnvValues strips a value. They stayed GREEN
// when Connect's wrapper was removed -- which is the difference between "a
// control exists" and "a control is applied", and the gap this pass keeps
// finding. This one launches a real server, hands it a real allow-listed value,
// has it print that value to stderr at startup, and asserts what the caller's
// sink actually received.
func TestConnect_RedactsProvisionedValuesFromServerStderr(t *testing.T) {
	const varName = "MCPTEST_DEPLOY_TOKEN"
	const secret = "tok-abcdefghijklmnopqrstuvwxyz"
	t.Setenv(varName, secret)
	t.Setenv("ECHOSERVER_PRINT_ENV", varName)

	bin := filepath.Join(t.TempDir(), exeName("echoserver"))
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/echoserver").CombinedOutput(); err != nil {
		t.Fatalf("building echoserver: %v\n%s", err, out)
	}

	var sink safeBuffer
	client, err := Connect(context.Background(), LaunchConfig{
		Name:     "redactme",
		Command:  bin,
		EnvAllow: []string{varName, "ECHOSERVER_PRINT_ENV"},
		Stderr:   &sink,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	// WAIT FOR THE LINE, DO NOT ASSUME IT HAS ARRIVED.
	//
	// The first version of this test read the sink immediately after Connect
	// returned, on the assumption that a completed handshake meant the startup
	// line had been copied. It had not: os/exec copies cmd.Stderr on a
	// goroutine of its own, with no ordering against the handshake. That
	// assumption held on a developer machine and FAILED on CI's first run --
	// the vacuity floor fired with Got: "", which is the floor doing exactly
	// its job and refusing to report a pass for a fixture that said nothing.
	//
	// A test that is green because it won a race is not evidence, and this
	// repository has paid for that lesson elsewhere. So: poll to a deadline.
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = sink.String()
		if strings.Contains(got, "startup:") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Vacuity floor: if the fixture printed nothing, a clean result would mean
	// "no stderr" rather than "redacted stderr", and this test would pass for
	// a server that never spoke.
	if !strings.Contains(got, "startup:") {
		t.Fatalf("vacuity floor: the fixture wrote no startup line, so this asserts nothing. Got: %q", got)
	}
	if strings.Contains(got, secret) {
		t.Errorf("a provisioned credential reached the caller's stderr sink: %q\n"+
			"/mcp-server renders this stream on the user's screen.", got)
	}
	if !strings.Contains(got, "[REDACTED:"+varName+"]") {
		t.Errorf("the value was not replaced with its named placeholder: %q", got)
	}
	// The NAME must survive: the diagnostic is "this variable was wrong", and
	// redacting the name too would leave the user with nothing to act on.
	if !strings.Contains(got, varName) {
		t.Errorf("the variable NAME was lost, destroying the diagnostic: %q", got)
	}
}

// safeBuffer is a strings.Builder guarded for the copier goroutine os/exec runs
// for cmd.Stderr, so this test is clean under -race.
type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Both defects the end-to-end test found, pinned as unit cases so they cannot
// come back quietly.
func TestRedactEnvValues_NoNestedPlaceholdersAndNoPointerRedaction(t *testing.T) {
	// A variable whose value NAMES another variable. Redacting it would strip
	// the name out of the diagnostic and protect nothing.
	env := []string{"PRINT_WHICH=DEPLOY_TOKEN", "DEPLOY_TOKEN=tok-abcdefghijklmnop"}
	allow := []string{"PRINT_WHICH", "DEPLOY_TOKEN"}
	got := redactEnvValues("startup: DEPLOY_TOKEN=tok-abcdefghijklmnop\n", env, allow)

	if strings.Contains(got, "tok-abcdefghijklmnop") {
		t.Errorf("the credential survived: %q", got)
	}
	if !strings.Contains(got, "DEPLOY_TOKEN=") {
		t.Errorf("the variable NAME was redacted as another variable's value, "+
			"destroying the diagnostic: %q", got)
	}
	if strings.Contains(got, "[REDACTED:[REDACTED:") {
		t.Errorf("a placeholder was redacted inside another placeholder: %q", got)
	}
}
