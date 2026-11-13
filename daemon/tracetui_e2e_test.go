//go:build trace && linux

package main

// LINUX ONLY, for the reason clients/tui's own pty suites are: allocating a pty is per
// kernel (TIOCSPTLCK and TIOCGPTN are Linux's; the BSDs have others), and a portable
// second implementation is one nobody runs. `make crossvet` compiles this package for
// darwin and windows with -tags trace, which is how the first version of this file,
// tagged `unix`, was found not to build there.
//
// THE TERMINAL CLIENT'S TRACE, END TO END: the real client in a real terminal
// against the real traced daemon, with dummy secrets planted wherever a person
// can type one.
//
// The binary is the one this test binary's tags build (agente2e_test.go): with
// -tags trace it is the traced daemon, and a hard link to it named `mochiii` is
// the traced client (the role comes from the file name, programrole.go). The
// client runs under a pty, MOCHIII_TRACE=1, with the daemon's XDG_RUNTIME_DIR, so
// the two write into one trace directory: daemon-<pid>.jsonl and tui-<pid>.jsonl.
//
// What it asserts, besides the scenarios' own shapes:
//  1. no canary is in any trace file, raw or as base64, hex or URL-encoded. The
//     canaries are a typed prompt, a command's argument, a key typed at /connect's
//     masked prompt and an argument of /mcp-server add, each as a recognised key
//     shape and as a plain string;
//  2. every line of both files fits the schema strictly (the daemon's tracecheck:
//     only schema keys, every string an enum member or an id, at most 512 bytes);
//  3. the directory is 0700 and each file 0600;
//  4. the client's lines carry the id the daemon's lines carry (the join);
//  5. with the client's switch off, there is no client file, and the traced daemon
//     still writes its own.
//
// NEUTER CHECKS (each on a throwaway copy of the repo; results are in the PR):
//   - traceCommandSubject records the typed name instead of the catalog entry's:
//     the command-argument and typed-command canaries go red (1 and 2).
//   - approval_shown records the request's Arguments as the subject: 1 and 2.
//   - tuitrace.Tool and Command stop validating: the hostile-name unit tests and
//     the daemon's parity test go red.
//   - the 0700/0600 modes widened: 3. A planted symlink is a unit test (tuitrace's
//     TestASymlinkAtTheFilesNameIsRefused...): the client's pid is not known before
//     it starts, so the name cannot be planted here.
//   - the Trace field sent with the switch off: the request-bytes test goes red.
//   - the id the client sends is not the id on its lines: 4.

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"mochiii/daemon/trace/tracecheck"
)

// --- a terminal for the client ----------------------------------------------------

type tuiScreen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *tuiScreen) pump(master *os.File) {
	chunk := make([]byte, 4096)
	for {
		n, err := master.Read(chunk)
		if n > 0 {
			data := string(chunk[:n])
			s.mu.Lock()
			s.buf.WriteString(data)
			s.mu.Unlock()
			// Bubble Tea asks two questions on start and waits for the answers.
			if strings.Contains(data, "\x1b]11;?") {
				_, _ = master.Write([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
			}
			if strings.Contains(data, "\x1b[6n") {
				_, _ = master.Write([]byte("\x1b[1;1R"))
			}
		}
		if err != nil {
			return
		}
	}
}

var reANSI = regexp.MustCompile("\x1b(\\[[0-9;?]*[a-zA-Z]|\\][^\x07\x1b]*(\x07|\x1b\\\\))")

// tail is the last n bytes of what the client drew, without its escape sequences:
// the end of a cumulative screen is what a failing test needs to see.
func (s *tuiScreen) tail(n int) string {
	plain := reANSI.ReplaceAllString(s.text(), "")
	if len(plain) > n {
		plain = plain[len(plain)-n:]
	}
	return plain
}

func (s *tuiScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type tuiProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	sc     *tuiScreen
	done   chan struct{}
}

// startTUI runs the client against d, with env added to its environment.
func (d *e2eDaemon) startTUI(t *testing.T, env ...string) *tuiProc {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mochiii")
	src := e2eBinary(t)
	if err := os.Link(src, bin); err != nil {
		raw, rerr := os.ReadFile(src)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(bin, raw, 0o755); werr != nil {
			t.Fatal(werr)
		}
	}
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "ptmx")
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.Command(bin, "--workspace", d.workspace, "--no-index")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color", "HOME=" + home,
		"XDG_RUNTIME_DIR=" + d.runtimeDir, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
	}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the client: %v", err)
	}
	_ = slave.Close()
	p := &tuiProc{t: t, cmd: cmd, master: master, sc: &tuiScreen{}, done: make(chan struct{})}
	go p.sc.pump(master)
	go func() { _, _ = cmd.Process.Wait(); close(p.done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
		_ = master.Close()
		if t.Failed() {
			t.Logf("the end of the client's screen:\n%s", p.sc.tail(3000))
		}
	})
	if !p.waitFor("Mochiii", 30*time.Second) {
		t.Fatalf("the client never drew:\n%s", p.sc.tail(3000))
	}
	p.write("x\x7f") // the splash swallows the first key
	time.Sleep(300 * time.Millisecond)
	return p
}

func (p *tuiProc) write(s string) {
	if _, err := p.master.Write([]byte(s)); err != nil {
		p.t.Logf("write to the terminal: %v", err)
	}
}

// typeLine types s one key at a time, as a person does (a burst would be a paste),
// then Enter.
func (p *tuiProc) typeLine(s string) {
	for _, r := range s {
		p.write(string(r))
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	p.write("\r")
}

func (p *tuiProc) waitFor(want string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(reANSI.ReplaceAllString(p.sc.text(), ""), want) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// --- reading the trace ------------------------------------------------------------

// traceFiles is the daemon's file and the client's (if any), as lines.
func traceFiles(t *testing.T) (daemon, tui []string) {
	t.Helper()
	if _, err := os.Stat(traceDirOf(t)); err != nil {
		return nil, nil
	}
	_, files := traceTree(t, traceDirOf(t))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		switch {
		case strings.HasPrefix(filepath.Base(f), "daemon-"):
			daemon = append(daemon, lines...)
		case strings.HasPrefix(filepath.Base(f), "tui-"):
			tui = append(tui, lines...)
		default:
			t.Errorf("an unexpected file in the trace directory: %s", f)
		}
	}
	return
}

func waitForTrace(t *testing.T, what string, d time.Duration, ok func(daemon, tui []string) bool) (daemon, tui []string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		daemon, tui = traceFiles(t)
		if ok(daemon, tui) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s.\ndaemon:\n%s\ntui:\n%s", what, strings.Join(daemon, "\n"), strings.Join(tui, "\n"))
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func firstKey(m map[string]int) string {
	for k := range m {
		return k
	}
	return ""
}

func has(lines []string, parts ...string) bool {
	for _, l := range lines {
		match := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

var reTurnID = regexp.MustCompile(`"turn":"([0-9a-f]{8})"`)

func turnIDs(lines []string) map[string]int {
	out := map[string]int{}
	for _, l := range lines {
		if m := reTurnID.FindStringSubmatch(l); m != nil {
			out[m[1]]++
		}
	}
	return out
}

// promptTurns is the turn ids of the daemon's lines that begin a PROMPT. The client
// also asks the daemon things that are not prompts on its own (the provider in
// force, the saved chats, a status), and each of those is a daemon turn of its own
// with an id the daemon minted.
func promptTurns(daemon []string) map[string]int {
	var starts []string
	for _, l := range daemon {
		if strings.Contains(l, `"stage":"request","status":"ok","dur_ms":0,"tool":"prompt"`) {
			starts = append(starts, l)
		}
	}
	return turnIDs(starts)
}

// stages is each line as "stage:status[:tool][:err]", for comparing a shape.
var reLineParts = regexp.MustCompile(`"stage":"([a-z_]+)","status":"([a-z]+)","dur_ms":\d+,"tool":"([^"]*)","call":\d+,"err":"([^"]*)"`)

func stagesOf(lines []string) []string {
	var out []string
	for _, l := range lines {
		if m := reLineParts.FindStringSubmatch(l); m != nil {
			s := m[1] + ":" + m[2]
			if m[3] != "" {
				s += ":" + m[3]
			}
			if m[4] != "" {
				s += ":" + m[4]
			}
			out = append(out, s)
		}
	}
	return out
}

// checkAll holds every file's every line to the schema, and the modes to 0700 and
// 0600, and returns the trace tree as one blob for the canary search.
func checkAll(t *testing.T) []byte {
	t.Helper()
	schema, err := tracecheck.Load("trace/trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	blob, files := traceTree(t, traceDirOf(t))
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if errs := schema.CheckFile(raw); len(errs) != 0 {
			for _, e := range errs {
				t.Errorf("%s: %v", filepath.Base(f), e)
			}
		}
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v (%v), want 0600", f, fi, err)
		}
	}
	if fi, err := os.Stat(traceDirOf(t)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the trace directory is %v (%v), want 0700", fi, err)
	}
	return blob
}

// writeEvidence copies the scenario's two files where MOCHIII_TRACE_EVIDENCE_DIR says.
func writeEvidence(t *testing.T, name string) {
	dir := os.Getenv("MOCHIII_TRACE_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, files := traceTree(t, traceDirOf(t))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		prefix := strings.SplitN(filepath.Base(f), "-", 2)[0]
		if err := os.WriteFile(filepath.Join(dir, name+"-"+prefix+".jsonl"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const dummyKey = "sk-proj-DUMMYKEYdummyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

var editSteps = [][]string{
	toolCallSSE("c1", "builtin__read_file", `{"path":"greet.go"}`),
	toolCallSSE("c2", "builtin__propose_edit", `{"path":"greet.go","search":"\"Hello\"","replace":"\"Howdy\""}`),
	withUsage(textSSE("Changed the greeting."), 1200, 34, 0.0021),
}

// lanIP is an address of this machine that is not loopback, for the scripted
// provider: the terminal client turns the agent's tools off for a model on this
// computer, so a tool-calling scenario needs the provider elsewhere. A machine with
// no such address skips the scenarios that need one.
func lanIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("no interface addresses: %v", err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			return ipn.IP.String()
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address to put the scripted provider on")
	return ""
}

// onLAN puts the scripted provider the next newE2EModel makes off loopback.
func onLAN(t *testing.T) {
	t.Helper()
	e2eModelIP = lanIP(t)
	t.Cleanup(func() { e2eModelIP = "" })
}

// startPair starts the traced daemon (a dummy key in force, so the client opens on a
// chat) and the client, both tracing.
func startPair(t *testing.T, model *e2eModel, clientEnv ...string) (*e2eDaemon, *tuiProc) {
	t.Helper()
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1", "MOCHIII_API_KEY="+dummyKey)
	return d, d.startTUI(t, append([]string{"MOCHIII_TRACE=1"}, clientEnv...)...)
}

// answerApproval presses key until the daemon's trace shows the approval settled.
func answerApproval(t *testing.T, p *tuiProc, key string) {
	t.Helper()
	waitForTrace(t, "the approval to be shown", 30*time.Second, func(_, tui []string) bool { return has(tui, `"stage":"approval_shown"`) })
	// An answer is a single deliberate key: the client ignores one that comes within
	// 500 ms of the question appearing or 600 ms of any other key (promptguard.go),
	// so wait, and space the retries further apart than that.
	time.Sleep(1200 * time.Millisecond)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		p.write(key)
		time.Sleep(900 * time.Millisecond)
		if d, _ := traceFiles(t); has(d, `"stage":"approval_result"`) {
			return
		}
	}
	t.Fatalf("the approval was never answered. Screen:\n%s", p.sc.tail(3000))
}

// --- the scenarios ----------------------------------------------------------------

func TestASuccessfulTaskIsOneTurnAcrossTheClientAndTheDaemon(t *testing.T) {
	onLAN(t)
	model := newE2EModel(t, byToolResults(editSteps...))
	d, p := startPair(t, model)
	p.typeLine("make the greeting say Howdy")
	answerApproval(t, p, "y")
	daemon, tui := waitForTrace(t, "the result to be rendered", 30*time.Second, func(_, tui []string) bool { return has(tui, `"stage":"result_rendered"`) })

	// THE JOIN: one turn id across both files.
	dt, tt := promptTurns(daemon), turnIDs(tui)
	if len(dt) != 1 || len(tt) != 1 {
		t.Fatalf("turn ids: the daemon's prompt %v, client %v; want one each", dt, tt)
	}
	for id := range dt {
		if tt[id] == 0 {
			t.Errorf("the client's turn %v is not the daemon's prompt turn %v", tt, dt)
		}
		if n := turnIDs(daemon)[id]; n < 10 {
			t.Errorf("the daemon wrote only %d lines under the joined id", n)
		}
	}
	got := stagesOf(tui)
	want := []string{"dial:start:prompt", "dial:ok:prompt", "request_sent:start:prompt", "request_sent:ok:prompt", "approval_shown:ok:builtin__propose_edit", "result_rendered:ok"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the client's lines were\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !has(daemon, `"stage":"approval_result","status":"ok"`) || !has(daemon, `"stage":"result","status":"ok"`) {
		t.Errorf("the daemon's side:\n%s", strings.Join(daemon, "\n"))
	}
	if got := readWorkspaceFile(t, d, "greet.go"); strings.Contains(got, "Howdy") {
		t.Logf("the edit is only proposed here, as the daemon's contract says: %q", got)
	}
	checkAll(t)
	writeEvidence(t, "success")
}

func TestADeniedApprovalIsSeenFromBothSides(t *testing.T) {
	onLAN(t)
	model := newE2EModel(t, byToolResults(editSteps...))
	_, p := startPair(t, model)
	p.typeLine("make the greeting say Howdy")
	answerApproval(t, p, "n")
	daemon, tui := waitForTrace(t, "the result to be rendered", 30*time.Second, func(_, tui []string) bool { return has(tui, `"stage":"result_rendered"`) })
	if !has(tui, `"stage":"approval_shown","status":"ok"`, `builtin__propose_edit`) {
		t.Errorf("the client did not record the approval being shown:\n%s", strings.Join(tui, "\n"))
	}
	if !has(daemon, `"stage":"approval_result","status":"fail"`, `"err":"denied_user"`) {
		t.Errorf("the daemon did not record the denial:\n%s", strings.Join(daemon, "\n"))
	}
	if dt, tt := promptTurns(daemon), turnIDs(tui); len(dt) != 1 || len(tt) != 1 || dt[firstKey(tt)] == 0 {
		t.Errorf("not one joined turn: the daemon's prompt %v, client %v", dt, tt)
	}
	checkAll(t)
	writeEvidence(t, "denied")
}

// A daemon that has stopped: the client's dial fails, and says how. Stopped
// gracefully the lockfile is gone (no_lockfile); killed it is left behind with
// nothing listening (unreachable).
func TestAStoppedDaemonIsADialFailureWithTheRightClass(t *testing.T) {
	for _, c := range []struct {
		name, class string
		stop        func(t *testing.T, d *e2eDaemon)
	}{
		{"stopped", "no_lockfile", func(t *testing.T, d *e2eDaemon) {
			if err := syscall.Kill(d.pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(lockPathIn(t, d.runtimeDir, d.workspace)); err != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("the daemon did not remove its lockfile on SIGTERM")
		}},
		{"killed", "unreachable", func(t *testing.T, d *e2eDaemon) {
			if err := syscall.Kill(d.pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			onLAN(t)
			model := newE2EModel(t, saysHi)
			d, p := startPair(t, model)
			c.stop(t, d)
			p.typeLine("are you there")
			_, tui := waitForTrace(t, "the dial to fail", 30*time.Second, func(_, tui []string) bool { return has(tui, `"stage":"dial","status":"fail"`) })
			got := stagesOf(tui)
			want := []string{"dial:start:prompt", "dial:fail:prompt:" + c.class}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("the client's lines were %q, want %q\nscreen:\n%s", got, want, p.sc.tail(3000))
			}
			checkAll(t)
			writeEvidence(t, "dial-"+c.name)
		})
	}
}

// A model that never answers: the daemon is waiting on it and the client on the
// daemon. Both files stop; the last line of each is a `start` that never ends.
func TestAHungModelLeavesTheTurnOpenOnBothSides(t *testing.T) {
	onLAN(t)
	model := newE2EModel(t, func(int, e2eChat) e2eReply {
		return e2eReply{lines: []string{`data: {"choices":[{"delta":{"role":"assistant"}}]}`, `data: {"choices":[{"delta":{"content":"x"}}]}`}, pause: 10 * time.Minute}
	})
	_, p := startPair(t, model)
	p.typeLine("this will hang")
	daemon, tui := waitForTrace(t, "the model call to start", 30*time.Second, func(d, _ []string) bool { return has(d, `"stage":"model_call","status":"start"`) })
	time.Sleep(1500 * time.Millisecond) // long enough for anything that was going to follow
	daemon, tui = traceFiles(t)
	if has(daemon, `"stage":"result"`) || has(tui, `"stage":"result_rendered"`) {
		t.Errorf("a turn that never finished has a result line:\ndaemon:\n%s\ntui:\n%s", strings.Join(daemon, "\n"), strings.Join(tui, "\n"))
	}
	if !has(daemon, `"stage":"model_call","status":"start"`) || !has(tui, `"stage":"request_sent","status":"start"`) {
		t.Errorf("the open stages are missing:\ndaemon:\n%s\ntui:\n%s", strings.Join(daemon, "\n"), strings.Join(tui, "\n"))
	}
	checkAll(t)
	writeEvidence(t, "hang")
}

// --- the leak test ----------------------------------------------------------------

type tuiCanaries struct{ prompt, command, connectKey, mcpArg string }

func (c tuiCanaries) all() map[string]string {
	return map[string]string{"typed prompt": c.prompt, "command argument": c.command, "key typed into /connect": c.connectKey, "MCP argument": c.mcpArg}
}

var tuiKeyShapes = tuiCanaries{
	prompt:     "sk-ant-api03-TUIPROMPTCANARYdummyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	command:    "ghp_TUICMDCANARYdummy000000000000000000",
	connectKey: "sk-proj-TUICONNECTCANARYdummyAAAAAAAAAAAAAAAAAAAAAA",
	mcpArg:     "AKIATUIMCPCANARY0001",
}

var tuiTextCanaries = tuiCanaries{
	prompt:     "plainzebra-tuipromptcanary-7f3a91",
	command:    "plainzebra-tuicmdcanary-2c88d0",
	connectKey: "plainzebra-tuiconnectcanary-a91b3e",
	mcpArg:     "plainzebra-tuimcpcanary-5d02f7",
}

func TestTheTerminalClientLeaksNoCanaryAndEveryLineFitsTheSchema(t *testing.T) {
	for name, c := range map[string]tuiCanaries{"recognised key shapes": tuiKeyShapes, "plain strings": tuiTextCanaries} {
		t.Run(name, func(t *testing.T) {
			for what, v := range c.all() {
				_, reds := scrub(v, false)
				if shape := name == "recognised key shapes"; shape != (len(reds) > 0) {
					t.Fatalf("canary %q (%s): scrub found %d redactions, but this run is %q", v, what, len(reds), name)
				}
			}
			onLAN(t)
			model := newE2EModel(t, byToolResults(withUsage(textSSE("Done."), 100, 5, 0.0002)))
			_, p := startPair(t, model)

			// 1. a command with an argument; a command that is not one; the argument
			//    of an add; and the key typed at /connect's masked prompt.
			// The client ignores Enter while a turn is in flight, so give each line time
			// to finish before the next (a line that is not a command goes to the model).
			settle := func() { time.Sleep(900 * time.Millisecond) }
			p.typeLine("/help " + c.command)
			settle()
			p.typeLine("/" + c.command)
			settle()
			// `echo` is a program that does nothing, so adding it reaches the daemon (the
			// request carries the argument) without starting anything that matters.
			p.typeLine("/mcp-server add files echo --root=" + c.mcpArg)
			if !p.waitFor("Type the server's name", 10*time.Second) {
				t.Fatalf("the add did not ask for the server's name:\n%s", p.sc.tail(3000))
			}
			settle()
			p.typeLine("files")
			waitForTrace(t, "the add to reach the daemon", 20*time.Second, func(d, _ []string) bool { return has(d, `"tool":"mcp"`) })
			settle()
			p.typeLine("/connect")
			if !p.waitFor("API key", 10*time.Second) && !p.waitFor("provider", 5*time.Second) {
				t.Fatalf("/connect did not open:\n%s", p.sc.text())
			}
			if !p.waitFor("Type to search", 10*time.Second) {
				t.Fatalf("the provider list did not open:\n%s", p.sc.tail(3000))
			}
			time.Sleep(300 * time.Millisecond)
			for _, r := range "openrouter" { // the list filters as it is typed; "On this computer" needs no key
				p.write(string(r))
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(400 * time.Millisecond)
			p.write("\r")
			if !p.waitFor("Paste your API key below", 10*time.Second) {
				t.Fatalf("the masked key prompt did not open, so the key canary would never be typed:\n%s", p.sc.tail(3000))
			}
			for _, r := range c.connectKey {
				p.write(string(r))
				time.Sleep(2 * time.Millisecond)
			}
			time.Sleep(300 * time.Millisecond)
			if strings.Contains(p.sc.text(), c.connectKey) {
				t.Errorf("the client echoed the key on screen")
			}
			// Abandon it: nothing is sent anywhere. Esc goes back to the list, and again to the chat.
			for i := 0; i < 3; i++ {
				p.write("\x1b")
				time.Sleep(400 * time.Millisecond)
			}

			// 2. a prompt carrying a canary, through to the end.
			p.typeLine("tell me about " + c.prompt)
			waitForTrace(t, "the typed prompt's result", 40*time.Second, func(_, tui []string) bool {
				return p.sc.text() != "" && strings.Contains(p.sc.text(), "tell me about") && has(tui, `"stage":"result_rendered"`)
			})
			time.Sleep(500 * time.Millisecond)

			blob := checkAll(t)
			daemon, tui := traceFiles(t)
			if len(tui) < 8 {
				t.Fatalf("the client wrote only %d lines; the scenario did not run:\n%s\nscreen:\n%s", len(tui), strings.Join(tui, "\n"), p.sc.tail(3000))
			}
			for what, v := range c.all() {
				for _, form := range canaryForms(v) {
					if bytes.Contains(blob, []byte(form)) {
						t.Errorf("the %s canary %q is in the trace as %q", what, v, form)
					}
				}
			}
			// The canaries really were typed into the client: the screen shows the
			// prompt's and the command's (the key is masked, so it does not; the MCP argument is on the add's confirmation).
			plain := reANSI.ReplaceAllString(p.sc.text(), "")
			for what, v := range map[string]string{"typed prompt": c.prompt, "command argument": c.command, "MCP argument": c.mcpArg} {
				if !strings.Contains(plain, v) && !strings.Contains(plain, v[:20]) {
					t.Errorf("the %s canary never reached the client's screen: the test would pass vacuously", what)
				}
			}
			// What the trace says about the commands: names from the catalog, else unknown.
			for _, want := range []string{
				`"stage":"command_parsed","status":"ok","dur_ms":0,"tool":"help"`,
				`"stage":"command_parsed","status":"ok","dur_ms":0,"tool":"unknown"`,
				`"stage":"command_parsed","status":"ok","dur_ms":0,"tool":"mcp-server"`,
				`"stage":"command_parsed","status":"ok","dur_ms":0,"tool":"connect"`,
				`"stage":"result_rendered","status":"ok"`,
			} {
				if !has(tui, want) {
					t.Errorf("the client's trace has no line containing %s:\n%s", want, strings.Join(tui, "\n"))
				}
			}
			// Local commands never reached the daemon: it has one turn, the prompt.
			// Prompts are the typed one, and a line that starts with "/" and is not a
			// command at all (a key after a slash is not a command word) is sent as one.
			if dt := promptTurns(daemon); len(dt) < 1 || len(dt) > 2 {
				t.Errorf("the daemon traced %d prompt turns, want the typed prompt's (and at most the odd slash line): %v", len(dt), dt)
			}
			if has(daemon, `"tool":"mcp"`) && !has(daemon, `"tool":"mcp","call":0`) {
				t.Errorf("an mcp request line is malformed")
			}
			// Every client turn is its own: four lines of commands and one prompt.
			if n := len(turnIDs(tui)); n < 4 {
				t.Errorf("the client's lines belong to %d turns, want at least one per Enter: %v", n, turnIDs(tui))
			}
			writeEvidence(t, "leak-"+strings.ReplaceAll(name, " ", "-"))
		})
	}
}

// WITH THE CLIENT'S SWITCH OFF: no client file at all (the daemon's is its own
// switch's), and the request is the one an untraced client sends.
func TestWithTheClientsSwitchOffThereIsNoClientFile(t *testing.T) {
	for _, env := range [][]string{nil, {"MOCHIII_TRACE="}, {"MOCHIII_TRACE=0"}, {"MOCHIII_TRACE=true"}} {
		t.Run(fmt.Sprint(env), func(t *testing.T) {
			onLAN(t)
			model := newE2EModel(t, saysHi)
			d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1", "MOCHIII_API_KEY="+dummyKey)
			p := d.startTUI(t, env...)
			p.typeLine("/help")
			p.typeLine("say hi")
			daemon, _ := waitForTrace(t, "the daemon's result", 40*time.Second, func(d, _ []string) bool { return has(d, `"stage":"result"`) })
			time.Sleep(300 * time.Millisecond)
			_, tui := traceFiles(t)
			if len(tui) != 0 {
				t.Errorf("the client wrote a trace with its switch off:\n%s", strings.Join(tui, "\n"))
			}
			_, files := traceTree(t, traceDirOf(t))
			for _, f := range files {
				if strings.HasPrefix(filepath.Base(f), "tui-") {
					t.Errorf("a client trace file exists: %s", f)
				}
			}
			// The daemon minted its own turn id: the client named none.
			if len(promptTurns(daemon)) != 1 {
				t.Errorf("daemon prompt turns: %v", promptTurns(daemon))
			}
		})
	}
}
