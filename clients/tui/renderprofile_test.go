//go:build linux

package main

import (
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// THE RENDERER IS A FUNCTION OF STATE, WIDTH AND ONE EXPLICIT PROFILE.
//
// Everything in this file exists to hold that sentence true, because the
// transcript cache in 3.2 stores rendered bytes and concatenates fresh ones
// onto them, and its equivalence gate in 3.4 asserts incrementalRender ==
// fullRender byte-for-byte. Neither means anything against a renderer that
// consults ambient global state at draw time: the gate would compare two
// renders that were entitled to disagree, and pass, and the cache would still
// be able to show a stale transcript.
//
// See renderprofile.go for the enumeration of what the ambient state actually
// was, and for what each entry could change about the bytes.

// TestMain pins the whole suite to ONE profile, before any test can draw.
//
// Ascii and not something colourful on purpose: it is what `go test` already
// produced, since the test binary's stdout is a pipe and termenv reads a pipe
// as "no colour". Pinning it changes no existing test -- what it removes is the
// ways that could STOP being true. A developer with CLICOLOR_FORCE exported ran
// a colourful suite while CI ran a colourless one, and whichever test drew
// first decided for all the rest.
//
// Tests that care about colour ask for it explicitly with withColorProfile,
// which is also how the equivalence gate gets to run somewhere the styles
// actually emit escape sequences.
func TestMain(m *testing.M) {
	if os.Getenv(renderProbeEnv) == "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	os.Exit(m.Run())
}

// withColorProfile runs f with p pinned, and puts the previous profile back.
// Process-global, so it must not be used from a parallel test; nothing in this
// package calls t.Parallel.
func withColorProfile(t *testing.T, p termenv.Profile, f func()) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	defer lipgloss.SetColorProfile(prev)
	f()
}

// ---------------------------------------------------------------------------
// The fixed subject. Every render below is of exactly this state at exactly
// this width, so any difference in bytes is a difference in the renderer.

const determinismWidth = 40

func determinismTurns() []turn {
	return []turn{
		{role: roleUser, text: "hello there"},
		{role: roleAssistant, text: "answer with CJK 日本語, an arrow →, an ambiguous ± and a wide 　space", reasoning: "some thinking"},
		{role: roleSystem, text: "a system note"},
		{role: roleSevered, text: "the daemon never answered"},
	}
}

func renderTranscriptForTest() string {
	return wrapToWidth(renderTranscript(determinismTurns(), determinismWidth), determinismWidth)
}

// renderViewForTest draws the WHOLE frame, input line included, with an
// overflowing line of ambiguous-width runes in the input. That is the one place
// a locale can still reach; see TestKnownGapTheInputLineFollowsTheLocale.
func renderViewForTest() string {
	m := newChatModel("probe", "/w", "/w", nil)
	u, _ := m.Update(tea.WindowSizeMsg{Width: determinismWidth, Height: 20})
	m = u.(chatModel)
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // dismiss the splash
	m = u.(chatModel)
	for _, r := range strings.Repeat("→±·※", 30) {
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(chatModel)
	}
	for _, tn := range determinismTurns() {
		m.appendTurn(tn)
	}
	m.refreshViewport()
	return m.View()
}

// ---------------------------------------------------------------------------
// In-process: the profile is a parameter, and it is the only one.

func TestSameStateAndWidthRenderIdenticalBytes(t *testing.T) {
	first := renderTranscriptForTest()
	second := renderTranscriptForTest()
	if first != second {
		t.Fatalf("two renders of the same state at the same width disagreed:\n first %q\nsecond %q", first, second)
	}
}

// Setting a profile, changing it, and setting it back must reproduce the
// earlier bytes EXACTLY. If it does not, something downstream of the profile is
// latching state of its own, and 3.4 cannot sweep profiles.
func TestTheColorProfileIsAParameterAndRoundTrips(t *testing.T) {
	var atANSI256a, atAscii, atANSI256b string
	withColorProfile(t, termenv.ANSI256, func() { atANSI256a = renderTranscriptForTest() })
	withColorProfile(t, termenv.Ascii, func() { atAscii = renderTranscriptForTest() })
	withColorProfile(t, termenv.ANSI256, func() { atANSI256b = renderTranscriptForTest() })

	if atANSI256a != atANSI256b {
		t.Errorf("the same profile rendered differently the second time:\n first %q\nsecond %q", atANSI256a, atANSI256b)
	}
	if atANSI256a == atAscii {
		t.Error("ANSI256 and Ascii rendered identically -- the profile is not reaching the styles, " +
			"so every profile-swept test below is asserting nothing")
	}
	if !strings.Contains(atANSI256a, "\x1b[38;5;") {
		t.Errorf("ANSI256 produced no 256-colour SGR, so the styles are not being applied: %q", atANSI256a)
	}
}

// The profile must be PINNED, not detected -- ColorProfile() must answer from
// storage. If detection were still live, this would re-read the environment.
func TestTheProfileIsPinnedRatherThanDetected(t *testing.T) {
	withColorProfile(t, termenv.ANSI256, func() {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("TERM", "dumb")
		if got := lipgloss.ColorProfile(); got != termenv.ANSI256 {
			t.Fatalf("the environment changed the profile after it was pinned: got %v, want ANSI256", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Structural: main() must pin before it can draw.

func TestMainPinsTheProfileBeforeAnythingElse(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(file, "main")
	if fn == nil {
		t.Fatal("main.go has no main()")
	}
	if len(fn.Body.List) == 0 {
		t.Fatal("main() is empty")
	}
	call, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("main()'s first statement is %T, not the pin", fn.Body.List[0])
	}
	if !callsFunc(call.X, "pinColorProfile") {
		t.Fatalf("main()'s first statement is not pinColorProfile(). The colour profile " +
			"must be resolved before anything can render, or the first Style.Render " +
			"in the process decides it instead -- see renderprofile.go")
	}
}

func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func callsFunc(e ast.Expr, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == name
}

// ---------------------------------------------------------------------------
// Cross-process: the environment cannot reach the bytes, and neither can order.

const (
	renderProbeEnv   = "TUI_RENDER_PROBE"
	renderProbeMode  = "TUI_RENDER_PROBE_MODE"
	renderProbeProf  = "TUI_RENDER_PROBE_PROFILE"
	renderProbeStart = "<<<"
	renderProbeEnd   = ">>>"
)

var probeProfiles = map[string]termenv.Profile{
	"ascii":     termenv.Ascii,
	"ansi":      termenv.ANSI,
	"ansi256":   termenv.ANSI256,
	"truecolor": termenv.TrueColor,
}

// TestRenderProbeChild is the child half of every subprocess test here. It is
// skipped in an ordinary run.
func TestRenderProbeChild(t *testing.T) {
	if os.Getenv(renderProbeEnv) == "" {
		t.Skip("subprocess half of the determinism tests")
	}
	var out string
	switch mode := os.Getenv(renderProbeMode); mode {
	case "transcript", "view":
		// The profile is supplied, so the environment is the only thing left
		// that could change the answer -- which is the point being tested.
		lipgloss.SetColorProfile(probeProfiles[os.Getenv(renderProbeProf)])
		if mode == "view" {
			out = renderViewForTest()
		} else {
			out = renderTranscriptForTest()
		}

	// The two ordering cases start from the SAME environment and differ only in
	// when the environment changes relative to a render. Without a pin the
	// profile is latched by whichever render happens first, so these disagree.
	case "mutate-then-render":
		pinColorProfile()
		os.Setenv("CLICOLOR_FORCE", "1")
		out = renderTranscriptForTest()
	case "render-then-mutate":
		pinColorProfile()
		_ = renderTranscriptForTest()
		os.Setenv("CLICOLOR_FORCE", "1")
		out = renderTranscriptForTest()

	default:
		t.Fatalf("unknown probe mode %q", mode)
	}
	fmt.Printf("%s%s%s", renderProbeStart, hex.EncodeToString([]byte(out)), renderProbeEnd)
}

// runRenderProbe runs the child and returns its render, hex-encoded. tty
// decides whether the child's stdout is a terminal, which termenv reads.
func runRenderProbe(t *testing.T, tty bool, env ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestRenderProbeChild")
	cmd.Env = append(os.Environ(), append([]string{renderProbeEnv + "=1"}, env...)...)

	var raw string
	if tty {
		master, slaveName := ptyPair(t)
		setWinsize(t, master, 30, 100)
		slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = slave, slave
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = slave.Close()
		read := make(chan string, 1)
		go func() {
			b, _ := io.ReadAll(master)
			read <- string(b)
		}()
		_ = cmd.Wait()
		select {
		case raw = <-read:
		case <-time.After(20 * time.Second):
			t.Fatal("the probe's pty never closed")
		}
		_ = master.Close()
	} else {
		b, _ := cmd.CombinedOutput()
		raw = string(b)
	}

	i := strings.Index(raw, renderProbeStart)
	j := strings.Index(raw, renderProbeEnd)
	if i < 0 || j < i {
		t.Fatalf("the probe rendered nothing. Output was:\n%s", raw)
	}
	return raw[i+len(renderProbeStart) : j]
}

// The environment matrix. Every entry names something termenv consults, and
// every one of them changed the bytes before the profile was pinned.
var determinismEnvironments = []struct {
	name string
	tty  bool
	env  []string
}{
	{"pipe", false, []string{"CI=", "NO_COLOR=", "CLICOLOR=", "CLICOLOR_FORCE="}},
	{"pipe/CLICOLOR_FORCE=1", false, []string{"CI=", "CLICOLOR_FORCE=1"}},
	{"pipe/NO_COLOR=1", false, []string{"CI=", "NO_COLOR=1"}},
	{"pipe/CI=1", false, []string{"CI=1"}},
	{"pipe/LANG=ja_JP.UTF-8", false, []string{"CI=", "LANG=ja_JP.UTF-8", "LC_ALL=ja_JP.UTF-8"}},
	{"pipe/RUNEWIDTH_EASTASIAN=1", false, []string{"CI=", "RUNEWIDTH_EASTASIAN=1"}},
	{"tty/TERM=xterm-256color", true, []string{"CI=", "NO_COLOR=", "CLICOLOR_FORCE=", "TERM=xterm-256color"}},
	{"tty/TERM=xterm", true, []string{"CI=", "NO_COLOR=", "CLICOLOR_FORCE=", "TERM=xterm"}},
	{"tty/TERM=dumb", true, []string{"CI=", "NO_COLOR=", "CLICOLOR_FORCE=", "TERM=dumb"}},
	{"tty/TERM unset", true, []string{"CI=", "NO_COLOR=", "CLICOLOR_FORCE=", "TERM="}},
	{"tty/COLORTERM=truecolor", true, []string{"CI=", "NO_COLOR=", "CLICOLOR_FORCE=", "TERM=xterm-256color", "COLORTERM=truecolor"}},
	{"tty/NO_COLOR=1", true, []string{"CI=", "TERM=xterm-256color", "NO_COLOR=1"}},
	{"tty/CI=1", true, []string{"CI=1", "TERM=xterm-256color"}},
	{"tty/GOOGLE_CLOUD_SHELL", true, []string{"CI=", "NO_COLOR=", "TERM=xterm", "GOOGLE_CLOUD_SHELL=true"}},
}

// MEASURED BEFORE THE PIN, transcript at width 40, identical model state:
// three distinct byte strings across this matrix -- 291 bytes colourless, 382
// with 16-colour SGR, 436 with 256-colour SGR. The environment, not the state,
// was choosing.
//
// What is asserted now: for a GIVEN profile the environment cannot change a
// single byte, and different profiles do change the bytes -- so the profile is
// carrying the whole difference and nothing else is leaking in. A style that
// started consulting the terminal's background colour (AdaptiveColor) would
// break the first assertion, because a pty and a pipe answer that query
// differently.
func TestTranscriptRenderIsByteIdenticalAcrossEnvironments(t *testing.T) {
	if testing.Short() {
		t.Skip("starts one subprocess per environment")
	}
	perProfile := map[string]string{}
	for _, prof := range []string{"ascii", "ansi", "ansi256", "truecolor"} {
		var first, firstName string
		for _, e := range determinismEnvironments {
			env := append([]string{renderProbeMode + "=transcript", renderProbeProf + "=" + prof}, e.env...)
			got := runRenderProbe(t, e.tty, env...)
			if first == "" {
				first, firstName = got, e.name
				continue
			}
			if got != first {
				t.Errorf("profile %s: %s rendered different bytes from %s.\n%s: %s\n%s: %s",
					prof, e.name, firstName, firstName, first, e.name, got)
			}
		}
		perProfile[prof] = first
	}

	// And the profile must actually REACH the styles, or the invariance above
	// would be the invariance of a renderer that emits no colour at all.
	//
	// THREE distinct outputs, not four: TrueColor and ANSI256 agree because the
	// palette in styles.go is written as 256-colour indices ("205", "44"), and
	// termenv only ever converts a colour DOWN to what a profile can express --
	// a 256-index colour on a truecolour terminal stays "38;5;205". That is
	// correct, so it is asserted as >= 3 rather than == 4: moving the palette to
	// hex would make it 4 and must not fail this test.
	distinct := map[string][]string{}
	for _, prof := range []string{"ascii", "ansi", "ansi256", "truecolor"} {
		distinct[perProfile[prof]] = append(distinct[perProfile[prof]], prof)
	}
	if len(distinct) < 3 {
		for out, profs := range distinct {
			t.Logf("%v -> %s", profs, out)
		}
		t.Errorf("four profiles produced only %d distinct renders; the profile is not "+
			"reaching the styles, so the invariance asserted above is vacuous", len(distinct))
	}
	if perProfile["ansi"] == perProfile["ansi256"] {
		t.Error("ANSI and ANSI256 rendered identically; the profile is not reaching the styles")
	}
}

// ORDER, ISOLATED FROM ENVIRONMENT. Both children start in the same
// environment and both end in the same environment; they differ only in whether
// a render happened before the change.
//
// MEASURED BEFORE THE PIN: 382 bytes when the environment changed first, 291
// when a render did. Same state, same width, same environment at the moment of
// the render being compared -- the difference was entirely "had anything drawn
// yet". In a test binary that reads as "did another test run first".
func TestRenderDoesNotDependOnWhatDrewFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("starts subprocesses")
	}
	env := []string{"CI=", "NO_COLOR=", "CLICOLOR=", "CLICOLOR_FORCE="}
	a := runRenderProbe(t, false, append([]string{renderProbeMode + "=mutate-then-render"}, env...)...)
	b := runRenderProbe(t, false, append([]string{renderProbeMode + "=render-then-mutate"}, env...)...)
	if a != b {
		t.Fatalf("the render changed depending on whether anything had drawn before it.\n"+
			"environment changed first: %s\nrender happened first:      %s", a, b)
	}
}

// A KNOWN GAP, PINNED SO IT CANNOT CHANGE SILENTLY.
//
// go-runewidth decides the width of AMBIGUOUS runes from the locale at package
// init (LC_ALL/LC_CTYPE/LANG, or RUNEWIDTH_EASTASIAN), and bubbles' textinput
// uses it to decide how far to scroll an overflowing input line. So the input
// line -- and only the input line -- still follows the locale.
//
// It is left alone rather than forced to a constant: forcing it would make the
// input scroll wrongly for CJK users, to buy determinism on a line that nothing
// caches. The transcript, which IS cached, is asserted here to be free of it.
//
// If the View halves stop differing, the gap has closed: delete this test and
// the note in renderprofile.go.
func TestKnownGapTheInputLineFollowsTheLocale(t *testing.T) {
	if testing.Short() {
		t.Skip("starts subprocesses")
	}
	probe := func(mode, eastAsian string) string {
		return runRenderProbe(t, false,
			renderProbeMode+"="+mode,
			renderProbeProf+"=ansi256",
			"CI=", "RUNEWIDTH_EASTASIAN="+eastAsian)
	}

	if narrow, wide := probe("transcript", "0"), probe("transcript", "1"); narrow != wide {
		t.Errorf("the locale reached the TRANSCRIPT, which the render cache stores.\n"+
			"EastAsianWidth=0: %s\nEastAsianWidth=1: %s", narrow, wide)
	}
	if narrow, wide := probe("view", "0"), probe("view", "1"); narrow == wide {
		t.Fatal("the input line no longer follows the locale. That gap has closed -- " +
			"delete this test and the note in renderprofile.go.")
	}
}
