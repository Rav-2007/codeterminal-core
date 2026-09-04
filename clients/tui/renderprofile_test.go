package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// renderProbeEnv marks a subprocess started by the cross-process determinism
// tests. Declared here rather than beside them because TestMain has to know
// about it and TestMain cannot live behind a build tag -- see
// renderprofile_pty_test.go, which is where those tests are.
const renderProbeEnv = "TUI_RENDER_PROBE"

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
