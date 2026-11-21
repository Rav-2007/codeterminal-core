package tui

import (
	"os"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Tests for inline.go: the chat printed into the terminal's own scrollback.

// inlineModel is a chat in inline mode, sized, past the splash.
func inlineModel(t *testing.T) chatModel {
	t.Helper()
	m := newTestModel()
	m.inline.on = true
	m.resizeViewport()
	m.refreshViewport()
	return m
}

// printOf is what the command an Update returned prints, or "" when it prints
// nothing. A print is always the first step of a tea.Sequence, which is read
// as the slice of commands it is: running the sequence's own command only
// hands that slice back, so nothing else in it (a stream's wait) is run.
func printOf(t *testing.T, was bool, m chatModel, cmd tea.Cmd) string {
	t.Helper()
	if was || !m.inline.inFlight || cmd == nil {
		return "" // no print was taken by this Update
	}
	msg := cmd()
	v := reflect.ValueOf(msg)
	if typeName(msg) != "tea.sequenceMsg" || v.Len() == 0 {
		t.Fatalf("a print was taken but the command is %s", typeName(msg))
	}
	first, _ := v.Index(0).Interface().(tea.Cmd)
	return printed(first())
}

// step runs one message through Update and returns what it printed.
func step(t *testing.T, m chatModel, msg tea.Msg) (chatModel, string) {
	t.Helper()
	was := m.inline.inFlight && msg != tea.Msg(inlinePrintedMsg{})
	next, cmd := m.Update(msg)
	nm := next.(chatModel)
	return nm, printOf(t, was, nm, cmd)
}

// settlePrints acknowledges prints until there is nothing left to print, and returns
// everything printed, one print per line.
func settlePrints(t *testing.T, m chatModel) (chatModel, string) {
	t.Helper()
	var all []string
	for i := 0; i < 20; i++ {
		var out string
		if m, out = ack(t, m); out == "" {
			return m, strings.Join(all, "\n")
		}
		all = append(all, out)
	}
	t.Fatal("prints never settled")
	return m, ""
}

// enterPrints presses enter and returns what that printed. (Not "enter": the
// trace build has one of its own, traceemit_test.go.)
func enterPrints(t *testing.T, m chatModel) (chatModel, string) {
	t.Helper()
	return step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

// ack is the renderer saying the last print arrived.
func ack(t *testing.T, m chatModel) (chatModel, string) {
	t.Helper()
	return step(t, m, inlinePrintedMsg{})
}

func plain(s string) string { return ansi.Strip(s) }

// A FINISHED TURN IS PRINTED ONCE AND LEAVES THE PROGRAM'S OWN LINES. /help is
// taller than the window; on the alternate screen a drag could reach only the
// part on screen. Printed, the terminal has all of it.
func TestInlineAFinishedTurnIsPrintedOnceAndLeavesTheScreen(t *testing.T) {
	m := inlineModel(t)
	m = typeText(m, "/help")
	m, out := enterPrints(t, m)
	out = plain(out)
	for _, want := range []string{"/account", "to change the ceilings"} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed /help lacks %q:\n%s", want, out)
		}
	}
	if v := plain(m.View()); strings.Contains(v, "/account") {
		t.Errorf("the printed turn is still drawn below it:\n%s", v)
	}
	m, again := ack(t, m)
	if again != "" {
		t.Errorf("the same turn was printed twice:\n%s", again)
	}
	// Idle with everything printed: the program's lines are the header, the
	// prompt box and the lines under it, and no empty transcript rows.
	if m.viewport.Height != 0 {
		t.Errorf("an empty live part takes %d row(s)", m.viewport.Height)
	}
	if rows := strings.Count(m.View(), "\n") + 1; rows > inlineSpacerRows+m.headerLineCount()+m.promptRows()+1+m.statusRows() {
		t.Errorf("the idle screen is %d rows:\n%s", rows, plain(m.View()))
	}
}

// AN ANSWER IS PRINTED A PARAGRAPH AT A TIME WHILE IT IS WRITTEN, never inside
// a code block, and the parts put together are exactly the whole printed at
// once: nothing doubled, nothing lost, one "Mochiii:" label.
func TestInlineAnAnswerIsPrintedAParagraphAtATime(t *testing.T) {
	m := inlineModel(t)
	m.state, m.streamCh, m.streamAssistant = stateStreaming, make(chan tea.Msg), -1
	m.appendTurn(turn{role: roleUser, text: "explain"})

	var prints []string
	feed := func(chunk string) {
		t.Helper()
		var out string
		m, out = step(t, m, tokenMsg(chunk))
		if out != "" {
			prints = append(prints, out)
			var more string
			if m, more = ack(t, m); more != "" {
				prints = append(prints, more)
			}
		}
	}
	feed("First paragraph, which is done.\n\nSecond para")
	if len(prints) != 1 || !strings.Contains(plain(prints[0]), "↬ First paragraph") || strings.Contains(prints[0], "Second") {
		t.Fatalf("after one finished paragraph, printed %q", prints)
	}
	if v := plain(m.View()); !strings.Contains(v, "Second para") || strings.Contains(v, "First paragraph") {
		t.Errorf("the live part should hold only the paragraph being written:\n%s", v)
	}
	feed("graph.\n\n```go\nfunc a() {\n\n\treturn\n}\n")
	last := plain(prints[len(prints)-1])
	if strings.Contains(last, "func a") || !strings.Contains(last, "Second paragraph.") {
		t.Fatalf("a blank line inside a code block was taken as a place to cut: %q", last)
	}
	feed("```\n\n- a list item\n- another\n\nTail being wri")
	feed("tten")

	// The stream ends; what is left is final, and goes when anything next
	// reaches Update.
	m.streamCh, m.state = nil, stateIdle
	if m.inline.inFlight {
		t.Fatal("a print is still waiting")
	}
	var out string
	m, out = settlePrints(t, m)
	prints = append(prints, out)

	got := strings.Join(prints, "\n")
	w := m.viewport.Width
	want := trimLineEnds(wrapToWidth(renderTranscript(m.turns, w), w))
	if plain(got) != plain(want) {
		t.Errorf("printed in parts:\n%s\n\nprinted whole:\n%s", plain(got), plain(want))
	}
	if n := strings.Count(plain(got), assistantLabel); n != 1 {
		t.Errorf("the label appears %d times", n)
	}
	if v := plain(m.View()); strings.Contains(v, "Tail") {
		t.Errorf("the finished answer is still drawn below the scrollback:\n%s", v)
	}
}

// ONE PRINT AT A TIME. Each command runs on a goroutine of its own, so a second
// print sent before the first arrived could overtake it. The second waits, and
// its turn stays on screen meanwhile.
func TestInlinePrintsWaitForTheOneBefore(t *testing.T) {
	m := inlineModel(t)
	m.appendTurn(turn{role: roleSystem, text: "first note"})
	m, out := ack(t, m)
	if !strings.Contains(out, "first note") {
		t.Fatalf("first print: %q", out)
	}
	m.appendTurn(turn{role: roleSystem, text: "second note"})
	m.refreshViewport()
	m, out = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if out != "" {
		t.Fatalf("a second print went out before the first arrived: %q", out)
	}
	if !strings.Contains(plain(m.View()), "second note") {
		t.Error("the turn waiting to be printed is not on screen")
	}
	if _, out = ack(t, m); !strings.Contains(out, "second note") {
		t.Errorf("after the first arrived, the second was not printed: %q", out)
	}
}

// A CALL STILL RUNNING STAYS ON SCREEN; its line changes when it ends.
func TestInlineARunningCallIsPrintedWhenItEnds(t *testing.T) {
	m := inlineModel(t)
	m.appendTurn(turn{role: roleActivity, act: &activityView{state: activityRunning, verb: "Read", target: "bot.py"}})
	m.refreshViewport()
	m, out := ack(t, m)
	if out != "" {
		t.Fatalf("a running call was printed: %q", out)
	}
	m.turns[0] = turn{role: roleActivity, act: &activityView{state: activityDone, verb: "Read", target: "bot.py", meta: "8 KB"}}
	m.refreshViewport()
	if _, out = ack(t, m); !strings.Contains(plain(out), "bot.py · 8 KB") {
		t.Errorf("the finished call printed %q", plain(out))
	}
}

// THE LIST OF TURNS IS NOT ONLY APPENDED TO, and nothing already printed may
// be printed again because of it.
func TestInlineTrimmingNeverReprints(t *testing.T) {
	m := inlineModel(t)
	for _, s := range []string{"one", "two", "three"} {
		m.appendTurn(turn{role: roleSystem, text: s})
	}
	m, out := ack(t, m)
	if !strings.Contains(out, "three") {
		t.Fatalf("setup print: %q", out)
	}

	// The transcript bound: dropped from the front, a marker put in.
	m.turns = append([]turn{{role: roleSystem, text: evictionMarkerPrefix + "2 earlier turns dropped"}}, m.turns[2:]...)
	if text, _ := m.takePrintable(); text != "" {
		t.Errorf("trimming the front reprinted:\n%s", text)
	}
	m.appendTurn(turn{role: roleSystem, text: "four"})
	if text, _ := m.takePrintable(); plain(text) != "\nfour" { // the blank line between turns, then the turn
		t.Errorf("after the front was trimmed, printed %q, want only the new turn", plain(text))
	}

	// A line taken back from the end.
	m.turns = m.turns[:len(m.turns)-1]
	if text, _ := m.takePrintable(); text != "" {
		t.Errorf("taking the last line back reprinted:\n%s", text)
	}
}

// WHAT AN UPDATE LINES UP WITHOUT PRINTING IS KEPT. Update hands back update's
// own model when nothing after it changed it (an allocation saved per token,
// inline.go); trimming the front moves the record of what was printed without
// printing anything, and that move must not be thrown away with the copy.
func TestInlineARecordLinedUpWithoutAPrintIsKept(t *testing.T) {
	m := inlineModel(t)
	for _, s := range []string{"one", "two", "three"} {
		m.appendTurn(turn{role: roleSystem, text: s})
	}
	m, out := settlePrints(t, m)
	if !strings.Contains(out, "three") {
		t.Fatalf("setup print: %q", out)
	}

	m.turns = append([]turn{{role: roleSystem, text: evictionMarkerPrefix + "2 earlier turns dropped"}}, m.turns[2:]...)
	type nothingMsg struct{}
	m, out = step(t, m, nothingMsg{})
	if out != "" {
		t.Errorf("trimming the front reprinted:\n%s", out)
	}
	if len(m.inline.printed) != len(m.turns) {
		t.Errorf("after the front was trimmed the record holds %d turns, want %d: the Update "+
			"handed back the model from before it was lined up", len(m.inline.printed), len(m.turns))
	}
}

// afterUpdate REPORTS EVERY CHANGE IT MAKES, each one even when nothing else
// does: Update hands back the model from before it when it reports none. A
// print on its way (inFlight) keeps the print out of each case.
func TestInlineAfterUpdateReportsWhatItChanges(t *testing.T) {
	var cmd tea.Cmd
	m := inlineModel(t)
	m.inline.inFlight = true
	if m.afterUpdate(m.welcoming(), m.promptRows(), &cmd) {
		t.Error("afterUpdate reported a change when it made none: an allocation for every streamed token")
	}

	// The first screen ends: something was said, during a turn, so the screen
	// was not the first screen to begin with and the prompt keeps its width.
	m = inlineModel(t)
	m.inline.inFlight, m.state = true, stateStreaming
	m.inline.welcome, m.inline.welcomeTurns = true, len(m.turns)+1
	if !m.afterUpdate(m.welcoming(), m.promptRows(), &cmd) || m.inline.welcome {
		t.Error("the end of the first screen was not reported, and goes with the copy")
	}

	// The prompt changes width: the first screen went.
	m = inlineModel(t)
	m.inline.inFlight = true
	m.input.Width = 3
	if !m.afterUpdate(true, m.promptRows(), &cmd) || m.input.Width == 3 {
		t.Error("the prompt's new width was not reported, and goes with the copy")
	}
}

// A REPLACED CHAT (/clear, /resume, /compact, another window) is printed below
// a rule; what was printed before it stays where it is.
func TestInlineAReplacedChatIsPrintedBelowARule(t *testing.T) {
	m := inlineModel(t)
	m.appendTurn(turn{role: roleSystem, text: "old chat"})
	m, _ = ack(t, m)
	m.turns = []turn{{role: roleUser, text: "resumed question"}}
	text, _ := m.takePrintable()
	lines := strings.Split(plain(text), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "───") || !strings.Contains(text, "resumed question") {
		t.Errorf("a replaced chat printed:\n%s", plain(text))
	}
	if strings.Contains(text, "old chat") {
		t.Error("the old chat was printed again")
	}
	// /clear: nothing to show but where the new chat begins.
	m.turns = nil
	if text, _ = m.takePrintable(); !strings.HasPrefix(plain(text), "───") {
		t.Errorf("/clear printed %q, want a rule", plain(text))
	}
}

// Nothing is printed above a screen that fills the terminal.
func TestInlineNothingIsPrintedAboveTheSplash(t *testing.T) {
	m := newChatModel("t", "/ws", "/ws", nil)
	m.inline.on = true
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatModel)
	m.appendTurn(turn{role: roleSystem, text: "note at start"})
	if text, _ := m.takePrintable(); text != "" {
		t.Errorf("printed above the splash: %q", text)
	}
	m, out := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // past the splash
	if !strings.Contains(out, "note at start") {
		t.Errorf("after the splash, the note was not printed: %q", out)
	}
}

// Leaving clears the program's own lines, so the terminal is left holding the
// chat and not a prompt box under it.
func TestInlineQuittingDrawsNothing(t *testing.T) {
	m := inlineModel(t)
	next, _ := m.quitNow(false)
	if v := next.(chatModel).View(); v != "" {
		t.Errorf("quitting still draws:\n%s", v)
	}
	// The alternate screen is left by Bubble Tea itself; nothing changes there.
	m = newTestModel()
	next, _ = m.quitNow(false)
	if next.(chatModel).View() == "" {
		t.Error("in fullscreen mode quitting blanked the screen")
	}
}

// /copy in inline mode prints the chat again with no width, and goes nowhere.
func TestInlineCopyPrintsUncutAndStays(t *testing.T) {
	m := inlineModel(t)
	long := strings.TrimSpace(strings.Repeat("a long line of the answer ", 8))
	m.appendTurn(turn{role: roleAssistant, text: long})
	m, _ = settlePrints(t, m)
	m = typeText(m, "/copy")
	m, out := enterPrints(t, m)
	if m.copyView != copyViewOff {
		t.Fatalf("/copy switched screens in inline mode (phase %v)", m.copyView)
	}
	out = plain(out)
	if !containsLine(out, long) || !strings.Contains(out, copyViewHeader) {
		t.Errorf("/copy printed:\n%s", out)
	}
}

// INLINE IS WHAT THE PROGRAM STARTS IN; --fullscreen and MOCHIII_FULLSCREEN=1
// are the way back. Read from main.go, where the program is built.
func TestTheProgramStartsInline(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	mainGo := strings.ReplaceAll(string(src), "\r\n", "\n") // git for Windows checks it out with CRLF
	for _, want := range []string{"model.inline.on = !launch.fullscreen", "if launch.fullscreen {\n\t\topts = append(opts, tea.WithAltScreen())"} {
		if !strings.Contains(mainGo, want) {
			t.Errorf("main.go does not have %q", want)
		}
	}
	t.Setenv("MOCHIII_FULLSCREEN", "")
	if fullscreenRequested(false) || !fullscreenRequested(true) {
		t.Error("--fullscreen is not what decides it")
	}
	t.Setenv("MOCHIII_FULLSCREEN", "1")
	if !fullscreenRequested(false) {
		t.Error("MOCHIII_FULLSCREEN=1 does not ask for the full screen")
	}
}

func TestAnswerSplit(t *testing.T) {
	for _, c := range []struct {
		text string
		from int
		want string // the text before the cut
	}{
		{"no blank line yet", 0, ""},
		{"one.\n\ntwo", 0, "one.\n\n"},
		{"one.\n\ntwo.\n\nthree", 0, "one.\n\ntwo.\n\n"},
		{"\n\nleading blank", 0, ""},
		{"```\na\n\nb\n```\n\nafter", 0, "```\na\n\nb\n```\n\n"},
		{"```\na\n\nb", 0, ""},
		{"one.\n\ntwo.\n\n", 6, "one.\n\ntwo.\n\n"},
	} {
		got := c.text[:answerSplit(c.text, c.from)]
		if c.from > 0 && got == c.text[:c.from] && c.want != got {
			t.Errorf("answerSplit(%q, %d) found nothing past from", c.text, c.from)
			continue
		}
		if c.want != "" && got != c.want || c.want == "" && got != c.text[:c.from] {
			t.Errorf("answerSplit(%q, %d) cuts after %q, want %q", c.text, c.from, got, c.want)
		}
	}
}

// /help says how the chat scrolls in the mode it is in.
func TestInlineHelpSaysTheTerminalScrollsTheChat(t *testing.T) {
	defer func(was bool) { helpInline = was }(helpInline)
	helpInline = true
	if h := formatSlashHelp(); !strings.Contains(h, "terminal's scrollback") || strings.Contains(h, "the wheel sends these too") {
		t.Errorf("inline /help describes the wrong scrolling:\n%s", h)
	}
	helpInline = false
	if h := formatSlashHelp(); !strings.Contains(h, "the wheel sends these too") {
		t.Error("fullscreen /help lost its scrolling line")
	}
}

// THE ANSWER BEING WRITTEN COMES STRAIGHT UNDER THE PRINTED CHAT, and the
// header after it, above the prompt box: a status line between two paragraphs
// of one answer cut it in two (first live run, 2026-11-21).
func TestInlineTheLivePartComesBeforeTheHeader(t *testing.T) {
	m := inlineModel(t)
	m.state, m.streamCh, m.streamAssistant = stateStreaming, make(chan tea.Msg), -1
	m.appendTurn(turn{role: roleUser, text: "q"})
	m, _ = step(t, m, tokenMsg("still being written"))
	lines := strings.Split(plain(m.View()), "\n")
	live, header, box := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "still being written") && live < 0:
			live = i
		case strings.Contains(l, brandName) && header < 0:
			header = i
		case strings.HasPrefix(l, promptRule) && box < 0:
			box = i
		}
	}
	if lines[0] != "" || !(live > 0 && live < header && header < box) {
		t.Errorf("want a blank row, the answer, the header, the box; got rows %d, %d, %d:\n%s", live, header, box, strings.Join(lines, "\n"))
	}
	if n := len(lines); n > m.height {
		t.Errorf("the program's lines (%d) are taller than the terminal (%d)", n, m.height)
	}
}

// THE CHAT STARTS ON A CLEAN TERMINAL (owner, 2026-11-21: "clear the upper
// stuff"), and the startup lines that were news, not progress, come into the
// chat instead of being cleared away with the rest.
func TestInlineStartsCleanAndKeepsTheNotesThatMatter(t *testing.T) {
	defer func(was []string) { startNotes = was }(startNotes)
	startNotes = nil
	var printedBefore strings.Builder
	say(&printedBefore, "Starting Mochiii for /p…\n")
	note(&printedBefore, "Note: Mochiii 1.5.0, from another install, is answering in this folder; this is 1.6.0.\n")
	note(&printedBefore, "%s", "") // notAProjectNote when the folder is a project
	if !strings.Contains(printedBefore.String(), "Starting Mochiii") || !strings.Contains(printedBefore.String(), "another install") {
		t.Fatalf("note and say must both still print: %q", printedBefore.String())
	}

	m := inlineModel(t)
	var term strings.Builder
	openInline(&m, &term, true)
	if term.String() != clearTerminal {
		t.Errorf("the terminal was sent %q, want the clear command's %q", term.String(), clearTerminal)
	}
	last := m.turns[len(m.turns)-1]
	if last.role != roleSystem || last.text != "Note: Mochiii 1.5.0, from another install, is answering in this folder; this is 1.6.0." {
		t.Errorf("the kept note did not come into the chat as it was: %+v", last)
	}
	if strings.Contains(last.text, "Starting Mochiii") {
		t.Error("a progress line was kept")
	}
	if startNotes != nil {
		t.Error("the notes were left to be added again")
	}

	// Not a terminal (output redirected): nothing is written to it.
	term.Reset()
	openInline(&m, &term, false)
	if term.Len() != 0 {
		t.Errorf("cleared something that is not a terminal: %q", term.String())
	}
	// And main.go does this only for an inline chat.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	mainGo := strings.ReplaceAll(string(src), "\r\n", "\n") // git for Windows checks it out with CRLF
	if !strings.Contains(mainGo, "if model.inline.on {\n\t\topenInline(&model, os.Stdout, isatty.IsTerminal(os.Stdout.Fd()))") {
		t.Error("main.go does not open an inline chat on a cleared terminal")
	}
}

// THE FIRST SCREEN IS THE LOTUS AND THE PROMPT (owner, 2026-11-21, showing
// Cline's: "add the lotus in the first then the prompt section should change
// like that"). In the middle of the window, in place of the splash, with the
// notes the chat opened with; nothing is printed while it is up; the first
// thing said ends it, the notes are printed first, and the prompt goes back to
// the width of the window.
//
// Neuter checks: welcoming returning false fails the first block; dropping the
// welcoming() test in takePrintable prints the note at once.
func TestInlineTheFirstScreenIsTheLotusAndThePrompt(t *testing.T) {
	defer func(was []string) { startNotes = was }(startNotes)
	startNotes = []string{"Note: another install is answering in this folder."}
	m := newChatModel("test-client", "/workspace", "/workspace", nil)
	m.inline.on = true
	openInline(&m, &strings.Builder{}, false)
	if m.state != stateIdle || !m.input.Focused() {
		t.Fatalf("an inline chat opened on the splash (state %v), not on the prompt", m.state)
	}
	m, out := step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if out != "" || !m.welcoming() {
		t.Fatalf("the first screen is not up (printed %q)", out)
	}

	lines := strings.Split(plain(m.View()), "\n")
	if len(lines) != m.height {
		t.Errorf("the first screen is %d rows, want the window's %d", len(lines), m.height)
	}
	find := func(s string) int {
		for i, l := range lines {
			if strings.Contains(l, s) {
				return i
			}
		}
		return -1
	}
	lotus := find(strings.TrimSpace(strings.Split(lotusLogo, "\n")[0]))
	name, title, notes, prompt := find(brandName), find(welcomeTitle), find("another install"), find(promptMark+" ")
	if !(lotus >= 0 && lotus < name && name < title && title < notes && notes < prompt) {
		t.Errorf("want the lotus, the name, the words, the note, the prompt; got rows %d %d %d %d %d:\n%s",
			lotus, name, title, notes, prompt, strings.Join(lines, "\n"))
	}
	w := m.welcomeWidth()
	if rule := strings.Repeat(promptRule, w); prompt < 1 || strings.TrimSpace(lines[prompt-1]) != rule {
		t.Errorf("the prompt's rules are not %d columns wide:\n%s", w, strings.Join(lines, "\n"))
	}
	if left := strings.Index(lines[prompt], promptMark); left != (m.width-w)/2 {
		t.Errorf("the prompt starts at column %d, not in the middle (%d)", left, (m.width-w)/2)
	}
	if m.input.Width != inputWidthFor(w, m.input.Prompt) {
		t.Errorf("the input is %d wide, want the first screen's prompt's %d", m.input.Width, inputWidthFor(w, m.input.Prompt))
	}

	// Typing is typing, and still prints nothing.
	m = typeText(m, "/help")
	if !m.welcoming() || m.inline.inFlight {
		t.Fatal("typing ended the first screen or printed something")
	}
	m, out = enterPrints(t, m)
	m, rest := settlePrints(t, m)
	out = plain(out + "\n" + rest)
	if m.welcoming() || m.inline.welcome {
		t.Fatal("the first screen outlived the first thing said")
	}
	if n, h := strings.Index(out, "another install"), strings.Index(out, "/account"); n < 0 || h < 0 || n > h {
		t.Errorf("want the note printed, then /help; got:\n%s", out)
	}
	if v := plain(m.View()); strings.Contains(v, welcomeTitle) {
		t.Errorf("the first screen is still drawn:\n%s", v)
	}
	if m.input.Width != inputWidthFor(m.width, m.input.Prompt) {
		t.Errorf("the input is %d wide after the first screen, want the window's %d", m.input.Width, inputWidthFor(m.width, m.input.Prompt))
	}
}

// A chat carried on from before is printed, not welcomed; and a small window
// keeps the first screen's words and prompt without the lotus.
func TestInlineTheFirstScreenOnlyWhenNothingWasSaid(t *testing.T) {
	m := newChatModel("test-client", "/workspace", "/workspace", nil)
	m.inline.on = true
	m.appendTurn(turn{role: roleUser, text: "from before"})
	openInline(&m, &strings.Builder{}, false)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.welcoming() {
		t.Error("a carried-on chat opened on the first screen")
	}

	small := newChatModel("test-client", "/workspace", "/workspace", nil)
	small.inline.on = true
	openInline(&small, &strings.Builder{}, false)
	small, _ = step(t, small, tea.WindowSizeMsg{Width: 80, Height: 14})
	v := plain(small.View())
	if strings.Contains(v, strings.TrimSpace(strings.Split(lotusLogo, "\n")[0])) {
		t.Errorf("the lotus was drawn in a window too small for it:\n%s", v)
	}
	if !strings.Contains(v, welcomeTitle) || !strings.Contains(v, promptMark+" ") || len(strings.Split(v, "\n")) > small.height {
		t.Errorf("the small first screen lost its words or prompt, or is too tall:\n%s", v)
	}
}

// THE PROMPT IS AT THE BOTTOM OF THE WINDOW (owner, 2026-11-21: "the prompt
// bar is now sticking in the top"). While the printed chat is short, the
// program's lines fill the rest of the window, so the prompt and the lines under
// it are its last rows; once the chat is longer than the window, they add no
// blank rows at all.
//
// Neuter check: no pad in composeScreen fails the first assertion.
func TestInlineThePromptIsAtTheBottomOfTheWindow(t *testing.T) {
	m := inlineModel(t)
	m.appendTurn(turn{role: roleSystem, text: "a short note"})
	m, out := step(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m, rest := settlePrints(t, m)
	if !strings.Contains(plain(out), "a short note") || rest != "" {
		t.Fatalf("the note was not printed once: %q then %q", out, rest)
	}
	if want := strings.Count(out, "\n") + 1; m.inline.rows != want {
		t.Errorf("the print took %d rows of the terminal, but %d were counted", want, m.inline.rows)
	}
	view := strings.Split(plain(m.View()), "\n")
	if got := m.inline.rows + len(view); got != m.height {
		t.Errorf("printed rows %d + the program's %d = %d, want the window's %d:\n%s", m.inline.rows, len(view), got, m.height, strings.Join(view, "\n"))
	}
	if rows := promptAndBelow(m); rows[0] != strings.Repeat(promptRule, m.width) || !strings.HasPrefix(rows[1], promptMark+" ") {
		t.Errorf("the prompt is not at the bottom:\n%s", strings.Join(rows, "\n"))
	}

	// A chat longer than the window: no blank rows, the terminal scrolls.
	m.inline.rows = m.height
	natural := inlineSpacerRows + m.headerLineCount() + m.promptRows() + 1 + m.statusRows()
	if n := len(strings.Split(m.View(), "\n")); n != natural {
		t.Errorf("with a long chat the program's lines are %d rows, want %d", n, natural)
	}
}

// rowsTaken counts what a print takes, a line longer than the terminal more
// than once.
func TestInlineRowsTaken(t *testing.T) {
	for _, c := range []struct {
		text  string
		width int
		want  int
	}{
		{"one", 10, 1}, {"one\ntwo", 10, 2}, {"", 10, 1}, {strings.Repeat("x", 25), 10, 3},
		{strings.Repeat("x", 10), 10, 1}, {"\x1b[1mbold\x1b[0m", 4, 1}, {"any", 0, 1},
	} {
		if got := rowsTaken(c.text, c.width); got != c.want {
			t.Errorf("rowsTaken(%q, %d) = %d, want %d", c.text, c.width, got, c.want)
		}
	}
}

// THE FIRST SCREEN'S BOTTOM RIGHT CORNER is the chat's: the project, and under it
// who is running the client at the end of the keys line -- the column is made
// wide enough for that, and no wider than the window.
//
// Neuter check: welcomeWidth ignoring the name drops it from the keys line.
func TestInlineTheFirstScreenKeepsTheBottomRightCorner(t *testing.T) {
	const who = "ravi-kiran@SCARX"
	m := newChatModel("test-client", "/p/Neww", "/p/Neww", nil)
	m.inline.on, m.whoami = true, who
	openInline(&m, &strings.Builder{}, false)
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	lines := strings.Split(plain(m.View()), "\n")
	keys, auto := "", ""
	for i, l := range lines {
		if strings.Contains(l, helpText) {
			keys, auto = strings.TrimRight(l, " "), strings.TrimRight(lines[i-1], " ")
		}
	}
	if !strings.HasSuffix(keys, "  "+who) || !strings.HasSuffix(auto, "  Neww") {
		t.Errorf("the first screen's corner:\n%s\n%s", auto, keys)
	}
	if w := m.welcomeWidth(); w != len([]rune(helpText))+whoamiGap+len(who)+rightEdgeGap {
		t.Errorf("the column is %d wide", w)
	}
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 60, Height: 40})
	if m.welcomeWidth() != 60 {
		t.Errorf("in a 60-column window the column is %d wide", m.welcomeWidth())
	}
}
