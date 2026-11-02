package tui

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"mochiii/protocol"
)

// THE MCP SERVERS PANEL (/mcp).
//
// FOUND 2026-11-01: a user pasted Figma's "mcpServers" entry into the chat,
// the model told them to add it "in Customize > MCP" -- a VS Code page -- and
// the terminal had nothing to show them instead: /mcp-server answered with a
// report and fourteen subcommands to learn. The owner's reference was Cline's
// /mcp, a box over the chat listing the servers. Cline's box only reads; to
// change anything it says to run another command. This one is where the
// changing is done.
//
// It is drawn where the transcript is, with the input box still under it,
// because the steps that need typing already live there: a key is typed into
// the hidden prompt (/mcp-server key), and a server is added or approved only
// when its name is typed (answerMCPAdd). The panel starts those flows and
// shows their answers; it adds no second way of agreeing to anything.

// mcpPanelView is which page of the panel is showing.
type mcpPanelView int

const (
	mcpPanelList mcpPanelView = iota
	mcpPanelDetail
	mcpPanelAdd
)

// mcpAddRowID is the "+ Add a server" row's id. A server name cannot hold a
// NUL, so it is never taken for one.
const mcpAddRowID = "\x00add"

// mcpPanelFile is where added servers are kept, said at the foot of the list
// for a user who would rather edit it (daemon/mcpuser.go, userMCPFileName).
const mcpPanelFile = "~/.mochiii/mcp-servers.json"

// mcpPanel is the panel's state; chatModel.mcpPanel is nil when it is closed.
type mcpPanel struct {
	view    mcpPanelView
	loading bool
	servers []protocol.MCPServerInfo
	picker  listPicker
	// open is the server the detail page shows.
	open string
	// editing is the server the add page changes, or "" to add a new one.
	editing string
	// notice is the daemon's last answer, shown under the list.
	notice string
	// removeArmed is the server x was pressed on once; x again removes it.
	removeArmed string
	// focus is a server just added: the next list lands on it, and on its
	// page when it needs something from the user (a sign-in, a key).
	focus string
	// paste is servers pasted in, on their way to being added (mcppaste.go).
	paste *mcpPaste
	// pasteCut: the last paste was longer than mcpPasteMax and was cut.
	pasteCut bool
	// held is a key in its quiet window (holdKey): it acts only if no other
	// key follows at once, so pasted text never presses a hotkey.
	held    tea.KeyMsg
	holding bool
	heldSeq int
}

// openMCPPanel is /mcp: the panel, filled from the daemon's list. The list
// starts nothing (MCPActionServers), so it opens at once.
func (m chatModel) openMCPPanel() (tea.Model, tea.Cmd) {
	p := &mcpPanel{loading: true, picker: newListPicker("", nil)}
	p.picker.bare = true
	m.mcpPanel = p
	return m, submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionServers})
}

// closeMCPPanel puts the transcript back.
func (m *chatModel) closeMCPPanel() {
	m.mcpPanel = nil
	if m.pendingMCPAdd == nil {
		m.input.Placeholder = defaultInputPlaceholder
	}
}

// refreshMCPPanel asks for the list again, after anything that changed it.
func (m chatModel) refreshMCPPanel() tea.Cmd {
	if m.mcpPanel == nil {
		return nil
	}
	return submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionServers})
}

// handleMCPPanelServers takes the list into the panel.
func (m chatModel) handleMCPPanelServers(msg mcpResultMsg) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if p == nil {
		return m, nil // closed while the list was on its way
	}
	p.loading = false
	switch {
	case msg.err != nil:
		p.notice = "could not ask the daemon: " + sanitizeText(msg.err.Error())
		return m, nil
	case msg.resp.Error != "":
		p.notice = sanitizeText(msg.resp.Error)
		return m, nil
	}
	p.servers = sortedForPanel(msg.resp.Servers)
	selected, _ := p.picker.selected()
	p.picker.rows = mcpPanelRows(p.servers)
	p.picker.selectID(selected.ID)
	if s := p.server(p.focus); s != nil {
		p.picker.selectID(s.Name)
		if needsUser(*s) {
			p.view, p.open = mcpPanelDetail, s.Name
		}
	}
	p.focus = ""
	if !msg.resp.AgentMode {
		p.notice = "Agent mode is off in this daemon's config, so no server's tools are offered to the model."
	}
	if p.view == mcpPanelDetail && p.server(p.open) == nil {
		p.view = mcpPanelList // removed
	}
	// The list after a paste's last add is what its results are read from.
	m.summarizePaste()
	return m, nil
}

// server is the listed server named name, or nil.
func (p *mcpPanel) server(name string) *protocol.MCPServerInfo {
	for i := range p.servers {
		if p.servers[i].Name == name {
			return &p.servers[i]
		}
	}
	return nil
}

// needsUser reports whether a server is waiting on the user: to approve it,
// sign in, or give it a key. Those are listed first and counted in the title.
func needsUser(s protocol.MCPServerInfo) bool {
	if s.Disabled || s.Approval == protocol.MCPApprovalRejected {
		return false
	}
	return s.Approval == protocol.MCPApprovalPending || s.State == protocol.MCPStateNeedsAuth ||
		s.State == protocol.MCPStateFailed || len(s.EnvMissing) > 0 || len(s.HeadersMissing) > 0
}

// sortedForPanel orders the servers as the panel lists them: what needs the
// user, then what is in use, then what is paused or rejected; by name within.
func sortedForPanel(servers []protocol.MCPServerInfo) []protocol.MCPServerInfo {
	rank := func(s protocol.MCPServerInfo) int {
		switch {
		case needsUser(s):
			return 0
		case s.Disabled || s.Approval == protocol.MCPApprovalRejected:
			return 2
		}
		return 1
	}
	out := append([]protocol.MCPServerInfo(nil), servers...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// mcpPanelRows is a row per server, in the daemon's status words, and the
// row that adds one.
func mcpPanelRows(servers []protocol.MCPServerInfo) []pickerRow {
	rows := make([]pickerRow, 0, len(servers)+1)
	for _, s := range servers {
		status := sanitizeText(s.StatusLine)
		if status == "" {
			status = "○ Not started yet"
		}
		rows = append(rows, pickerRow{
			ID:    s.Name,
			Label: sanitizeText(s.Name),
			Hint:  status + " · " + serverWhere(s),
			Dim:   s.Disabled || s.Approval == protocol.MCPApprovalRejected,
		})
	}
	return append(rows, pickerRow{ID: mcpAddRowID, Label: "+ Add a server…", Hint: "paste its JSON, a command, or a URL"})
}

// serverWhere is what a server is, in a few words: the host a remote one
// reaches, or the program a local one runs.
func serverWhere(s protocol.MCPServerInfo) string {
	var where string
	switch {
	case s.URL != "":
		where = "remote"
		if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
			where += " " + u.Host
		}
	case s.Program != "":
		where = s.Program
	default:
		where, _, _ = strings.Cut(s.Command, " ")
	}
	switch {
	case s.Approval != "":
		where += " · from this project's .mcp.json"
	case s.Source != protocol.MCPSourceUser:
		where += " · from the daemon's config"
	}
	return sanitizeText(where)
}

// handleMCPPanelKey takes a key while the panel is open. done is false for a
// key the panel leaves to the input box: typing on the add page, and while a
// paste's question is in the box.
//
// A PASTE IS NEVER KEYS. One that comes bracketed (msg.Paste) is read as
// servers, from any page; one from a terminal that sends pastes as typing is
// caught by holding each action key for hotkeyQuiet (holdKey).
func (m chatModel) handleMCPPanelKey(msg tea.KeyMsg) (next tea.Model, cmd tea.Cmd, done bool) {
	p := m.mcpPanel
	if p.paste != nil {
		return m.pasteKey(msg)
	}
	if msg.Paste {
		text := string(msg.Runes)
		if strings.HasPrefix(strings.TrimSpace(text), "{") || p.view != mcpPanelAdd {
			if !strings.Contains(text, "{") {
				// Not JSON: a command or a URL, put in the box to read over
				// and send with enter.
				return m.takeTypedPaste(text), nil, true
			}
			if p.view == mcpPanelAdd && p.editing != "" {
				return m, nil, false
			}
			next, cmd := m.beginPaste(text)
			return next, cmd, true
		}
		return m, nil, false
	}
	key := msg.String()
	if p.view == mcpPanelAdd {
		// AN ENTER IS HELD HERE TOO. From a terminal that sends a paste as
		// typing, each of its newlines is an enter with more keys right behind
		// it: a space in the box, not a send of the first line alone.
		if p.holding {
			p.holding = false
			p.heldSeq++
			m.input.SetValue(m.input.Value() + " ")
			m.input.CursorEnd()
			if key != "enter" {
				return m, nil, false // the key itself is typed into the box
			}
		}
		switch key {
		case "esc":
			m.input.SetValue("")
			m.input.Placeholder = defaultInputPlaceholder
			p.view, p.editing = mcpPanelList, ""
			return m, nil, true
		case "enter":
			next, cmd = m.holdKey(msg)
			return next, cmd, true
		}
		return m, nil, false
	}
	switch key {
	case "up", "down", "pgup", "pgdown", "home", "end", "esc", "left", "backspace", "ctrl+p", "ctrl+n":
		// Never in a paste's bytes as these keys: they act at once.
		if p.holding {
			break // a key inside a held key's window: both are text
		}
		next, cmd := m.mcpPanelAction(msg)
		return next, cmd, true
	}
	next, cmd = m.holdKey(msg)
	return next, cmd, true
}

// mcpPanelAction is a key acting on the list or a server's page.
func (m chatModel) mcpPanelAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	key := msg.String()
	if key != "x" {
		p.removeArmed = ""
	}
	if p.view == mcpPanelDetail {
		return m.mcpPanelDetailKey(key)
	}
	switch key {
	case "esc", "q":
		m.closeMCPPanel()
		return m, nil
	case "up", "ctrl+p", "k":
		p.picker.move(-1)
	case "down", "ctrl+n", "j":
		p.picker.move(1)
	case "home":
		p.picker.move(-len(p.picker.rows))
	case "end":
		p.picker.move(len(p.picker.rows))
	case "a":
		return m.beginMCPPanelAdd(""), nil
	case "{":
		// The first key of JSON typed by hand: the add page, with it.
		return m.takeTypedPaste("{"), nil
	case "r":
		p.loading, p.notice = true, "starting every server to see whether it works…"
		return m, submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionCheck})
	case "enter":
		row, ok := p.picker.selected()
		switch {
		case !ok:
		case row.ID == mcpAddRowID:
			return m.beginMCPPanelAdd(""), nil
		default:
			p.view, p.open, p.notice = mcpPanelDetail, row.ID, ""
		}
	case " ":
		if row, ok := p.picker.selected(); ok && row.ID != mcpAddRowID {
			return m.mcpPanelAct(p.server(row.ID), "pause")
		}
	}
	return m, nil
}

// mcpPanelDetailKey is a key on one server's page.
func (m chatModel) mcpPanelDetailKey(key string) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	s := p.server(p.open)
	if s == nil || key == "esc" || key == "q" || key == "left" || key == "backspace" {
		p.view, p.notice = mcpPanelList, ""
		return m, nil
	}
	action := map[string]string{"k": "key", "h": "header", "s": "sign-in", "c": "check", " ": "pause",
		"x": "remove", "e": "change", "a": "approve", "r": "reject"}[key]
	if action == "" {
		return m, nil
	}
	return m.mcpPanelAct(s, action)
}

// mcpPanelAct does one thing to one server, if it is a thing that server
// allows; otherwise the notice says why not.
func (m chatModel) mcpPanelAct(s *protocol.MCPServerInfo, action string) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if s == nil {
		return m, nil
	}
	name := s.Name
	own := s.Source == protocol.MCPSourceUser && s.Approval == ""
	refuse := func(why string) (tea.Model, tea.Cmd) {
		p.notice = why
		return m, nil
	}
	send := func(req protocol.MCPRequest, doing string) (tea.Model, tea.Cmd) {
		p.loading, p.notice = true, doing
		return m, submitMCP(m.clientName, req)
	}
	switch action {
	case "key":
		vars := append(append([]string(nil), s.EnvMissing...), s.Env...)
		if len(vars) == 0 {
			return refuse(sanitizeText(name) + " reads no variables, so it has no key to give.")
		}
		m.beginHiddenValue(protocol.MCPRequest{Action: protocol.MCPActionSetEnv, Name: name, EnvName: vars[0]},
			"the value of "+vars[0])
		p.notice = "Type the value of " + sanitizeText(vars[0]) + " in the box below; it is not shown. Esc cancels."
		return m, nil
	case "header":
		headers := append(append([]string(nil), s.HeadersMissing...), s.Headers...)
		if len(headers) == 0 {
			return refuse(sanitizeText(name) + " sends no headers. A server that signs you in takes s instead.")
		}
		m.beginHiddenValue(protocol.MCPRequest{Action: protocol.MCPActionSetEnv, Name: name, HeaderName: headers[0]},
			"the value of the "+headers[0]+" header")
		p.notice = "Type the value of the " + sanitizeText(headers[0]) + " header (for example Bearer <token>) in the " +
			"box below; it is not shown. Esc cancels."
		return m, nil
	case "sign-in":
		if s.URL == "" {
			return refuse(sanitizeText(name) + " is a program on this computer; only a remote server signs you in.")
		}
		// The address to open is long and has to be copied whole, which a box
		// that wraps it cannot offer: the panel steps aside for the chat.
		m.closeMCPPanel()
		m.appendTurn(turn{role: roleAssistant, local: true, text: "asking " + sanitizeText(name) + " where to sign in…"})
		m.resizeViewport()
		m.refreshViewport()
		return m, submitMCP(m.clientName, protocol.MCPRequest{Action: protocol.MCPActionAuthStart, Name: name})
	case "check":
		return send(protocol.MCPRequest{Action: protocol.MCPActionCheck, Name: name}, "starting "+sanitizeText(name)+"…")
	case "pause":
		if !own {
			return refuse("Only a server you added can be paused here.")
		}
		verb := map[bool]string{true: protocol.MCPActionEnable, false: protocol.MCPActionDisable}[s.Disabled]
		return send(protocol.MCPRequest{Action: verb, Name: name}, map[bool]string{true: "resuming ", false: "pausing "}[s.Disabled]+sanitizeText(name)+"…")
	case "remove":
		if !own {
			return refuse("Only a server you added can be removed here; a project's is rejected with r instead.")
		}
		if p.removeArmed != name {
			p.removeArmed = name
			return refuse("Press x again to remove " + sanitizeText(name) + ". Its keys are kept, for adding it back.")
		}
		p.removeArmed = ""
		return send(protocol.MCPRequest{Action: protocol.MCPActionRemove, Name: name}, "removing "+sanitizeText(name)+"…")
	case "change":
		if !own {
			return refuse("Only a server you added can be changed here.")
		}
		return m.beginMCPPanelAdd(name), nil
	case "approve", "reject":
		if s.Approval == "" {
			return refuse(sanitizeText(name) + " is not one this project proposes.")
		}
		return send(protocol.MCPRequest{Action: action, Name: name}, "asking about "+sanitizeText(name)+"…")
	}
	return m, nil
}

// beginHiddenValue opens the hidden prompt for a key or header value: the
// same one /mcp-server key opens, answered by answerMCPAdd.
func (m *chatModel) beginHiddenValue(req protocol.MCPRequest, what string) {
	m.pendingMCPAdd = &req
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoPassword
	m.input.Placeholder = sanitizeText(what) + " — hidden; esc cancels"
}

// beginMCPPanelAdd opens the add page: the box below takes what is pasted or
// typed. Changing a server starts from what it is now.
func (m chatModel) beginMCPPanelAdd(editing string) chatModel {
	p := m.mcpPanel
	p.view, p.editing, p.notice = mcpPanelAdd, editing, ""
	m.input.SetValue("")
	m.input.Placeholder = "paste the server's JSON, or type: <name> <url>  or  <name> <command> [args…]"
	if s := p.server(editing); s != nil {
		line := editLine(*s)
		m.input.SetValue(line)
		m.input.SetCursor(len(line))
	}
	return m
}

// editLine is a server as the add page reads it back: every flag it was added
// with, so changing one argument does not drop its --env or --header names.
func editLine(s protocol.MCPServerInfo) string {
	words := []string{s.Name}
	if s.URL != "" {
		transport := s.Transport
		if transport == "" {
			transport = "http"
		}
		words = append(words, "--transport", transport)
		for _, h := range s.Headers {
			words = append(words, "--header", h)
		}
		return quoteWords(append(words, s.URL))
	}
	for _, e := range s.Env {
		words = append(words, "--env", e)
	}
	if s.Cwd == protocol.MCPCwdWorkspace {
		words = append(words, "--in-project")
	}
	return quoteWords(append(append(words, s.Program), s.Args...))
}

// quoteWords joins words for splitCommandLine to cut the same way again.
func quoteWords(words []string) string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "" || strings.ContainsAny(w, " \t'\"") {
			q := `"`
			if strings.Contains(w, `"`) {
				q = "'"
			}
			w = q + w + q
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// submitMCPPanelAdd sends what the add page was given: new servers go the
// paste's way (beginPaste), each shown and agreed to by name; a change to one
// already added is one request, agreed to as a change (handleMCPResult).
//
// THE LINE IS NEVER REMEMBERED. Pasted JSON can carry a key; the line goes to
// the daemon, which keeps any key out of the server list, and leaves the box.
func (m chatModel) submitMCPPanelAdd(raw string) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if raw == "" {
		return m, nil
	}
	if p.editing == "" {
		return m.beginPaste(raw)
	}
	reqs, _, err := mcpAddRequests(raw)
	if err == nil {
		switch {
		case len(reqs) != 1 || reqs[0].Name != p.editing:
			err = fmt.Errorf("to change %s, keep its name first; to add another server, esc and press a", p.editing)
		case reqs[0].Action == protocol.MCPActionAddJSON:
			err = errors.New("give the changed server as a command or a URL, not JSON")
		default:
			reqs[0].Action = protocol.MCPActionUpdate
		}
	}
	if err != nil {
		p.notice = sanitizeText(err.Error()) + ". Nothing was sent."
		return m, nil
	}
	m.input.SetValue("")
	m.input.Placeholder = defaultInputPlaceholder
	p.view, p.editing, p.loading = mcpPanelList, "", true
	p.notice = "asking the daemon about " + sanitizeText(reqs[0].Name) + "…"
	return m, submitMCP(m.clientName, reqs[0])
}

// mcpAddRequests reads the add page's line: JSON (pastedServers' shapes, or
// one server's entry after its name), "<name> <url>", a URL alone, or
// "<name> <command> [args…]" as /mcp-server add takes it.
func mcpAddRequests(raw string) ([]protocol.MCPRequest, []string, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		return nil, nil, errors.New("this is a JSON list; paste the object that holds the servers, from { to }")
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		name := strings.TrimSpace(raw[:i])
		if strings.ContainsAny(name, " \t\n") {
			return nil, nil, errors.New("there is text before the JSON; paste it from { to }, or put only a server's " +
				"name before it")
		}
		if name != "" && checkServerName(name) != nil {
			return nil, nil, errors.New("the text before the JSON is not a server name; paste the JSON from { to }")
		}
		return pastedServers(name, []byte(raw[i:]))
	}
	words, err := splitCommandLine(raw)
	if err != nil {
		return nil, nil, err
	}
	if len(words) == 1 && isURL(words[0]) {
		words = []string{nameFromURL(words[0]), words[0]}
	}
	if len(words) == 2 && isURL(words[1]) {
		words = []string{words[0], "--transport", "http", words[1]}
	}
	req, err := parseMCPAdd(words)
	if err != nil {
		return nil, nil, err
	}
	return []protocol.MCPRequest{req}, nil, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// nameFromURL is a name for a server given only its address: the site's own
// name, without the "mcp." or "api." in front -- mcp.figma.com is figma.
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "remote"
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) > 1 {
		labels = labels[:len(labels)-1] // the top-level domain
	}
	for len(labels) > 1 && (labels[0] == "mcp" || labels[0] == "api" || labels[0] == "www") {
		labels = labels[1:]
	}
	return labels[len(labels)-1]
}

var (
	mcpPanelBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorBlue).Padding(0, 2)
	mcpPanelTitle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	mcpPanelLabel = lipgloss.NewStyle().Foreground(colorGray)
)

// mcpPanelMaxWidth keeps the box readable on a wide terminal, as Cline's is.
const mcpPanelMaxWidth = 84

// renderMCPPanel draws the panel centred in width x height: the transcript's
// place on screen.
func (m chatModel) renderMCPPanel(width, height int) string {
	p := m.mcpPanel
	boxW := min(width-2, mcpPanelMaxWidth)
	inner := max(boxW-6, 20) // the border and two columns of padding, each side
	wrap := lipgloss.NewStyle().Width(inner)

	need := 0
	for _, s := range p.servers {
		if needsUser(s) {
			need++
		}
	}
	summary := ""
	if len(p.servers) > 0 {
		summary = fmt.Sprintf("%d server%s", len(p.servers), map[bool]string{true: "", false: "s"}[len(p.servers) == 1])
		if need > 0 {
			summary += fmt.Sprintf(" · %d need%s you", need, map[bool]string{true: "s", false: ""}[need == 1])
		}
	}
	title := mcpPanelTitle.Render("MCP Servers")
	if gap := inner - lipgloss.Width(title) - lipgloss.Width(summary); gap > 1 && summary != "" {
		title += strings.Repeat(" ", gap) + mcpPanelLabel.Render(summary)
	}

	notice := p.notice
	if p.loading && notice == "" {
		notice = "asking the daemon…"
	}
	// A QUESTION OPEN IN THE BOX BELOW -- a server's name to agree to, a value
	// to type hidden -- takes the keys, and its words take the page: the
	// warning being agreed to is shown whole, never cut to fit a list beside it
	// (seen in the real app, 2026-11-01: its last line was).
	asking := m.pendingMCPAdd != nil && notice != ""

	var body []string
	var keys string
	switch {
	case asking:
		keys = "answer in the box below · esc cancels"
	case p.paste != nil:
		body, keys = m.renderPaste(inner)
		// A paste's pages scroll: the review of several servers, or one
		// server's whole warning, may be longer than the panel.
		noticeLines := 0
		if notice != "" {
			noticeLines = 1 + lipgloss.Height(wrap.Render(notice))
		}
		room := height - 2 - 2 - noticeLines - 1 - lipgloss.Height(wrap.Render(keys))
		body = scrollWindow(strings.Split(strings.Join(body, "\n"), "\n"), &p.paste.scroll, room)
	case p.view == mcpPanelDetail:
		body, keys = m.mcpPanelDetailLines(inner), detailKeys(p.server(p.open))
	case p.view == mcpPanelAdd:
		body, keys = mcpPanelAddLines(p.editing), "enter send · esc back"
	default:
		keys = "paste JSON to add · ↑↓ choose · enter open · a add · space pause · r check all · esc close"
		if len(p.servers) == 0 && !p.loading {
			body = append(body, "No servers yet.", "",
				wrap.Render("Paste a server's JSON from its setup page here -- one server, or a whole "+
					"\"mcpServers\" block of them -- or press a to give a command or a URL."), "")
		}
		// The list has what the title, the notice, the foot and the keys
		// leave: its own two marker lines, the file line, and the gaps.
		noticeLines := 0
		if notice != "" {
			noticeLines = 1 + lipgloss.Height(wrap.Render(notice))
		}
		used := 2 + lipgloss.Height(strings.Join(body, "\n")) + 2 + 2 + noticeLines + 1 + lipgloss.Height(wrap.Render(keys))
		rows := max(height-2-used, 2)
		body = append(body, p.picker.view(inner, rows))
		body = append(body, "", mcpPanelLabel.Render("Saved in "+mcpPanelFile))
	}
	keyBlock := helpStyle.Render(wrap.Render(keys))
	lines := []string{title, ""}
	lines = append(lines, body...)
	if notice != "" {
		lines = append(lines, "", wrap.Render(notice))
	}
	lines = append(lines, "", keyBlock)

	content := strings.Join(lines, "\n")
	// A box still taller than its place loses lines above the keys, never the
	// keys: they are how the user leaves.
	all := strings.Split(content, "\n")
	tail := 1 + lipgloss.Height(keyBlock)
	if room := height - 2; len(all) > room && room > tail {
		content = strings.Join(append(all[:room-tail:room-tail], all[len(all)-tail:]...), "\n")
	}
	box := mcpPanelBox.Width(boxW - 2).Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// scrollWindow is the room lines of lines from *at, with a marker line for
// what is above and below; *at is kept in range.
func scrollWindow(lines []string, at *int, room int) []string {
	if room < 3 || len(lines) <= room {
		*at = 0
		return lines
	}
	shown := room - 2 // the two marker lines
	*at = min(max(*at, 0), len(lines)-shown)
	out := []string{""}
	if *at > 0 {
		out[0] = mcpPanelLabel.Render(fmt.Sprintf("▲ %d more line%s (↑)", *at, plural(*at)))
	}
	out = append(out, lines[*at:*at+shown]...)
	below := ""
	if rest := len(lines) - *at - shown; rest > 0 {
		below = mcpPanelLabel.Render(fmt.Sprintf("▼ %d more line%s (↓)", rest, plural(rest)))
	}
	return append(out, below)
}

// mcpPanelDetailLines is one server's page: what it is, what it needs, and
// where its keys stand. Values are never here, only names.
func (m chatModel) mcpPanelDetailLines(inner int) []string {
	p := m.mcpPanel
	s := p.server(p.open)
	if s == nil {
		return nil
	}
	wrap := lipgloss.NewStyle().Width(inner)
	field := func(label, value string) string {
		return wrap.Render(mcpPanelLabel.Render(fmt.Sprintf("%-10s", label)) + " " + sanitizeText(value))
	}
	lines := []string{mcpPanelTitle.Render(sanitizeText(s.Name)) + "  " + sanitizeText(s.StatusLine), ""}
	if s.URL != "" {
		lines = append(lines, field("Reaches", s.URL+" (a call sends its arguments there)"))
	} else {
		runs := s.Command
		if runs == "" {
			runs = quoteWords(append([]string{s.Program}, s.Args...))
		}
		lines = append(lines, field("Runs", runs+" (as you, with your permissions)"))
		where := "a private folder of its own"
		if s.Cwd == protocol.MCPCwdWorkspace {
			where = "this project"
		}
		lines = append(lines, field("Starts in", where))
	}
	added := "you"
	switch {
	case s.Approval != "":
		added = "this project's .mcp.json (" + s.Approval + ")"
	case s.Source != protocol.MCPSourceUser:
		added = "the daemon's config file"
	}
	lines = append(lines, field("Added by", added))
	if keys := keyStates(s.Env, s.EnvMissing, s.EnvOptional); keys != "" {
		lines = append(lines, field("Keys", keys))
	}
	if headers := keyStates(s.Headers, s.HeadersMissing, nil); headers != "" {
		lines = append(lines, field("Headers", headers))
	}
	if s.URL != "" {
		lines = append(lines, field("Signed in", map[bool]string{true: "yes", false: "no"}[s.SignedIn]))
	}
	if s.ToolsListed {
		lines = append(lines, field("Tools", fmt.Sprintf("%d", s.ToolCount)))
	}
	if s.StateDetail != "" && s.State == protocol.MCPStateFailed {
		lines = append(lines, field("Said", s.StateDetail))
	}
	for _, w := range s.Warnings {
		lines = append(lines, field("Warning", w))
	}
	if next := nextStep(*s); next != "" {
		lines = append(lines, "", wrap.Render(mcpPanelTitle.Render("Next")+"  "+next))
	}
	return lines
}

// nextStep is the one thing a server waits on, as the key that does it.
func nextStep(s protocol.MCPServerInfo) string {
	own := s.Source == protocol.MCPSourceUser && s.Approval == ""
	switch {
	case s.Approval == protocol.MCPApprovalPending:
		return "this project proposes it: press a to approve it, or r to reject it"
	case s.Disabled:
		return "it is paused: press space to resume it"
	case s.State == protocol.MCPStateNeedsAuth:
		return "press s to sign in, on the server's own page in your browser"
	case len(s.EnvMissing) > 0:
		return "press k to give it " + sanitizeText(s.EnvMissing[0]) + ", typed hidden"
	case len(s.HeadersMissing) > 0:
		return "press h to give it its " + sanitizeText(s.HeadersMissing[0]) + " header, typed hidden"
	case s.State == protocol.MCPStateFailed && own:
		return "press c to try it again, or e to change how it starts"
	case s.State == "":
		return "press c to start it now and see what it offers"
	}
	return ""
}

// detailKeys is the keys one server's page answers to: only the actions that
// server allows, so none of them is a key that says no.
func detailKeys(s *protocol.MCPServerInfo) string {
	if s == nil {
		return "esc back"
	}
	var keys []string
	if s.Approval == protocol.MCPApprovalPending || s.Approval == protocol.MCPApprovalRejected {
		keys = append(keys, "a approve")
	}
	if s.Approval == protocol.MCPApprovalPending {
		keys = append(keys, "r reject")
	}
	if len(s.Env)+len(s.EnvMissing) > 0 {
		keys = append(keys, "k key")
	}
	if len(s.Headers)+len(s.HeadersMissing) > 0 {
		keys = append(keys, "h header")
	}
	if s.URL != "" {
		keys = append(keys, "s sign in")
	}
	keys = append(keys, "c check")
	if s.Source == protocol.MCPSourceUser && s.Approval == "" {
		keys = append(keys, map[bool]string{true: "space resume", false: "space pause"}[s.Disabled], "e change", "x remove")
	}
	return strings.Join(append(keys, "esc back"), " · ")
}

// keyStates is each name with whether a value is set: "GITHUB_TOKEN missing,
// GITHUB_HOST set".
func keyStates(names, missing, optional []string) string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(append([]string(nil), missing...), names...) {
		if seen[n] {
			continue
		}
		seen[n] = true
		state := "set"
		switch {
		case slices.Contains(missing, n):
			state = "missing"
		case slices.Contains(optional, n):
			state = "optional"
		}
		out = append(out, n+" "+state)
	}
	return strings.Join(out, ", ")
}

// mcpPanelAddLines is the add page: what the box below takes.
func mcpPanelAddLines(editing string) []string {
	if editing != "" {
		return []string{
			"Change " + sanitizeText(editing) + ": edit the line in the box below and press enter.",
			"",
			"It is shown to you again, and changed only when you type its name. Its keys are kept.",
		}
	}
	return []string{
		"Add a server: paste or type it in the box below.",
		"",
		mcpPanelLabel.Render("JSON") + "     a setup page's \"mcpServers\" block, with one server or many",
		mcpPanelLabel.Render("Remote") + "   figma https://mcp.figma.com/mcp",
		mcpPanelLabel.Render("Program") + "  files npx -y @modelcontextprotocol/server-filesystem ~/notes",
		"",
		"A key in pasted JSON is kept in ~/.mochiii/mcp-secrets.json, never in the server list. Each server is shown " +
			"to you first, and added only when you type its name.",
	}
}
