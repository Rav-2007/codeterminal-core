package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// copyTestModel is a chat taller than its window, holding a paragraph far
// wider than it: the two things the chat screen could not hand to a drag.
func copyTestModel(t *testing.T) (chatModel, string) {
	t.Helper()
	m := newTestModel() // 80x24
	para := strings.TrimSpace(strings.Repeat("Tara corrects grammar and pairs speaking partners. ", 8))
	var answer strings.Builder
	answer.WriteString(para + "\n\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&answer, "- point %02d of the answer\n", i)
	}
	m.appendTurn(turn{role: roleUser, text: "Did you understand what the code is for?"})
	m.appendTurn(turn{role: roleAssistant, text: answer.String()})
	m.resizeViewport()
	m.refreshViewport()
	if len(para) <= 80 {
		t.Fatalf("the paragraph (%d) must be wider than the window", len(para))
	}
	return m, para
}

// seqMsgs runs a command and every command a tea.Sequence or tea.Batch holds,
// in order, and returns the messages they produce. tea.Sequence's message type
// is not exported, so it is read as the slice of commands it is.
func seqMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, seqMsgs(t, c)...)
		}
		return out
	}
	if v := reflect.ValueOf(msg); v.IsValid() && v.Kind() == reflect.Slice && typeName(msg) == "tea.sequenceMsg" {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			c, _ := v.Index(i).Interface().(tea.Cmd)
			out = append(out, seqMsgs(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// printed is what a tea.Println message prints ("" for any other message).
func printed(msg tea.Msg) string {
	if typeName(msg) != "tea.printLineMessage" {
		return ""
	}
	return reflect.ValueOf(msg).FieldByName("messageBody").String()
}

func msgNames(msgs []tea.Msg) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, typeName(m))
	}
	return out
}

func runCopy(t *testing.T, m chatModel) (chatModel, []tea.Msg) {
	t.Helper()
	m = typeText(m, "/copy")
	m, cmd := pressEnter(m)
	return m, seqMsgs(t, cmd)
}

// THE WHOLE CHAT REACHES THE TERMINAL'S NORMAL SCREEN, AND IN ONE PIECE.
// The owner could select only the rows on screen (2026-11-21): the alternate
// screen has no scrollback. /copy must leave it BEFORE it prints -- Bubble Tea
// drops a print made on the alternate screen -- and must print every turn, not
// the window's worth, with no line cut at the window's edge.
func TestCopyPrintsTheWholeChatOnTheNormalScreenUncut(t *testing.T) {
	m, para := copyTestModel(t)
	m, msgs := runCopy(t, m)
	if m.copyView != copyViewShowing {
		t.Fatalf("/copy left the phase at %v", m.copyView)
	}
	exit, print := -1, -1
	var body string
	for i, msg := range msgs {
		switch typeName(msg) {
		case "tea.exitAltScreenMsg":
			exit = i
		case "tea.printLineMessage":
			print, body = i, ansi.Strip(printed(msg))
		}
	}
	if exit < 0 || print < 0 || exit > print {
		t.Fatalf("want the alternate screen left, then the chat printed; got %v", msgNames(msgs))
	}
	for _, want := range []string{promptMark + " Did you understand", "point 01 of the answer", "point 60 of the answer"} {
		if !strings.Contains(body, want) {
			t.Errorf("the printed chat is missing %q: a selection still cannot reach it", want)
		}
	}
	if !containsLine(body, para) {
		t.Errorf("the %d-column paragraph was cut into window-wide lines; a copy of it would come "+
			"out broken where the window ended:\n%s", len(para), body)
	}
}

// NO LINE IS PADDED. Lip Gloss pads every line of a multi-line block to the
// width of its longest (a command's reply is drawn as one such block), and on a
// screen with no width to cut it at, that padding wrapped into rows of blanks
// between every two lines of /help -- found in the first live run, 2026-11-21
// -- and would be copied as trailing spaces.
func TestCopyPrintsNoPaddedLines(t *testing.T) {
	m, _ := copyTestModel(t)
	m.appendTurn(turn{role: roleAssistant, local: true, verbatim: true,
		text: "short\na line that is a good deal longer than the one above it\nmid-length line"})
	_, msgs := runCopy(t, m)
	var body string
	for _, msg := range msgs {
		if p := printed(msg); p != "" {
			body = ansi.Strip(p)
		}
	}
	if body == "" {
		t.Fatalf("nothing printed: %v", msgNames(msgs))
	}
	for i, line := range strings.Split(body, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("printed line %d ends in %d spaces of padding: %q", i+1,
				len(line)-len(strings.TrimRight(line, " ")), line)
		}
	}
}

// containsLine reports whether some line of s holds want whole.
func containsLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// While /copy shows, this program draws ONE line: the keys. Anything taller
// would be drawn over the printed chat each frame.
func TestCopyScreenDrawsOnlyTheKeys(t *testing.T) {
	m, _ := copyTestModel(t)
	m, _ = runCopy(t, m)
	v := ansi.Strip(m.View())
	if strings.Contains(v, "\n") {
		t.Errorf("the /copy screen draws %d lines, want 1: %q", strings.Count(v, "\n")+1, v)
	}
	for _, want := range []string{"esc", copyKey()} {
		if !strings.Contains(v, want) {
			t.Errorf("the /copy screen does not say %q: %q", want, v)
		}
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		keys := strings.Replace(copyViewKeys(), copyKey(), copyKeyFor(goos), 1)
		if w := ansi.StringWidth(keys); w > 80 {
			t.Errorf("on %s the key line is %d columns: an 80-column terminal cuts its end off", goos, w)
		}
	}
}

// esc comes back in three steps: the key line is blanked (so the scrollback
// does not keep it), the alternate screen is entered, and only then is the
// chat drawn -- drawn earlier, it would be painted into the normal screen.
func TestEscFromCopyComesBackToTheChat(t *testing.T) {
	m, _ := copyTestModel(t)
	chatBefore := ansi.Strip(m.View())
	m, _ = runCopy(t, m)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(chatModel)
	if m.copyView != copyViewLeaving || cmd == nil {
		t.Fatalf("esc: phase %v, cmd %v; want leaving, with the wait", m.copyView, cmd != nil)
	}
	if v := m.View(); v != "" {
		t.Errorf("leaving, the screen still draws %q: the key line would stay in the scrollback", v)
	}
	if msgs := seqMsgs(t, cmd); len(msgs) != 1 || msgs[0] != (copyViewMsg{}) {
		t.Fatalf("the wait produced %v", msgNames(msgs))
	}

	next, cmd = m.Update(copyViewMsg{})
	m = next.(chatModel)
	msgs := seqMsgs(t, cmd)
	if len(msgs) != 2 || typeName(msgs[0]) != "tea.enterAltScreenMsg" || msgs[1] != (copyViewMsg{back: true}) {
		t.Fatalf("want the alternate screen entered, then the chat back; got %v", msgNames(msgs))
	}
	if v := m.View(); v != "" {
		t.Errorf("the chat was drawn before the alternate screen was up: %q", v)
	}

	next, _ = m.Update(copyViewMsg{back: true})
	m = next.(chatModel)
	if m.copyView != copyViewOff {
		t.Fatalf("back: phase %v", m.copyView)
	}
	if got := ansi.Strip(m.View()); got != chatBefore {
		t.Errorf("the chat did not come back as it was:\nbefore:\n%s\nafter:\n%s", chatBefore, got)
	}
}

// ctrl+c on the /copy screen brings the chat back and never quits: the quit
// question would be asked on a screen that is not showing, and a ctrl+c that
// arrives from there was most likely meant as "copy". Other keys do nothing,
// and none of them reaches the prompt.
func TestCopyScreenNeverQuitsAndKeepsKeysOutOfThePrompt(t *testing.T) {
	m, _ := copyTestModel(t)
	m, _ = runCopy(t, m)
	m = typeText(m, "hello")
	if m.input.Value() != "" {
		t.Errorf("keys typed on the /copy screen went into the prompt: %q", m.input.Value())
	}
	if m.copyView != copyViewShowing {
		t.Errorf("a letter left the /copy screen (phase %v)", m.copyView)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(chatModel)
	for _, msg := range seqMsgs(t, cmd) {
		if _, quit := msg.(tea.QuitMsg); quit {
			t.Fatal("ctrl+c on the /copy screen quit the program")
		}
	}
	if m.quitArmed || m.quitChatArmed {
		t.Error("ctrl+c on the /copy screen armed a quit the user cannot see")
	}
	if m.copyView != copyViewLeaving {
		t.Errorf("ctrl+c did not bring the chat back (phase %v)", m.copyView)
	}
}

// With /mouse on, the terminal can neither select with a plain drag nor scroll
// its scrollback with the wheel: /copy gives the mouse back while it shows, and
// takes it again on the way out.
func TestCopyGivesTheMouseBackWhileItShows(t *testing.T) {
	m, _ := copyTestModel(t)
	m = typeText(m, "/mouse")
	m, _ = pressEnter(m)
	if !m.mouseCaptured {
		t.Fatal("setup: /mouse did not turn capture on")
	}
	m, msgs := runCopy(t, m)
	if len(msgs) == 0 || !strings.Contains(strings.ToLower(typeName(msgs[0])), "disablemouse") {
		t.Fatalf("/copy with capture on did not release the mouse first: %v", msgNames(msgs))
	}
	m.copyView = copyViewLeaving
	_, cmd := m.Update(copyViewMsg{back: true})
	if got := msgNames(seqMsgs(t, cmd)); len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), "enablemouse") {
		t.Errorf("back from /copy, capture was not restored: %v", got)
	}
}

// Nothing to copy: say so, on the chat screen, and do not leave it.
func TestCopyOnAnEmptyChatStaysPut(t *testing.T) {
	m := newTestModel()
	m, msgs := runCopy(t, m)
	if m.copyView != copyViewOff || len(msgs) != 0 {
		t.Fatalf("/copy on an empty chat switched screens: phase %v, %v", m.copyView, msgNames(msgs))
	}
	if !strings.Contains(m.turns[len(m.turns)-1].text, "Nothing to copy") {
		t.Errorf("/copy on an empty chat said: %q", m.turns[len(m.turns)-1].text)
	}
}

// A step that arrives when /copy is not on its way back changes nothing.
func TestStrayCopyStepIsIgnored(t *testing.T) {
	m, _ := copyTestModel(t)
	for _, msg := range []copyViewMsg{{}, {back: true}} {
		next, cmd := m.Update(msg)
		if next.(chatModel).copyView != copyViewOff || cmd != nil {
			t.Errorf("%+v outside /copy did something", msg)
		}
	}
}

// /copy is found where people look: the popup's catalog and /help.
func TestCopyIsListed(t *testing.T) {
	if lookupSlash("copy") == nil {
		t.Error("/copy is not in the slash catalog, so the popup never offers it")
	}
	if !strings.Contains(formatSlashHelp(), "/copy") {
		t.Error("/help does not mention /copy")
	}
}
