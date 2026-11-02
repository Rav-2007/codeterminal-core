package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// fakeMCP answers the panel the way the real daemon does: the list; for an
// add, the warning and the server in parts until it is acknowledged, then
// "added" -- refusing a name already added unless asked to replace it. A
// server named "broken" fails its add; an entry with "envFile" is refused, as
// the daemon refuses a field it cannot use.
type fakeMCP struct {
	*recordingDaemon
	mu      sync.Mutex
	servers []protocol.MCPServerInfo
}

func newFakeMCP(t *testing.T, existing []protocol.MCPServerInfo) *fakeMCP {
	t.Helper()
	t.Cleanup(func() { addKeys.server, addKeys.pending, addKeys.stored, addKeys.checking = "", nil, false, "" })
	f := &fakeMCP{servers: append([]protocol.MCPServerInfo(nil), existing...)}
	f.recordingDaemon = startRequestRecorder(t, []string{protocol.FeatureMCPAdmin}, f.answer)
	return f
}

type fakeEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers"`
}

func (f *fakeMCP) answer(raw json.RawMessage) any {
	var req protocol.MCPRequest
	_ = json.Unmarshal(raw, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Action {
	case protocol.MCPActionServers:
		return protocol.MCPResponse{Ok: true, AgentMode: true, Servers: append([]protocol.MCPServerInfo(nil), f.servers...)}
	case protocol.MCPActionSetEnv:
		for i := range f.servers {
			s := &f.servers[i]
			if s.Name == req.Name {
				s.EnvMissing = without(s.EnvMissing, req.EnvName)
				s.HeadersMissing = without(s.HeadersMissing, req.HeaderName)
			}
		}
		return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: "set for " + req.Name}
	case protocol.MCPActionCheck:
		for i := range f.servers {
			if s := &f.servers[i]; s.Name == req.Name && len(s.EnvMissing)+len(s.HeadersMissing) == 0 {
				s.State, s.ToolsListed, s.ToolCount = protocol.MCPStateRunning, true, 3
			}
		}
		return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: req.Name + ": checked"}
	case protocol.MCPActionAddJSON:
	default:
		return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: req.Action + " " + req.Name + " done."}
	}
	if strings.Contains(string(req.JSON), "envFile") {
		return protocol.MCPResponse{Error: `the entry's "envFile" field is not one Mochiii can use; remove it to add the server; nothing was added`}
	}
	var e fakeEntry
	_ = json.Unmarshal(req.JSON, &e)
	info := protocol.MCPServerInfo{Name: req.Name, Source: protocol.MCPSourceUser, Program: e.Command, Args: e.Args}
	if e.URL != "" {
		info.URL, info.Transport = e.URL, "http"
		info.Command = "http " + e.URL
	} else {
		info.Command = strings.TrimSpace(e.Command + " " + strings.Join(e.Args, " "))
	}
	for _, k := range sortedKeys(e.Env) {
		info.Env = append(info.Env, k)
		if v := e.Env[k]; v == "" || strings.Contains(v, "YOUR") {
			info.EnvMissing = append(info.EnvMissing, k)
		} else {
			info.EnvStored = append(info.EnvStored, k)
		}
	}
	for _, h := range sortedKeys(e.Headers) {
		info.Headers = append(info.Headers, h)
		if strings.Contains(e.Headers[h], "YOUR") {
			info.HeadersMissing = append(info.HeadersMissing, h)
		}
	}
	exists := -1
	for i, s := range f.servers {
		if s.Name == req.Name {
			exists = i
		}
	}
	switch {
	case exists >= 0 && !req.Replace:
		return protocol.MCPResponse{Error: "a server named " + req.Name + " is already added; remove it first to change it"}
	case !req.AcknowledgedUnconfined:
		lead := map[bool]string{true: "Changing", false: "Adding"}[req.Replace]
		detail := lead + " \"" + req.Name + "\" will make this daemon run:\n\n    " + info.Command + "\n\nas an ordinary program with YOUR full privileges."
		if e.URL != "" {
			detail = lead + " \"" + req.Name + "\" will connect this daemon to:\n\n    " + e.URL + "\n\nEvery tool call you approve SENDS its arguments there."
		}
		return protocol.MCPResponse{NeedsAcknowledgement: true, AgentMode: true, Detail: detail, Preview: &info}
	case req.Name == "broken":
		return protocol.MCPResponse{Error: "saving the server: the disk is full"}
	}
	switch {
	case strings.Contains(e.URL, "signin"):
		info.State = protocol.MCPStateNeedsAuth
	case len(info.EnvMissing)+len(info.HeadersMissing) == 0:
		info.State, info.ToolsListed, info.ToolCount = protocol.MCPStateRunning, true, 2
	}
	if exists >= 0 {
		f.servers[exists] = info
	} else {
		f.servers = append(f.servers, info)
	}
	return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: "Added " + req.Name + ".",
		Servers: append([]protocol.MCPServerInfo(nil), f.servers...)}
}

func without(list []string, s string) []string {
	var out []string
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// requests are what the daemon was sent, of one action.
func (f *fakeMCP) requests(t *testing.T, action string) []protocol.MCPRequest {
	t.Helper()
	var out []protocol.MCPRequest
	for _, r := range mcpRequests(t, f.recordingDaemon) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// added are the names whose acknowledged add was sent.
func (f *fakeMCP) added(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, r := range mcpRequests(t, f.recordingDaemon) {
		if (r.Action == protocol.MCPActionAddJSON || r.Action == protocol.MCPActionAdd) && r.AcknowledgedUnconfined {
			names = append(names, r.Name)
		}
	}
	return names
}

// settle runs every command cmd holds, batches included, feeds the panel's
// messages back in, and does the same for what those return -- until nothing
// more comes. A batch's answers arrive in no fixed order, as in the program.
func settle(m chatModel, cmd tea.Cmd) chatModel {
	for round := 0; cmd != nil && round < 40; round++ {
		out := make(chan tea.Msg, 64)
		leaves := 0
		// A batch comes back whole and is run in the loop below, so the
		// goroutines never touch leaves.
		run := func(c tea.Cmd) {
			if c == nil {
				return
			}
			leaves++
			go func() { out <- c() }()
		}
		run(cmd)
		var cmds []tea.Cmd
		deadline := time.After(5 * time.Second)
		for leaves > 0 {
			select {
			case msg := <-out:
				leaves--
				if batch, ok := msg.(tea.BatchMsg); ok {
					for _, sub := range batch {
						run(sub)
					}
					continue
				}
				switch msg.(type) {
				case mcpResultMsg, mcpPasteMsg, mcpHotkeyMsg:
					updated, next := m.Update(msg)
					m = updated.(chatModel)
					cmds = append(cmds, next)
				}
			case <-deadline:
				leaves = 0
			}
		}
		cmd = tea.Batch(cmds...)
	}
	return m
}

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// pressPanel presses each key as a person does: one, then whatever it set off
// runs to its end (a held key's window included) before the next.
func pressPanel(m chatModel, keys ...string) chatModel {
	for _, k := range keys {
		updated, cmd := m.Update(keyMsg(k))
		m = settle(updated.(chatModel), cmd)
	}
	return m
}

// typeLine types s and presses enter, in a box the panel is asking through.
func typeLine(m chatModel, s string) chatModel {
	return pressPanel(typeText(m, s), "enter")
}

// pasteBracketed is a paste as a terminal with bracketed paste sends it: one
// event, newlines and all.
func pasteBracketed(m chatModel, s string) chatModel {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true})
	return settle(updated.(chatModel), cmd)
}

// pasteAsTyping is the same paste from a terminal WITHOUT bracketed paste:
// bubbletea cuts it at every space and newline into separate key events, all
// arriving at once. Nothing runs between them, as nothing does in the program.
func pasteAsTyping(m chatModel, s string) chatModel {
	var cmds []tea.Cmd
	feed := func(msg tea.KeyMsg) {
		updated, cmd := m.Update(msg)
		m = updated.(chatModel)
		cmds = append(cmds, cmd)
	}
	var word []rune
	flush := func() {
		if len(word) > 0 {
			feed(tea.KeyMsg{Type: tea.KeyRunes, Runes: word})
			word = nil
		}
	}
	for _, r := range s {
		switch r {
		case ' ':
			flush()
			feed(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		case '\n':
			flush()
			feed(tea.KeyMsg{Type: tea.KeyEnter})
		default:
			word = append(word, r)
		}
	}
	flush()
	return settle(m, tea.Batch(cmds...))
}

func openPanel(t *testing.T, servers []protocol.MCPServerInfo) (chatModel, *fakeMCP) {
	t.Helper()
	f := newFakeMCP(t, servers)
	m := newTestModel()
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = sized.(chatModel)
	m = typeText(m, "/mcp")
	m, cmd := pressEnter(m)
	m = settle(m, cmd)
	if m.mcpPanel == nil {
		t.Fatal("/mcp opened no panel")
	}
	return m, f
}

var panelServers = []protocol.MCPServerInfo{
	{Name: "github", Source: protocol.MCPSourceUser, Program: "npx", State: protocol.MCPStateRunning, ToolsListed: true,
		ToolCount: 41, StatusLine: "✔ Connected (41 tools)"},
	{Name: "notes", Source: protocol.MCPSourceUser, Program: "npx", Disabled: true, StatusLine: "⊘ Disabled"},
	{Name: "postgres", Source: protocol.MCPSourceUser, Program: "npx", Env: []string{"DATABASE_URL"},
		EnvMissing: []string{"DATABASE_URL"}, StatusLine: "! Needs a key: DATABASE_URL"},
	{Name: "figma", Source: protocol.MCPSourceUser, URL: "https://mcp.figma.com/mcp", Transport: "http",
		State: protocol.MCPStateNeedsAuth, StatusLine: "! Needs authentication"},
}

// The three-server paste of the brief (2026-11-02): one program, one remote
// server, and one whose name is already added.
const threeServers = `{
  "mcpServers": {
    "files": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/notes"]},
    "docs":  {"url": "https://docs.example/mcp", "type": "http", "headers": {"Authorization": "Bearer YOUR_TOKEN"}},
    "github": {"command": "docker", "args": ["run", "-i", "ghcr.io/github/github-mcp-server"], "env": {"GITHUB_TOKEN": "ghp_fake_9c1e7a"}}
  }
}`

// The owner's reference (2026-11-01) was Cline's /mcp: a box over the chat
// listing the servers. Here what needs the user is listed first, counted in
// the title, and each row says its state in the daemon's own words.
func TestTheMCPPanelListsWhatNeedsTheUserFirst(t *testing.T) {
	m, _ := openPanel(t, panelServers)
	screen := plainScreen(m)
	for _, want := range []string{"MCP Servers", "4 servers · 2 need you", "! Needs authentication",
		"! Needs a key: DATABASE_URL", "✔ Connected (41 tools)", "+ Add a server…", "Saved in ~/.mochiii/mcp-servers.json",
		"paste JSON to add", "esc close"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the panel does not show %q:\n%s", want, screen)
		}
	}
	var order []string
	for _, row := range m.mcpPanel.picker.rows {
		order = append(order, row.ID)
	}
	if want := []string{"figma", "postgres", "github", "notes", mcpAddRowID}; !reflect.DeepEqual(order, want) {
		t.Errorf("rows %q, want %q", order, want)
	}
	if m = pressPanel(m, "esc"); m.mcpPanel != nil {
		t.Error("esc did not close the panel")
	}
}

func TestTheEmptyPanelSaysToPasteHere(t *testing.T) {
	m, _ := openPanel(t, nil)
	if screen := plainScreen(m); !strings.Contains(screen, "No servers yet.") || !strings.Contains(screen, "Paste a server's JSON") {
		t.Errorf("the empty panel does not say how to add one:\n%s", screen)
	}
}

// PASTED STRAIGHT IN, no key first: from an empty list and from a full one,
// the paste is read and its servers are on the review page.
//
// Neuter check: drop the msg.Paste branch of handleMCPPanelKey -- the paste
// is held as a key, then taken as typing into the box, and no review appears.
func TestAPasteGoesStraightToTheReviewFromAnyList(t *testing.T) {
	for _, existing := range [][]protocol.MCPServerInfo{nil, panelServers} {
		m, f := openPanel(t, existing)
		m = pasteBracketed(m, `{"mcpServers":{"files":{"command":"npx","args":["-y","fs"]}}}`)
		if m.mcpPanel.paste == nil || m.mcpPanel.paste.stage != pasteReview {
			t.Fatalf("with %d servers listed, the paste did not reach the review: %+v", len(existing), m.mcpPanel.paste)
		}
		if screen := plainScreen(m); !strings.Contains(screen, "1 server to add") || !strings.Contains(screen, "runs npx -y fs") {
			t.Errorf("the review does not show the server:\n%s", screen)
		}
		if len(f.added(t)) != 0 {
			t.Fatal("a paste added something before any name was typed")
		}
	}
}

// A PASTE NEVER PRESSES A HOTKEY: text full of x, space, e, s, c, a, enter
// and esc, from a full list, pressing nothing -- not remove, pause, change,
// sign-in, check, add, nor leaving the panel. Bracketed, it is one event;
// from a terminal without bracketed paste, its pieces arrive at once and the
// first is held (holdKey) until the second shows it is text.
//
// Neuter checks: (1) drop the msg.Paste branch of handleMCPPanelKey -- the
// bracketed case fails, its paste never reviewed. (2) make holdKey act at
// once (return m.mcpPanelAction(msg)) -- the typed case fails: the leading
// space pauses a server and "x" arms a removal.
func TestAPasteFullOfHotkeysPressesNone(t *testing.T) {
	text := " \nx e s c a\n" + `{"mcpServers": {"x-e-s": {"command": "npx", "args": ["x", "e", "s", "c", "a", "esc", "enter"]}}}` + "\n"
	for _, how := range []struct {
		name  string
		paste func(chatModel, string) chatModel
	}{{"bracketed", pasteBracketed}, {"as typing", pasteAsTyping}} {
		t.Run(how.name, func(t *testing.T) {
			m, f := openPanel(t, panelServers)
			m = how.paste(m, text)
			for _, action := range []string{protocol.MCPActionRemove, protocol.MCPActionDisable, protocol.MCPActionEnable,
				protocol.MCPActionCheck, protocol.MCPActionAuthStart, protocol.MCPActionUpdate} {
				if got := f.requests(t, action); len(got) != 0 {
					t.Errorf("the paste pressed %s: %+v", action, got)
				}
			}
			if m.mcpPanel == nil {
				t.Fatal("the paste closed the panel")
			}
			if m.mcpPanel.removeArmed != "" {
				t.Errorf("the paste armed a removal of %s", m.mcpPanel.removeArmed)
			}
			// Either way it is one paste, read whole: lines before its JSON
			// are not servers, so it is refused -- and kept in the box.
			if m.mcpPanel.paste != nil || !strings.Contains(m.mcpPanel.notice, "there is text before the JSON") {
				t.Errorf("a paste with text before its JSON was not refused whole: paste %+v notice %q",
					m.mcpPanel.paste, m.mcpPanel.notice)
			}
			if !strings.Contains(m.input.Value(), `"x-e-s"`) {
				t.Errorf("the paste is not kept in the box: %q", m.input.Value())
			}
		})
	}
}

// The same, with clean JSON from a terminal without bracketed paste: the
// enters inside the object are spaces in the box, and the last one -- once
// the object is closed -- sends it to the review.
func TestAPasteFromATerminalWithoutBracketedPasteIsReviewed(t *testing.T) {
	m, f := openPanel(t, panelServers)
	m = pasteAsTyping(m, "{\n  \"mcpServers\": {\n    \"files\": {\"command\": \"npx\", \"args\": [\"x\", \"s\"]}\n  }\n}\n")
	if m.mcpPanel.paste == nil || m.mcpPanel.paste.stage != pasteReview {
		t.Fatalf("not reviewed: view %v notice %q box %q", m.mcpPanel.view, m.mcpPanel.notice, m.input.Value())
	}
	if len(f.requests(t, protocol.MCPActionDisable))+len(f.requests(t, protocol.MCPActionRemove)) != 0 {
		t.Fatal("a hotkey fired")
	}
}

// A key typed by a person still acts, once its quiet window passes.
func TestATypedHotkeyStillActs(t *testing.T) {
	m, f := openPanel(t, panelServers)
	m = pressPanel(m, "down", "down", "space") // github
	if got := f.requests(t, protocol.MCPActionDisable); len(got) != 1 || got[0].Name != "github" {
		t.Fatalf("space did not pause github: %+v", got)
	}
	m = pressPanel(m, "enter", "x")
	if !strings.Contains(m.mcpPanel.notice, "Press x again") || len(f.requests(t, protocol.MCPActionRemove)) != 0 {
		t.Fatalf("one x removed it, or did not ask: %q", m.mcpPanel.notice)
	}
	pressPanel(m, "x")
	if len(f.requests(t, protocol.MCPActionRemove)) != 1 {
		t.Error("the second x did not remove it")
	}
}

// THE THREE-SERVER PASTE: one review page with all of them, each with what it
// will do -- the exact command a program runs, the exact URL a remote server
// is sent to -- and the conflict said; then one name at a time; a skip; and
// a conflict replaced only by its name typed.
func TestThreeServersAreReviewedThenAgreedToOneAtATime(t *testing.T) {
	m, f := openPanel(t, panelServers) // github is already added
	m = pasteBracketed(m, threeServers)

	screen := plainScreen(m)
	for _, want := range []string{"3 servers to add", "Nothing is added until you type each one's name",
		"1 files", "runs npx -y @modelcontextprotocol/server-filesystem /tmp/notes (on this computer, as you)",
		"2 docs", "sends what its tools are given to https://docs.example/mcp",
		"headers Authorization missing (asked for after the add)",
		"3 github — you already have one of this name: type it to replace yours, or skip",
		"runs docker run -i ghcr.io/github/github-mcp-server", "GITHUB_TOKEN given (kept in ~/.mochiii/mcp-secrets.json)"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the review does not show %q:\n%s", want, screen)
		}
	}
	if previews := f.requests(t, protocol.MCPActionAddJSON); len(previews) != 3 || !previews[2].Replace {
		t.Fatalf("the daemon was asked %+v: want three previews, github's as a replacement", previews)
	}

	m = pressPanel(m, "enter") // into the confirmations
	if screen := plainScreen(m); !strings.Contains(screen, "1 of 3: files") || !strings.Contains(screen, "YOUR full privileges") {
		t.Fatalf("the first confirmation does not show its warning:\n%s", screen)
	}
	m = typeLine(m, "fiels") // a typo: asked again, nothing decided
	if m.mcpPanel.paste.at != 0 || !strings.Contains(m.mcpPanel.notice, "Type files to add it") {
		t.Fatalf("a wrong name moved on: at %d notice %q", m.mcpPanel.paste.at, m.mcpPanel.notice)
	}
	if m.input.Value() != "fiels" {
		t.Errorf("the typo is not kept in the box to fix: %q", m.input.Value())
	}
	m.input.SetValue("")
	m = typeLine(m, "files") // agreed
	m = typeLine(m, "")      // docs: skipped
	if screen := plainScreen(m); !strings.Contains(screen, "3 of 3: github") || !strings.Contains(screen, "REPLACES that one") {
		t.Fatalf("the conflict's confirmation does not say it replaces:\n%s", screen)
	}
	if len(f.added(t)) != 0 {
		t.Fatal("something was added before the last answer")
	}
	m = typeLine(m, "github") // replace

	if got := f.added(t); !reflect.DeepEqual(got, []string{"files", "github"}) {
		t.Fatalf("added %q, want files then github", got)
	}
	for _, r := range f.requests(t, protocol.MCPActionAddJSON) {
		if r.AcknowledgedUnconfined && r.Name == "github" && !r.Replace {
			t.Error("github was added without replace")
		}
	}
	screen = plainScreen(m)
	for _, want := range []string{"Results", "files connected (2 tools)", "docs skipped", "github connected (2 tools)"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the results do not say %q:\n%s", want, screen)
		}
	}
	if turnsContaining(m, "MCP servers from your paste:") != 1 {
		t.Error("the results are not in the transcript once")
	}
}

// One server failing to add does not stop the next; each says how it went.
func TestOneFailureDoesNotStopTheRest(t *testing.T) {
	m, f := openPanel(t, nil)
	m = pasteBracketed(m, `{"mcpServers":{"broken":{"command":"npx"},"after":{"command":"npx","args":["ok"]},`+
		`"remote":{"url":"https://signin.example/mcp"}}}`)
	m = typeLine(pressPanel(m, "enter"), "broken")
	m = typeLine(m, "after")
	m = typeLine(m, "remote")
	if got := f.added(t); !reflect.DeepEqual(got, []string{"broken", "after", "remote"}) {
		t.Fatalf("adds sent %q", got)
	}
	screen := plainScreen(m)
	for _, want := range []string{"broken not added: saving the server: the disk is full", "after connected (2 tools)",
		"remote needs sign-in — open it here and press s"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the results do not say %q:\n%s", want, screen)
		}
	}
}

// Nothing is added without its name typed, and esc -- on the review, or at
// any confirmation -- cancels them all and adds nothing.
//
// Neuter check: in answerPaste, agree on any typed line (drop the
// typed == e.req.Name test) -- "solo" is added after "nope" was typed.
func TestNothingIsAddedWithoutItsNameAndEscCancelsAll(t *testing.T) {
	m, f := openPanel(t, nil)
	m = pasteBracketed(m, `{"mcpServers":{"solo":{"command":"npx"}}}`)
	m = typeLine(pressPanel(m, "enter"), "nope")
	if got := f.added(t); len(got) != 0 {
		t.Fatalf("added %q with the wrong name typed", got)
	}
	if m.mcpPanel.paste == nil || m.mcpPanel.paste.stage != pasteConfirming {
		t.Fatal("a wrong name ended the question")
	}
	m = pressPanel(m, "esc")

	m = pasteBracketed(m, threeServers)
	m = pressPanel(m, "esc")
	if m.mcpPanel.paste != nil || !strings.Contains(m.mcpPanel.notice, "Cancelled: nothing was added") {
		t.Fatalf("esc on the review: paste %+v notice %q", m.mcpPanel.paste, m.mcpPanel.notice)
	}

	m = pasteBracketed(m, threeServers)
	m = typeLine(pressPanel(m, "enter"), "nope")
	m.input.SetValue("")
	m = typeLine(m, "files")
	m = pressPanel(m, "esc") // at docs: cancel all, files included
	if got := f.added(t); len(got) != 0 {
		t.Fatalf("added %q without every answer", got)
	}
	if m.mcpPanel.paste != nil {
		t.Error("esc did not end the paste")
	}
}

// A KEY IN THE PASTE IS NEVER SHOWN: not on any page, in the transcript or in
// the up-arrow recall. The daemon keeps it in the key file (its own test,
// TestTheReviewPreviewNamesKeysAndShowsNoValue); the panel names it, given.
// A placeholder is a key missing, asked for hidden after the add.
func TestAPastedKeyIsNeverShownAndAPlaceholderIsAskedFor(t *testing.T) {
	const secret = "ghp_fake_9c1e7a"
	m, f := openPanel(t, nil)
	m = pasteBracketed(m, `{"mcpServers":{"gh":{"command":"docker","env":{"GITHUB_TOKEN":"`+secret+`"}},`+
		`"pg":{"command":"npx","env":{"DATABASE_URL":"YOUR_DATABASE_URL"}}}}`)
	seen := []string{plainScreen(m)}
	m = pressPanel(m, "enter")
	seen = append(seen, plainScreen(m))
	m = typeLine(m, "gh")
	seen = append(seen, plainScreen(m))
	m = typeLine(m, "pg")
	// pg's placeholder is missing: the hidden prompt asks for it.
	if m.pendingMCPAdd == nil || m.pendingMCPAdd.EnvName != "DATABASE_URL" || m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("the placeholder was not asked for, hidden: %+v", m.pendingMCPAdd)
	}
	seen = append(seen, plainScreen(m))
	m = typeLine(m, "postgres://u:fake-pw@db/x")
	seen = append(seen, plainScreen(m))
	if sets := f.requests(t, protocol.MCPActionSetEnv); len(sets) != 1 || sets[0].Name != "pg" {
		t.Fatalf("set_env %+v", sets)
	}
	if !strings.Contains(plainScreen(m), "pg connected (3 tools)") {
		t.Errorf("the results do not show pg connected with its key:\n%s", plainScreen(m))
	}
	for _, s := range seen {
		if strings.Contains(s, secret) || strings.Contains(s, "fake-pw") {
			t.Fatalf("a key is on screen:\n%s", s)
		}
	}
	for _, tn := range m.turns {
		if strings.Contains(tn.text, secret) || strings.Contains(tn.text, "fake-pw") {
			t.Fatalf("a key is in the transcript: %s", tn.text)
		}
	}
	for _, h := range m.promptHistory {
		if strings.Contains(h, secret) || strings.Contains(h, "mcpServers") {
			t.Fatal("the paste is in the up-arrow recall")
		}
	}
}

// One server with no name is asked a name, checked as the daemon checks it.
func TestAnUnnamedServerIsAskedAName(t *testing.T) {
	m, f := openPanel(t, nil)
	m = pasteBracketed(m, `{"url":"https://docs.example/mcp","type":"http"}`)
	if m.mcpPanel.paste == nil || m.mcpPanel.paste.stage != pasteNaming {
		t.Fatalf("no name was asked: %+v", m.mcpPanel.paste)
	}
	m = typeLine(m, "Docs")
	if !strings.Contains(m.mcpPanel.notice, "lower-case") || m.mcpPanel.paste.stage != pasteNaming {
		t.Fatalf("a bad name was taken: %q", m.mcpPanel.notice)
	}
	m.input.SetValue("")
	m = typeLine(m, "docs")
	if m.mcpPanel.paste == nil || m.mcpPanel.paste.stage != pasteReview || f.requests(t, protocol.MCPActionAddJSON)[0].Name != "docs" {
		t.Fatalf("the named server was not reviewed: %+v", m.mcpPanel.paste)
	}
}

// A paste that is not servers is refused whole, saying what was wrong, and
// kept in the box to fix. A server the daemon refuses refuses the paste.
func TestAPasteThatIsNotServersIsRefusedAndKept(t *testing.T) {
	for _, c := range []struct{ paste, why string }{
		{`{"mcpServers": {"a": {"command": "npx",}}}`, "not valid JSON: it stops making sense at character"},
		{`{"mcpServers": {"a": {"command": "npx"}}`, "ends before it is finished"},
		{`[{"command": "npx"}]`, "JSON list"},
		{`{}`, "the object is empty"},
		{`{"mcpServers": {}}`, `"mcpServers" block names no servers`},
		{`{"mcpServers": {"a": "npx"}}`, `"a" is not a server: a server is an object`},
		{`{"mcpServers": {"a": {"args": ["x"]}}}`, `"a" is not a server: it has no "command"`},
		{`{"mcpServers": {"a": {"command": "x"}}, "servers": {}}`, `both an "mcpServers" and a "servers" block`},
		{`{"mcpServers": {"a": {"command": "x"}, "a": {"command": "y"}}}`, `"a" appears twice`},
		{`{"theme": "dark", "fontSize": 12}`, "no servers found"},
		{`{"mcpServers": {"a": {"command": "x"}}} {}`, "more after the JSON's closing }"},
		{`{"mcpServers": {"a": {"command": "npx", "envFile": ".env"}}}`, `a: the entry's "envFile" field is not one Mochiii can use`},
	} {
		m, f := openPanel(t, nil)
		m = pasteBracketed(m, c.paste)
		if m.mcpPanel.paste != nil || !strings.Contains(m.mcpPanel.notice, c.why) || !strings.Contains(m.mcpPanel.notice, "Nothing was added") {
			t.Errorf("%s: notice %q", c.paste, m.mcpPanel.notice)
		}
		if m.mcpPanel.view != mcpPanelAdd || !strings.Contains(m.input.Value(), strings.Fields(c.paste)[0]) {
			t.Errorf("%s: the paste is not back in the box: view %v box %q", c.paste, m.mcpPanel.view, m.input.Value())
		}
		if len(f.added(t)) != 0 {
			t.Errorf("%s: something was added", c.paste)
		}
	}
}

// Every shape the panel reads, in the order the servers were written.
func TestEveryAcceptedShapeIsRead(t *testing.T) {
	entry := `{"command":"npx","args":["-y","s"]}`
	for _, c := range []struct {
		paste string
		names []string
	}{
		{`{"mcpServers":{"zeta":` + entry + `,"alpha":` + entry + `}}`, []string{"zeta", "alpha"}},        // Claude Desktop, Cursor, Cline
		{`{"servers":{"gh":{"type":"http","url":"https://api.githubcopilot.com/mcp/"}}}`, []string{"gh"}}, // VS Code
		{`{"one":` + entry + `,"two":{"url":"https://x.example/mcp"}}`, []string{"one", "two"}},           // the block alone
		{`{"mcpServers":{"a":` + entry + `},"globalShortcut":"Ctrl+Space"}`, []string{"a"}},               // a whole config file
		{`files ` + entry, []string{"files"}},                                                             // a name, then one entry
		{`{"command":"npx","env":{"K":"v"}}`, []string{""}},                                               // one entry, no name: asked
		{`{"url":"https://x.example/mcp","headers":{"Authorization":"Bearer t"}}`, []string{""}},
	} {
		reqs, _, err := mcpAddRequests(c.paste)
		var names []string
		for _, r := range reqs {
			names = append(names, r.Name)
			if r.Action != protocol.MCPActionAddJSON || len(r.JSON) == 0 {
				t.Errorf("%s: request %+v", c.paste, r)
			}
		}
		if err != nil || !reflect.DeepEqual(names, c.names) {
			t.Errorf("%s: %q, %v; want %q", c.paste, names, err, c.names)
		}
	}
	_, notes, _ := mcpAddRequests(`{"mcpServers":{"a":` + entry + `},"globalShortcut":"Ctrl+Space"}`)
	if len(notes) != 1 || !strings.Contains(notes[0], "globalShortcut") {
		t.Errorf("a config's other settings are not said to be left alone: %v", notes)
	}
}

// Each server's page offers what it needs: a key typed hidden into the box,
// the same prompt as /mcp-server key.
func TestAServersPageAsksForItsMissingKeyHidden(t *testing.T) {
	m, f := openPanel(t, panelServers)
	m = pressPanel(m, "down", "enter") // postgres, under figma
	if m.mcpPanel.view != mcpPanelDetail || m.mcpPanel.open != "postgres" {
		t.Fatalf("enter opened %q (view %v)", m.mcpPanel.open, m.mcpPanel.view)
	}
	screen := plainScreen(m)
	for _, want := range []string{"DATABASE_URL missing", "Added by you", "k key", "x remove", "Next press k"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the page does not show %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "s sign in") {
		t.Error("a program's page offers a sign-in")
	}
	m = pressPanel(m, "k")
	if m.pendingMCPAdd == nil || m.pendingMCPAdd.EnvName != "DATABASE_URL" || m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("k opened no hidden prompt: %+v echo %v", m.pendingMCPAdd, m.input.EchoMode)
	}
	const secret = "postgres://u:fake-pw-7c1@db/x"
	m = typeLine(m, secret)
	if sets := f.requests(t, protocol.MCPActionSetEnv); len(sets) != 1 || sets[0].EnvValue != secret || sets[0].Name != "postgres" {
		t.Fatalf("the key was sent as %+v", sets)
	}
	if strings.Contains(plainScreen(m), secret) || turnsContaining(m, secret) != 0 {
		t.Fatal("the key is on screen")
	}
}

func TestTheAddPageReadsEveryWayOfGivingAServer(t *testing.T) {
	for _, c := range []struct {
		line string
		want []protocol.MCPRequest
	}{
		{"https://mcp.figma.com/mcp", []protocol.MCPRequest{{Action: protocol.MCPActionAdd, Name: "figma", Transport: "http",
			URL: "https://mcp.figma.com/mcp"}}},
		{"lin https://mcp.linear.app/sse", []protocol.MCPRequest{{Action: protocol.MCPActionAdd, Name: "lin", Transport: "http",
			URL: "https://mcp.linear.app/sse"}}},
		{"files --env TOKEN npx -y fs ~/notes", []protocol.MCPRequest{{Action: protocol.MCPActionAdd, Name: "files",
			Env: []string{"TOKEN"}, Command: "npx", Args: []string{"-y", "fs", "~/notes"}}}},
	} {
		got, _, err := mcpAddRequests(c.line)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v (%v)\nwant %+v", c.line, got, err, c.want)
		}
	}
	const secret = "sk-fake-0d9e"
	for _, line := range []string{`{"command": ` + secret, `two words {"command":"npx"}`, "files TOKEN=" + secret + " npx x"} {
		if _, _, err := mcpAddRequests(line); err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %v", line, err)
		}
	}
}

// Changing a server starts from every flag it was added with, so editing one
// argument does not drop its --env or --header names.
func TestChangingAServerKeepsItsFlags(t *testing.T) {
	for _, s := range []protocol.MCPServerInfo{
		{Name: "files", Program: "npx", Args: []string{"-y", "fs", "/home/me/My Notes"}, Env: []string{"A", "B"},
			Cwd: protocol.MCPCwdWorkspace},
		{Name: "lin", URL: "https://mcp.linear.app/sse", Transport: "sse", Headers: []string{"Authorization"}},
	} {
		reqs, _, err := mcpAddRequests(editLine(s))
		if err != nil || len(reqs) != 1 {
			t.Fatalf("%s: %v", editLine(s), err)
		}
		r := reqs[0]
		if r.Name != s.Name || r.Command != s.Program || !reflect.DeepEqual(r.Args, s.Args) || !reflect.DeepEqual(r.Env, s.Env) ||
			r.Cwd != s.Cwd || r.URL != s.URL || r.Transport != s.Transport || len(r.Headers) != len(s.Headers) {
			t.Errorf("%s read back as %+v", editLine(s), r)
		}
	}
}

func TestNameFromURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://mcp.figma.com/mcp":          "figma",
		"https://mcp.linear.app/sse":         "linear",
		"https://api.githubcopilot.com/mcp/": "githubcopilot",
		"http://localhost:3845/mcp":          "localhost",
	} {
		if got := nameFromURL(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// The panel fits the transcript's place on a small terminal, keys included --
// the review of three servers too, which scrolls.
func TestThePanelFitsASmallTerminal(t *testing.T) {
	m, _ := openPanel(t, panelServers)
	review := pasteBracketed(m, threeServers)
	for _, model := range []chatModel{m, review} {
		for _, size := range [][2]int{{60, 18}, {40, 14}} {
			sized, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			small := sized.(chatModel)
			if lines := strings.Count(small.View(), "\n") + 1; lines > size[1] {
				t.Errorf("%dx%d: the screen is %d lines", size[0], size[1], lines)
			}
			if !strings.Contains(plainScreen(small), "esc") {
				t.Errorf("%dx%d: the keys were cut:\n%s", size[0], size[1], plainScreen(small))
			}
		}
	}
	sized, _ := review.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	small := pressPanel(sized.(chatModel), "down", "down", "down")
	if !strings.Contains(plainScreen(small), "more line") {
		t.Errorf("the long review does not say it scrolls:\n%s", plainScreen(small))
	}
}
