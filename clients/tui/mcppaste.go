package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"mochiii/protocol"
)

// PASTING SERVERS INTO THE PANEL (2026-11-02).
//
// A server's setup page gives JSON -- an "mcpServers" block, often of several
// servers -- and the panel takes it pasted straight in, with no key pressed
// first. What happens next is the same grant the single add always was, said
// for every server before any is stored:
//
//  1. The paste is read here, in the order it was written. Anything that is
//     not servers is refused whole, with what was wrong, and the paste goes
//     back into the box to fix. A server is never dropped from it unsaid.
//  2. The daemon is asked about each server WITHOUT the acknowledgement, which
//     stores nothing: its answer is the warning, and the server in parts (the
//     exact command, or the URL; which keys were given and which are missing).
//     A server it refuses refuses the paste.
//  3. One review page shows them all. A name already added is a conflict: the
//     server can replace it, or be skipped.
//  4. Then one server at a time: its warning, whole, and its name typed to add
//     it (or to replace the one of that name). Enter on an empty line skips
//     it; esc cancels them all. There is no "all of them": each is a grant.
//  5. Only after the last answer is anything added: each agreed server in
//     turn, started once to see what it offers; a key it has no value for is
//     asked for, hidden, as a single add asks (askNextAddKey). One failing
//     does not stop the rest.
//  6. A line per server says how it went: connected, needs a sign-in, needs a
//     key, failed to start -- or skipped.
//
// The JSON's values never reach the panel, the transcript or the up-arrow
// recall: the daemon moves a key into the key file and answers with names.

type pasteStage int

const (
	pasteNaming     pasteStage = iota + 1 // one server with no name: the box asks for one
	pasteChecking                         // the daemon is asked about each, storing nothing
	pasteReview                           // every server on one page
	pasteConfirming                       // one at a time, each agreed to by name
	pasteAdding                           // the agreed ones added and started, in turn
	pasteDone                             // a line each for how it went
)

// The conflicts a pasted name can have with a server already listed.
const (
	conflictReplace = "replace" // one the user added: replaced only if they type its name
	conflictFixed   = "fixed"   // one set up elsewhere (the config, the project): skipped only
)

// pasteEntry is one pasted server and what became of it.
type pasteEntry struct {
	req      protocol.MCPRequest // never acknowledged here; the add is a copy
	preview  *protocol.MCPServerInfo
	warning  string
	notes    []string
	conflict string
	agreed   bool
	skipped  bool
	sent     bool
	result   string // the add's own answer
	failed   string // why it was not added
}

// mcpPaste is a paste on its way through the stages.
type mcpPaste struct {
	stage   pasteStage
	raw     string
	notes   []string // about the paste as a whole
	entries []pasteEntry
	at      int
	scroll  int
	// summarized: the results are in the transcript, once.
	summarized bool
}

// mcpPasteMsg is the daemon's answer about one pasted server.
type mcpPasteMsg struct {
	entry int
	res   mcpResultMsg
}

func submitPaste(clientName string, i int, req protocol.MCPRequest) tea.Cmd {
	return func() tea.Msg {
		resp, err := sendMCP(clientName, req)
		return mcpPasteMsg{entry: i, res: mcpResultMsg{req: req, resp: resp, err: err}}
	}
}

// mcpPasteMax bounds a paste into the panel: the daemon takes 64 KiB a server,
// and the prompt box's own bound (4000) is shorter than some whole configs.
const mcpPasteMax = 64 << 10

// HOTKEYS THAT A PASTE CANNOT PRESS.
//
// A terminal with bracketed paste (bubbletea asks for it, and most give it)
// sends a paste as one KeyMsg with Paste set, which the panel never reads as
// keys. A terminal without it sends the same bytes as typing, and bubbletea
// cuts them at every space and newline -- so "x e s" arrives as x, space, e,
// space, s: remove, pause, change. Pasted bytes arrive together, though, and
// no person types two keys 25ms apart. So a key that would act is HELD for
// that long: if another key comes inside the window, everything held and
// arriving is text, taken as a paste; if none does, the key acts.
const hotkeyQuiet = 25 * time.Millisecond

// mcpHotkeyMsg ends a held key's quiet window.
type mcpHotkeyMsg struct{ seq int }

// keyText is the text a key event stands for, as part of a paste.
func keyText(msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyRunes:
		return string(msg.Runes)
	case tea.KeySpace:
		return " "
	case tea.KeyEnter:
		return "\n"
	case tea.KeyTab:
		return "\t"
	}
	return ""
}

// holdKey starts a key's quiet window, or -- when one is already open -- turns
// what was held and this key into typed text in the add page's box.
func (m chatModel) holdKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if p.holding {
		text := keyText(p.held) + keyText(msg)
		p.holding = false
		p.heldSeq++
		return m.takeTypedPaste(text), nil
	}
	p.held, p.holding = msg, true
	p.heldSeq++
	seq := p.heldSeq
	return m, tea.Tick(hotkeyQuiet, func(time.Time) tea.Msg { return mcpHotkeyMsg{seq: seq} })
}

// handleMCPHotkey is a held key's window ending with no other key: it acts.
func (m chatModel) handleMCPHotkey(msg mcpHotkeyMsg) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if p == nil || !p.holding || msg.seq != p.heldSeq {
		return m, nil
	}
	p.holding = false
	if p.view == mcpPanelAdd {
		return m.submitAddPage()
	}
	return m.mcpPanelAction(p.held)
}

// submitAddPage is an enter on the add page that no other key followed. An
// enter inside an object still open is a line break in JSON being typed by
// hand: a space, not a send.
func (m chatModel) submitAddPage() (tea.Model, tea.Cmd) {
	text := m.input.Value()
	if jsonUnfinished(text) {
		m.input.SetValue(text + " ")
		m.input.CursorEnd()
		return m, nil
	}
	return m.submitMCPPanelAdd(strings.TrimSpace(text))
}

// takeTypedPaste puts text that arrived as keys into the add page's box. The
// rest of the paste follows into the box as typing; an enter inside an
// unfinished JSON object is a space there, not a send (submitAddPage).
func (m chatModel) takeTypedPaste(text string) chatModel {
	m = m.beginMCPPanelAdd("")
	m.input.SetValue(strings.TrimLeft(strings.NewReplacer("\n", " ", "\t", " ").Replace(text), " "))
	m.input.CursorEnd()
	return m
}

// jsonUnfinished reports whether text holds a JSON object that is still open:
// more { than } outside strings.
func jsonUnfinished(text string) bool {
	i := strings.Index(text, "{")
	if i < 0 {
		return false
	}
	depth, inString, escaped := 0, false, false
	for _, r := range text[i:] {
		switch {
		case escaped:
			escaped = false
		case inString && r == '\\':
			escaped = true
		case r == '"':
			inString = !inString
		case inString:
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			depth--
		}
	}
	return depth > 0
}

// beginPaste reads raw -- pasted, or entered on the add page -- and starts it
// through the stages.
func (m chatModel) beginPaste(raw string) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if p.pasteCut {
		p.pasteCut = false
		return m.refusePaste("", fmt.Sprintf("the paste is longer than %d KiB, more than the panel takes; paste fewer "+
			"servers at a time", mcpPasteMax>>10)), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return m, nil
	}
	reqs, notes, err := mcpAddRequests(raw)
	if err != nil {
		return m.refusePaste(raw, err.Error()), nil
	}
	m.input.SetValue("")
	p.view, p.notice = mcpPanelList, ""
	p.paste = &mcpPaste{raw: raw, notes: notes}
	for _, r := range reqs {
		p.paste.entries = append(p.paste.entries, pasteEntry{req: r})
	}
	if len(reqs) == 1 && reqs[0].Name == "" {
		p.paste.stage = pasteNaming
		m.input.Placeholder = "a name for this server: lower-case letters, digits, - or _"
		return m, nil
	}
	return m.checkPaste()
}

// refusePaste ends a paste that cannot be added: why, said plainly, and the
// paste back in the box to fix -- a refusal never makes the user find it again.
func (m chatModel) refusePaste(raw, why string) chatModel {
	p := m.mcpPanel
	p.paste = nil
	m = m.beginMCPPanelAdd("")
	notice := "Not added: " + sanitizeText(why) + ". Nothing was added."
	switch {
	case raw == "":
	case len([]rune(raw)) > m.input.CharLimit:
		notice += " The paste is longer than the box below holds: fix it where you copied it from, and paste it again."
	default:
		m.input.SetValue(strings.NewReplacer("\n", " ", "\t", " ").Replace(raw))
		m.input.CursorEnd()
		notice += " What you pasted is in the box below to fix; esc discards it."
	}
	p.notice = notice
	return m
}

// checkPaste marks each name's conflict, then asks the daemon about the first
// server it has no answer for.
func (m chatModel) checkPaste() (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	ps := p.paste
	ps.stage = pasteChecking
	for i := range ps.entries {
		e := &ps.entries[i]
		e.conflict, e.req.Replace = "", false
		if s := p.server(e.req.Name); s != nil {
			if s.Source == protocol.MCPSourceUser && s.Approval == "" {
				e.conflict, e.req.Replace = conflictReplace, true
			} else {
				e.conflict = conflictFixed
			}
		}
	}
	return m.checkNext()
}

func (m chatModel) checkNext() (tea.Model, tea.Cmd) {
	ps := m.mcpPanel.paste
	for i := range ps.entries {
		e := &ps.entries[i]
		if e.conflict != conflictFixed && e.warning == "" {
			m.mcpPanel.loading = true
			return m, submitPaste(m.clientName, i, e.req)
		}
	}
	m.mcpPanel.loading = false
	ps.stage, ps.scroll = pasteReview, 0
	m.input.Placeholder = defaultInputPlaceholder
	return m, nil
}

// handleMCPPaste takes the daemon's answer about one pasted server.
func (m chatModel) handleMCPPaste(msg mcpPasteMsg) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	if p == nil || p.paste == nil || msg.entry >= len(p.paste.entries) {
		return m, nil // cancelled, or the panel closed, while it was on its way
	}
	ps := p.paste
	e := &ps.entries[msg.entry]
	r := msg.res
	failure := ""
	switch {
	case r.err != nil:
		failure = "could not ask the daemon: " + r.err.Error()
	case r.resp.Error != "":
		failure = r.resp.Error
	}
	switch ps.stage {
	case pasteChecking:
		if failure != "" {
			return m.refusePaste(ps.raw, e.req.Name+": "+failure), nil
		}
		if !r.resp.NeedsAcknowledgement {
			// Only a daemon that stored it unasked answers so. Say it; stop.
			return m.refusePaste(ps.raw, e.req.Name+": the daemon did not ask before adding it; restart it with "+
				"./run-tui.sh --stop && ./run-tui.sh"), nil
		}
		e.warning, e.preview, e.notes = r.resp.Detail, r.resp.Preview, r.resp.Notes
		return m.checkNext()
	case pasteAdding:
		p.loading = false
		if failure != "" {
			e.failed = failure
			m.sayMCP(sanitizeText(e.req.Name)+": not added: "+sanitizeText(failure), false)
			return m, m.addNext()
		}
		e.result = r.resp.Detail
		m.sayMCP(formatMCPResult(r), true)
		// A key it has no value for is asked for now, hidden, before the next
		// server is added: the add flow's own questions (askNextAddKey).
		for _, info := range r.resp.Servers {
			if info.Name != e.req.Name {
				continue
			}
			for _, n := range info.EnvMissing {
				addKeys.pending = append(addKeys.pending, addKey{name: n})
			}
			for _, h := range info.HeadersMissing {
				addKeys.pending = append(addKeys.pending, addKey{name: h, header: true})
			}
		}
		if len(addKeys.pending) > 0 {
			addKeys.server, addKeys.stored = e.req.Name, false
			return m.askNextAddKey()
		}
		return m, m.addNext()
	}
	return m, nil
}

// addNext sends the next agreed server's add, or -- with none left -- ends the
// paste and asks for the list its results are read from.
func (m chatModel) addNext() tea.Cmd {
	p := m.mcpPanel
	if p == nil || p.paste == nil || p.paste.stage != pasteAdding {
		return nil
	}
	for i := range p.paste.entries {
		e := &p.paste.entries[i]
		if e.agreed && !e.sent {
			e.sent = true
			p.loading = true
			req := e.req
			req.AcknowledgedUnconfined = true
			return submitPaste(m.clientName, i, req)
		}
	}
	p.paste.stage, p.paste.scroll = pasteDone, 0
	p.loading = true
	return m.refreshMCPPanel()
}

// pasteKey is a key while a paste is on its way: the box takes the typing
// while it asks for a name; the rest are the pages' own.
func (m chatModel) pasteKey(msg tea.KeyMsg) (next tea.Model, cmd tea.Cmd, done bool) {
	p := m.mcpPanel
	ps := p.paste
	key := msg.String()
	switch key {
	case "up", "pgup":
		ps.scroll = max(ps.scroll-map[bool]int{true: 1, false: 8}[key == "up"], 0)
		return m, nil, true
	case "down", "pgdown":
		ps.scroll += map[bool]int{true: 1, false: 8}[key == "down"]
		return m, nil, true
	}
	switch ps.stage {
	case pasteNaming, pasteConfirming:
		switch key {
		case "esc":
			return m.cancelPaste(), nil, true
		case "enter":
			next, cmd := m.answerPaste(strings.TrimSpace(m.input.Value()))
			return next, cmd, true
		}
		return m, nil, false
	case pasteChecking, pasteReview:
		switch key {
		case "esc":
			return m.cancelPaste(), nil, true
		case "enter":
			if ps.stage == pasteReview {
				return m.confirmAt(0), nil, true
			}
		}
	case pasteDone:
		if key == "esc" || key == "enter" {
			p.paste = nil
			p.view = mcpPanelList
			return m, nil, true
		}
	}
	return m, nil, true // adding: the add goes on; nothing here stops it
}

// cancelPaste is esc before the adding began: nothing was stored, nothing is.
func (m chatModel) cancelPaste() chatModel {
	p := m.mcpPanel
	p.paste, p.loading = nil, false
	p.view, p.notice = mcpPanelList, "Cancelled: nothing was added."
	m.input.SetValue("")
	m.input.Placeholder = defaultInputPlaceholder
	return m
}

// confirmAt opens server i's question in the box, or -- past the last -- adds
// what was agreed to.
func (m chatModel) confirmAt(i int) chatModel {
	ps := m.mcpPanel.paste
	ps.stage, ps.at, ps.scroll = pasteConfirming, i, 0
	m.input.SetValue("")
	if i >= len(ps.entries) {
		return m
	}
	e := ps.entries[i]
	switch e.conflict {
	case conflictFixed:
		m.input.Placeholder = "enter skips " + sanitizeText(e.req.Name) + " — esc cancels them all"
	case conflictReplace:
		m.input.Placeholder = "type " + sanitizeText(e.req.Name) + " to replace yours · enter skips it · esc cancels them all"
	default:
		m.input.Placeholder = "type " + sanitizeText(e.req.Name) + " to add it · enter skips it · esc cancels them all"
	}
	return m
}

// answerPaste takes the line typed while the box asks: a name for an unnamed
// server, or a server's name to agree to it.
func (m chatModel) answerPaste(typed string) (tea.Model, tea.Cmd) {
	p := m.mcpPanel
	ps := p.paste
	if ps.stage == pasteNaming {
		if err := checkServerName(typed); err != nil {
			p.notice = sanitizeText(err.Error())
			return m, nil
		}
		ps.entries[0].req.Name = typed
		m.input.SetValue("")
		p.notice = ""
		return m.checkPaste()
	}
	e := &ps.entries[ps.at]
	switch {
	case typed == "":
		e.skipped = true
	case typed == e.req.Name && e.conflict != conflictFixed:
		e.agreed = true
	default:
		what := "Type " + e.req.Name + " to add it"
		if e.conflict == conflictFixed {
			what = "It cannot be added under that name"
		}
		p.notice = sanitizeText("That is not what was asked. " + what + ", or press enter on an empty line to skip it.")
		return m, nil
	}
	p.notice = ""
	m = m.confirmAt(ps.at + 1)
	if ps.at < len(ps.entries) {
		return m, nil
	}
	m.input.Placeholder = defaultInputPlaceholder
	ps.stage = pasteAdding
	return m, m.addNext()
}

// checkServerName is the daemon's rule for a name (validateUserServerName),
// checked where the name is typed so the box can ask again.
func checkServerName(name string) error {
	switch {
	case name == "":
		return errors.New("type a name for the server")
	case len(name) > 32:
		return fmt.Errorf("a name is at most 32 characters; that one is %d", len(name))
	case strings.Contains(name, "__"):
		return errors.New("a name cannot hold two _ in a row")
	case name == "builtin":
		return errors.New("builtin is the name of Mochiii's own tools; choose another")
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return errors.New("a name holds only lower-case letters, digits, - and _, and starts with a letter or digit")
	}
	return nil
}

// THE SHAPES A PASTE MAY TAKE, in the order the review lists its servers:
//
//	{"mcpServers": {"a": {...}, "b": {...}}}   Claude Desktop, Cursor, Cline, Claude Code
//	{"servers": {"a": {...}}}                  VS Code's mcp.json
//	{"a": {...}, "b": {...}}                   the block alone, every value a server
//	{"command": ..., "args": [...], "env": {}} one server with no name: its name is asked
//	{"url": ..., "type": "http", "headers": {}} likewise, a remote one
//
// A server is an object with "command" (a program on this computer) or "url"
// (a remote server); the daemon reads the rest of its fields, and refuses a
// field it cannot use by name.

// entryFields mark an object as one server rather than a block of them.
var entryFields = []string{"command", "args", "env", "url", "type", "headers", "transportType", "serverUrl", "cwd"}

// pastedServers turns pasted JSON into one add_json request per server, in the
// order written. name, when given before the JSON, names a single server.
// Errors never quote the JSON: it may hold a key.
func pastedServers(name string, raw []byte) (reqs []protocol.MCPRequest, notes []string, err error) {
	keys, top, err := orderedObject(raw)
	if err != nil {
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
			return nil, nil, errors.New("this is a JSON list; paste the object that holds the servers, from { to }")
		}
		return nil, nil, err
	}
	blockKey := ""
	for _, k := range []string{"mcpServers", "servers"} {
		if _, ok := top[k]; ok {
			if blockKey != "" {
				return nil, nil, errors.New(`the paste has both an "mcpServers" and a "servers" block; paste one`)
			}
			blockKey = k
		}
	}
	single := false
	for _, f := range entryFields {
		if _, ok := top[f]; ok {
			single = true
		}
	}
	switch {
	case blockKey != "":
		var others []string
		for _, k := range keys {
			if k != blockKey {
				others = append(others, k)
			}
		}
		if len(others) > 0 {
			notes = append(notes, fmt.Sprintf("the paste's other settings (%s) are not MCP servers, so they were left "+
				"alone", strings.Join(others, ", ")))
		}
		keys, top, err = orderedObject(top[blockKey])
		switch {
		case errors.Is(err, errTwice):
			return nil, nil, err
		case err != nil:
			return nil, nil, fmt.Errorf("the %q block is not an object of servers", blockKey)
		}
		if len(keys) == 0 {
			return nil, nil, fmt.Errorf("the %q block names no servers", blockKey)
		}
	case single:
		if name == "" {
			return []protocol.MCPRequest{{Action: protocol.MCPActionAddJSON, JSON: raw}}, nil, nil
		}
		return []protocol.MCPRequest{{Action: protocol.MCPActionAddJSON, Name: name, JSON: raw}}, nil, nil
	case len(keys) == 0:
		return nil, nil, errors.New(`no servers found: the object is empty`)
	}
	// A block, named or bare: every value must be one server.
	looksLikeServer := 0
	for _, k := range keys {
		var fields map[string]json.RawMessage
		if json.Unmarshal(top[k], &fields) == nil {
			for _, f := range entryFields {
				if _, ok := fields[f]; ok {
					looksLikeServer++
					break
				}
			}
		}
	}
	for _, k := range keys {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(top[k], &fields); err != nil {
			if blockKey == "" && looksLikeServer == 0 {
				break
			}
			return nil, nil, fmt.Errorf("%q is not a server: a server is an object, { … }", truncate(k, 40))
		}
		if !hasAny(fields, "command", "url", "serverUrl") {
			if blockKey == "" && looksLikeServer == 0 {
				break
			}
			return nil, nil, fmt.Errorf("%q is not a server: it has no \"command\" to run and no \"url\" to reach", truncate(k, 40))
		}
	}
	if blockKey == "" && looksLikeServer == 0 {
		return nil, nil, errors.New(`no servers found: paste an "mcpServers" block, a {"name": {...}} object of ` +
			`servers, or one server's {"command": ...} or {"url": ...}`)
	}
	if name != "" && len(keys) > 1 {
		return nil, nil, errors.New("the block names its servers itself; paste it without a name in front")
	}
	for _, k := range keys {
		as := k
		if name != "" {
			as = name
		}
		reqs = append(reqs, protocol.MCPRequest{Action: protocol.MCPActionAddJSON, Name: as, JSON: top[k]})
	}
	return reqs, notes, nil
}

func hasAny(fields map[string]json.RawMessage, names ...string) bool {
	for _, n := range names {
		if _, ok := fields[n]; ok {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// errTwice is a name given twice in one object: which one would be meant
// cannot be told, so neither is taken.
var errTwice = errors.New("appears twice; each name once")

// orderedObject reads a JSON object's members in the order they were written:
// the order the user pasted the servers in is the order they are asked about.
func orderedObject(raw []byte) (keys []string, values map[string]json.RawMessage, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, jsonProblem(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, errors.New("this is not a JSON object; paste it from { to }")
	}
	values = map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, jsonProblem(err)
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, jsonProblem(err)
		}
		if _, dup := values[key]; dup {
			return nil, nil, fmt.Errorf("%q %w", truncate(key, 40), errTwice)
		}
		keys = append(keys, key)
		values[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, jsonProblem(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, errors.New("there is more after the JSON's closing }; paste one object")
	}
	return keys, values, nil
}

// jsonProblem says what is wrong with JSON that does not parse, by where, never
// by quoting it.
func jsonProblem(err error) error {
	var syntax *json.SyntaxError
	switch {
	case errors.As(err, &syntax):
		return fmt.Errorf("this is not valid JSON: it stops making sense at character %d (a missing comma, quote "+
			"or brace is the usual cause)", syntax.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return errors.New("the JSON ends before it is finished: a } or ] is missing")
	}
	return errors.New("this is not valid JSON")
}

// renderPaste is the panel's page while a paste is on its way.
func (m chatModel) renderPaste(inner int) (body []string, keys string) {
	ps := m.mcpPanel.paste
	wrap := lipgloss.NewStyle().Width(inner)
	say := func(s string) { body = append(body, wrap.Render(s)) }
	n := len(ps.entries)
	switch ps.stage {
	case pasteNaming:
		say("One server, pasted with no name.")
		body = append(body, "")
		say("Type a name for it in the box below: lower-case letters, digits, - or _. Nothing is added yet.")
		return body, "enter use the name · esc cancel, nothing is added"
	case pasteChecking:
		say(fmt.Sprintf("Reading %d server%s…", n, plural(n)))
		return body, "esc cancel, nothing is added"
	case pasteReview:
		say(fmt.Sprintf("%d server%s to add. Nothing is added until you type each one's name, one at a time.", n, plural(n)))
		for _, note := range ps.notes {
			say("Note: " + sanitizeText(note))
		}
		for i, e := range ps.entries {
			body = append(body, "")
			body = append(body, reviewLines(i, e, inner)...)
		}
		return body, "enter go through them one at a time · ↑↓ scroll · esc cancel, nothing is added"
	case pasteConfirming:
		e := ps.entries[ps.at]
		say(mcpPanelTitle.Render(fmt.Sprintf("%d of %d: %s", ps.at+1, n, sanitizeText(e.req.Name))))
		body = append(body, "")
		switch e.conflict {
		case conflictFixed:
			say("A server named " + sanitizeText(e.req.Name) + " is already set up, by the daemon's config or this " +
				"project; this one cannot replace it. Press enter to skip it. To add it, rename it in the JSON and " +
				"paste it again.")
		case conflictReplace:
			say("You already added a server named " + sanitizeText(e.req.Name) + ". Typing its name REPLACES that one " +
				"with this; its stored keys are kept. Enter on an empty line skips this one and keeps yours.")
			body = append(body, "")
			say(sanitizeText(e.warning))
		default:
			say(sanitizeText(e.warning))
		}
		return body, "type its name and enter · enter alone skips it · ↑↓ scroll · esc cancel them all"
	case pasteAdding:
		say("Adding what you agreed to, one at a time:")
		for _, e := range ps.entries {
			body = append(body, hanging(padName(e.req.Name), addingState(e), inner))
		}
		return body, "the box below asks when a server needs a key"
	}
	// pasteDone
	say(mcpPanelTitle.Render("Results"))
	body = append(body, "")
	for _, e := range ps.entries {
		body = append(body, hanging(padName(e.req.Name), m.pasteResult(e), inner))
	}
	return body, "enter or esc back to the list"
}

// hanging is "  name  text", with text wrapped under itself rather than under
// the name, so each server's line reads as one.
func hanging(name, text string, inner int) string {
	lead := "  " + name + "  "
	width := lipgloss.Width(lead)
	if inner-width < 20 {
		return lipgloss.NewStyle().Width(inner).Render(lead + text)
	}
	lines := strings.Split(lipgloss.NewStyle().Width(inner-width).Render(text), "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = lead + lines[i]
		} else {
			lines[i] = strings.Repeat(" ", width) + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func padName(name string) string {
	return fmt.Sprintf("%-16s", sanitizeText(name))
}

// reviewLines are one server on the review page: what it is and does, in the
// daemon's own parts, and what is still to be decided.
func reviewLines(i int, e pasteEntry, inner int) []string {
	wrap := lipgloss.NewStyle().Width(inner)
	indent := lipgloss.NewStyle().Width(inner).PaddingLeft(4)
	name := fmt.Sprintf("%d  %s", i+1, sanitizeText(e.req.Name))
	switch e.conflict {
	case conflictReplace:
		name += "  — you already have one of this name: type it to replace yours, or skip"
	case conflictFixed:
		name += "  — already set up elsewhere: this one can only be skipped"
	}
	lines := []string{wrap.Render(mcpPanelTitle.Render(name))}
	p := e.preview
	if p == nil {
		return lines
	}
	if p.URL != "" {
		lines = append(lines, indent.Render(mcpPanelLabel.Render("remote ")+p.Transport+" · sends what its tools are "+
			"given to "+sanitizeText(p.URL)))
	} else {
		lines = append(lines, indent.Render(mcpPanelLabel.Render("runs   ")+sanitizeText(p.Command)+
			mcpPanelLabel.Render("  (on this computer, as you)")))
	}
	if keys := reviewKeys(p.Env, p.EnvStored, p.EnvMissing); keys != "" {
		lines = append(lines, indent.Render(mcpPanelLabel.Render("keys   ")+keys))
	}
	if headers := reviewKeys(p.Headers, nil, p.HeadersMissing); headers != "" {
		lines = append(lines, indent.Render(mcpPanelLabel.Render("headers ")+headers))
	}
	for _, note := range e.notes {
		lines = append(lines, indent.Render(mcpPanelLabel.Render("note   ")+sanitizeText(note)))
	}
	return lines
}

// reviewKeys says of each key whether the paste gave it, it is missing, or it
// comes from elsewhere (a stored value, the daemon's environment).
func reviewKeys(names, given, missing []string) string {
	var out []string
	for _, n := range names {
		switch {
		case slices.Contains(given, n):
			out = append(out, n+" given (kept in ~/.mochiii/mcp-secrets.json)")
		case slices.Contains(missing, n):
			out = append(out, n+" missing (asked for after the add)")
		default:
			out = append(out, n+" set")
		}
	}
	return sanitizeText(strings.Join(out, ", "))
}

func addingState(e pasteEntry) string {
	switch {
	case e.skipped:
		return "skipped"
	case !e.agreed:
		return "skipped"
	case e.failed != "":
		return "not added"
	case e.result != "":
		return "added"
	case e.sent:
		return "adding and starting it…"
	}
	return "waiting"
}

// pasteResult is a server's line on the results page, read from the list as
// it stands now: the add's start, any key given since, and the check after.
func (m chatModel) pasteResult(e pasteEntry) string {
	switch {
	case e.skipped || !e.agreed:
		if e.conflict == conflictFixed {
			return "skipped: a server of that name is already set up"
		}
		return "skipped"
	case e.failed != "":
		return "not added: " + sanitizeText(e.failed)
	}
	s := m.mcpPanel.server(e.req.Name)
	if s == nil {
		return sanitizeText(firstSentence(e.result))
	}
	switch {
	case len(s.EnvMissing) > 0:
		return "needs a key: " + sanitizeText(strings.Join(s.EnvMissing, ", ")) + " — open it here and press k"
	case len(s.HeadersMissing) > 0:
		return "needs a key: its " + sanitizeText(strings.Join(s.HeadersMissing, ", ")) + " header — open it here and press h"
	case s.State == protocol.MCPStateNeedsAuth:
		return "needs sign-in — open it here and press s, or /mcp-server sign-in " + sanitizeText(s.Name)
	case s.State == protocol.MCPStateFailed:
		return "failed to start: " + sanitizeText(s.StateDetail)
	case s.State == protocol.MCPStateToolsFailed:
		return "started, but listing its tools failed: " + sanitizeText(s.ToolsError)
	case s.State == protocol.MCPStateRunning && s.ToolsListed:
		return fmt.Sprintf("connected (%d tool%s)", s.ToolCount, plural(s.ToolCount))
	case s.State == protocol.MCPStateRunning:
		return "connected"
	case strings.Contains(e.result, "did NOT start"), strings.Contains(e.result, "cannot start"):
		return "failed to start: " + sanitizeText(firstSentence(e.result))
	}
	return "added; not started yet — open it here and press c"
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// summarizePaste puts the results into the transcript once, so they outlast
// the panel.
func (m *chatModel) summarizePaste() {
	ps := m.mcpPanel.paste
	if ps == nil || ps.stage != pasteDone || ps.summarized {
		return
	}
	ps.summarized = true
	var b strings.Builder
	b.WriteString("MCP servers from your paste:")
	for _, e := range ps.entries {
		b.WriteString("\n  " + padName(e.req.Name) + "  " + m.pasteResult(e))
	}
	m.appendTurn(turn{role: roleAssistant, local: true, verbatim: true, text: b.String()})
	m.resizeViewport()
	m.refreshViewport()
}
