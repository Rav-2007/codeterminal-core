package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// chatState is where the chat model is in one request/response cycle.
// stateSplash is a one-time phase before the first cycle begins.
type chatState int

const (
	stateSplash       chatState = iota // welcome screen; any key dismisses it
	stateIdle                          // ready to accept a prompt
	stateSending                       // waiting for the first token or an immediate error
	stateStreaming                     // tokens are arriving
	stateError                         // last turn failed; shown in the header, input re-enabled
	stateEditReview                    // the last answer contained edit blocks; reviewing them one at a time
	stateToolApproval                  // an agent turn is paused on a tool-call approval
)

type turnRole int

const (
	roleUser turnRole = iota
	roleAssistant
	roleSystem // parse-error notices and end-of-review summaries
)

type turn struct {
	role turnRole
	text string
	// reasoning holds a reasoning-tier model's thinking tokens for an assistant
	// turn (see protocol.TokenResponse.Reasoning), accumulated SEPARATELY from
	// text. It is rendered as a dimmed "thinking" block above the answer and is
	// NEVER folded into text: text is what gets echoed as the answer and carried
	// back as history, and thinking must not enter either (Fix 14). Empty for a
	// non-reasoning turn, which renders exactly as before.
	reasoning string
	// incomplete is the protocol.IncompleteInfo.Reason slug when THIS assistant
	// turn was cut off rather than finished. Carried back to the daemon by
	// buildHistory so the next request's context says so.
	//
	// It is a separate field from the roleSystem notice appended beside it
	// because those two serve different readers: the notice is for the user's
	// eye and stays out of history, this is for the model and never renders.
	// Keeping the display copy as the source of truth would have meant the
	// model's view of the conversation depended on string-matching TUI chrome.
	incomplete string
}

// helpText is the persistent hint shown under the input. Conversational
// memory is on: every prompt after the first sends the transcript so far as
// PromptRequest.History (see buildHistory), so follow-ups can build on
// earlier turns. ctrl+n clears the transcript to start a fresh conversation
// with no carried-over history.
const helpText = "enter to send · ctrl+n new conversation · ctrl+c to quit"

// reviewHelpText is shown instead of helpText while reviewing edit blocks.
const reviewHelpText = "y apply · n skip · q cancel remaining"

// approvalHelpText is shown while an agent turn is paused on a tool call.
// Worded in the same shape as reviewHelpText because it is the same kind of
// moment: the product has stopped and is waiting for a person to decide.
const approvalHelpText = "y run once · a allow this tool for the turn · n deny · q stop the task"

// chatModel is the Bubble Tea model for Mochiii's interactive chat.
type chatModel struct {
	state chatState
	turns []turn

	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	statusErr string

	streamCh     chan tea.Msg       // the active stream's channel; nil when idle
	streamCancel context.CancelFunc // cancels the in-flight request; nil when idle

	// lastGrounding is the most recent GroundingInfo reported by the
	// daemon, shown in the header. Cleared at the start of each new turn
	// (set startTurn) so a stale result from a previous turn is never
	// shown as if it described the in-flight one.
	lastGrounding *protocol.GroundingInfo

	// lastRedactions is the most recent set of secret-kind labels the
	// daemon's heuristic scrubber redacted from the prompt (see
	// redactionsMsg), shown in the header exactly like lastGrounding —
	// same "arrives once, before any tokens" mechanism, same "cleared at
	// the start of each new turn" lifetime (see startTurn). Never a secret
	// value, only kind labels (e.g. "openai_key").
	lastRedactions []string

	// lastDegraded is the most recent set of reduced-subsystem reports from
	// the daemon (see degradedMsg), shown in the header on one line each.
	// Same "arrives once, before any tokens" mechanism and same
	// cleared-at-the-start-of-each-turn lifetime as lastGrounding and
	// lastRedactions above — a degradation is re-reported on every turn it
	// still applies to, so re-deriving it per turn is always current rather
	// than a stale claim carried forward.
	lastDegraded []protocol.Degradation

	// lastProvider is the upstream provider the daemon reported serving the
	// current turn (see providerMsg). Unlike the notices above it arrives
	// mid-stream (at or before the first token), not before tokens, but shares
	// the same cleared-at-the-start-of-each-turn lifetime so a previous turn's
	// provider is never shown against the in-flight one. "" means none was
	// reported (the common case when OpenRouter omits the field) — rendered as
	// nothing, never as an error. Plain "served by X"; never a fallback or ZDR
	// claim (see providerLabel).
	lastProvider string

	// lastHistoryTruncated records whether the daemon dropped the oldest
	// conversation turns the client sent, by turn-count or byte cap (see
	// protocol.HistoryInfo.Truncated, daemon/history.go). Shown in the header for
	// the same reason grounding truncation is: a client that silently lost its
	// oldest turns would answer "what did I first ask?" confidently and wrongly.
	// Same cleared-at-the-start-of-each-turn lifetime as the notices above.
	lastHistoryTruncated bool

	// Edit-review state: set when the last completed answer contained
	// SEARCH/REPLACE edit blocks (see editapply.ParseEditBlocks). Reviewed
	// one block at a time — reviewIndex only ever points at a block that
	// PrepareEdit succeeded for; blocks that fail to prepare (not found /
	// ambiguous / outside workspace / secret / syntax-breaking) are
	// recorded into reviewRefusals and skipped past automatically, exactly
	// like the CLI's applyEditBlocks never prompts for a refused block.
	reviewBlocks    []editapply.EditBlock
	reviewIndex     int
	reviewPrepared  *editapply.PreparedEdit
	reviewApplied   int
	reviewSkipped   int
	reviewRefused   int
	reviewRefusals  []string
	reviewBackupDir string // lazily created on the first applied edit of a review

	// Tool-approval state: set while an agent turn is paused waiting for the
	// user to decide about one tool call (see toolApprovalMsg). pendingApproval
	// is nil at every other moment, and the reply channel is the daemon's turn
	// held open -- answering it is the only thing that lets the turn continue.
	pendingApproval *protocol.ToolApprovalRequest
	approvalReply   chan string

	// activityTurns maps a tool call's id to the transcript turn narrating it,
	// so a call's line is REWRITTEN from "running" to its outcome rather than
	// appended to. One call, one line: an agent turn that emitted four lines per
	// tool would bury the answer it produced.
	activityTurns map[string]int

	// streamAssistant is the index of the assistant turn the in-flight stream
	// is writing into, or -1 when none exists yet. Before agent mode the last
	// turn was ALWAYS the assistant's, so tokens could simply append to
	// turns[len-1]; a turn that interleaves tool-activity notices broke that
	// assumption, and appending an answer into a system line is how a
	// transcript starts lying about who said what.
	//
	// One assistant turn per stream, deliberately: buildHistory, the edit-block
	// parse, and lastAssistantText all still see exactly one assistant answer
	// per exchange, so an edit block emitted early in a multi-step turn cannot
	// go missing.
	autocompleteIdx int
	// autocompletePicked is set only when the user has moved through the popup
	// with the arrow keys. It is what separates Enter-submits from
	// Enter-accepts-a-completion: without it, Enter was a duplicate of Tab and
	// a slash command could never be run in one keystroke.
	autocompletePicked bool

	streamAssistant int

	clientName    string
	workspace     string // sent to the daemon so it can flag a workspace mismatch
	workspaceRoot string // real (symlink-resolved) workspace root edits are confined to
	// preferredTier is the models.json tier name chosen via /model <name>.
	// Empty means default routing. Sent as PromptRequest.Tier on every turn.
	preferredTier string
	width         int
	height        int
	ready         bool // true once the first WindowSizeMsg has sized the viewport
}

func newChatModel(clientName, workspace, workspaceRoot string, initialHistory []protocol.Turn) chatModel {
	ti := textinput.New()
	ti.Placeholder = "ask something…"
	ti.Prompt = "> "
	ti.PromptStyle = accentStyle
	ti.TextStyle = userStyle
	ti.CharLimit = 4000

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = accentStyle

	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true

	return chatModel{
		state:           stateSplash,
		streamAssistant: -1,
		input:           ti,
		spinner:         sp,
		viewport:        vp,
		clientName:      clientName,
		workspace:       workspace,
		workspaceRoot:   workspaceRoot,
		turns:           turnsFromProtocol(initialHistory),
	}
}

// turnsFromProtocol converts cross-session history hydrated at startup (see
// protocol.HandshakeResponse.PersistedHistory and runChat in main.go) into
// the TUI's own turn type, preserving order. The daemon has already
// re-validated these roles (daemon/memory.go's LoadRecentTurns re-runs the
// same injection defense prepareHistory applies on the wire), but this
// conversion still only ever maps exactly "user"/"assistant" rather than
// trusting the daemon blindly a second time.
func turnsFromProtocol(protoTurns []protocol.Turn) []turn {
	var turns []turn
	for _, t := range protoTurns {
		switch t.Role {
		case "user":
			turns = append(turns, turn{role: roleUser, text: t.Content})
		case "assistant":
			turns = append(turns, turn{role: roleAssistant, text: t.Content})
		}
	}
	return turns
}

func (m chatModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.viewport.Width = m.width
		m.resizeViewport()
		m.input.Width = m.width - len(m.input.Prompt) - 2
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		if m.state == stateSplash {
			m.state = stateIdle
			return m, m.input.Focus()
		}
		if m.state == stateEditReview {
			return m.handleReviewKey(msg)
		}
		if m.state == stateToolApproval {
			return m.handleApprovalKey(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.streamCancel != nil {
				m.streamCancel()
			}
			return m, tea.Quit
		case "enter":
			// Enter SUBMITS. It accepts a completion only when the user has
			// actively chosen one with the arrow keys, which is the convention
			// every editor popup follows.
			//
			// This used to be a byte-for-byte copy of the "tab" arm below, so
			// Enter on "/help" only rewrote the input to "/help " and ran
			// nothing. Every no-argument command -- /help, /clear, /git,
			// /init, /context, /exit -- needed two Enters, and the two tests
			// that assert otherwise were failing on main.
			if m.state == stateIdle && m.autocompletePicked {
				matches := m.slashMatches()
				if len(matches) > 0 {
					idx := m.autocompleteIdx
					if idx >= 0 && idx < len(matches) {
						m.input.SetValue("/" + matches[idx].Name + " ")
						m.input.SetCursor(len(m.input.Value()))
						m.autocompleteIdx = 0
						m.autocompletePicked = false
						m.resizeViewport()
						return m, nil
					}
				}
			}
			m.autocompletePicked = false
			return m.startTurn()
		case "tab":
			if m.state == stateIdle {
				matches := m.slashMatches()
				if len(matches) > 0 {
					idx := m.autocompleteIdx
					if idx >= 0 && idx < len(matches) {
						m.input.SetValue("/" + matches[idx].Name + " ")
						m.input.SetCursor(len(m.input.Value()))
						m.autocompleteIdx = 0
						m.resizeViewport()
						return m, nil
					}
				}
			}
		case "up", "down":
			if m.state == stateIdle {
				matches := m.slashMatches()
				if len(matches) > 0 {
					if msg.String() == "up" {
						m.autocompleteIdx--
						if m.autocompleteIdx < 0 {
							m.autocompleteIdx = len(matches) - 1
						}
					} else {
						m.autocompleteIdx++
						if m.autocompleteIdx >= len(matches) {
							m.autocompleteIdx = 0
						}
					}
					m.autocompletePicked = true
					return m, nil
				}
			}
		case "ctrl+n":
			return m.clearConversation()
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		}
		var cmd tea.Cmd
		oldPopupLines := 0
		if m.state == stateIdle {
			_, oldPopupLines = m.renderSlashPopup()
		}
		m.input, cmd = m.input.Update(msg)
		newPopupLines := 0
		if m.state == stateIdle {
			_, newPopupLines = m.renderSlashPopup()
		}
		if oldPopupLines != newPopupLines {
			m.resizeViewport()
		}
		if newPopupLines == 0 {
			m.autocompleteIdx = 0
			m.autocompletePicked = false
		}
		return m, cmd

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case groundingMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.lastGrounding = msg.info
		m.resizeViewport()
		return m, waitForNext(m.streamCh)

	case redactionsMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.lastRedactions = msg.kinds
		m.resizeViewport()
		return m, waitForNext(m.streamCh)

	case degradedMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.lastDegraded = msg.items
		m.resizeViewport()
		return m, waitForNext(m.streamCh)

	case providerMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.lastProvider = msg.provider
		m.resizeViewport()
		return m, waitForNext(m.streamCh)

	case reasoningMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		// Thinking arrives before (and among) the content tokens; start the
		// assistant turn on the first reasoning chunk if no token has yet, so the
		// dead air fills with visible thinking instead of a blank screen. Kept in
		// the turn's SEPARATE reasoning field, never appended to text.
		if m.state == stateSending {
			m.state = stateStreaming
		}
		m.turns[m.ensureAssistantTurn()].reasoning += msg.text
		m.refreshViewport()
		return m, waitForNext(m.streamCh)

	case historyMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.lastHistoryTruncated = msg.info != nil && msg.info.Truncated
		m.resizeViewport()
		return m, waitForNext(m.streamCh)

	case toolActivityMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		m.noteToolActivity(msg.activity)
		m.refreshViewport()
		return m, waitForNext(m.streamCh)

	case toolApprovalMsg:
		if m.streamCh == nil {
			// The stream was abandoned while the daemon was asking. Answer
			// anyway, on the safe side, so nothing is left waiting on a
			// question whose asker has gone.
			msg.reply <- protocol.ApprovalCancelTurn
			return m, nil
		}
		req := msg.req
		m.pendingApproval = &req
		m.approvalReply = msg.reply
		m.state = stateToolApproval
		m.refreshViewport()
		// Keep draining: the stream goroutine is blocked on the reply, so
		// nothing arrives until the user answers, and this wait is what picks up
		// the stream again when they do.
		return m, waitForNext(m.streamCh)

	case tokenMsg:
		return m.handleToken(msg)

	case incompleteMsg:
		if m.streamCh == nil {
			return m, nil // a stray message from an already-abandoned stream
		}
		// TWO RECORDS, ONE EVENT, because the screen and the model are
		// different audiences.
		//
		// For the model: the reason slug goes on the assistant turn itself, so
		// buildHistory can carry it into the next request. Without this the
		// notice below was the ONLY record, and buildHistory drops roleSystem
		// -- so the next turn re-showed the model its own truncated answer
		// with nothing to say it had been cut short, and it would build on a
		// conclusion it never actually reached.
		//
		// For the user: a persistent scrollback notice (TUI-only chrome)
		// appended right after the partial answer, not a header notice that
		// clears on the next turn.
		//
		// streamDoneMsg follows, so keep draining the channel.
		if msg.info != nil && m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) {
			m.turns[m.streamAssistant].incomplete = msg.info.Reason
		}
		m.turns = append(m.turns, turn{role: roleSystem, text: "⚠ answer cut off: " + incompleteText(msg.info)})
		m.refreshViewport()
		return m, waitForNext(m.streamCh)

	case streamDoneMsg:
		m.endStream()
		return m.checkForEditBlocks()

	case streamErrMsg:
		// THE SAME LOSS, THROUGH THE ERROR DOOR (register item L2's first half).
		// A stream that fails PARTWAY leaves whatever streamed sitting in
		// m.turns as an ordinary assistant turn, and buildHistory sends it on
		// the next prompt as a finished answer -- so the model is re-shown a
		// reply that stops mid-sentence with nothing to say it was interrupted.
		//
		// The daemon cannot mark this one: its error path sends Done+Error with
		// no Incomplete, and a transport drop has no daemon left to annotate it
		// (protocol.go says exactly this -- a connection drop "remains the
		// client's to distinguish"). This is the client doing that.
		//
		// Only when something actually streamed. An empty assistant turn is not
		// a partial answer, carries no risk of being read as one, and is dropped
		// by validTurn server-side anyway.
		if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) &&
			strings.TrimSpace(m.turns[m.streamAssistant].text) != "" {
			m.turns[m.streamAssistant].incomplete = protocol.IncompleteProviderError
		}
		m.state = stateError
		m.statusErr = msg.err.Error()
		m.endStream()
		return m, m.input.Focus()

	case resetErrMsg:
		// The live transcript was already cleared synchronously in
		// clearConversation; only the daemon-side half failed. Reported as
		// a transcript note rather than statusErr/stateError, since the
		// user's chat is not actually in an error state — they can keep
		// typing normally.
		m.turns = append(m.turns, turn{role: roleSystem, text: fmt.Sprintf("(local chat cleared, but clearing it on the daemon failed: %v)", msg.err)})
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		if m.state != stateSending && m.state != stateStreaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

// commandReason and commandRefactor are the exact, case-sensitive slash-
// command prefixes recognized in chat input -- deliberately including the
// trailing space. That space is load-bearing, not cosmetic: startTurn
// already runs strings.TrimSpace on the raw input before parsePromptKind
// ever sees it, so a bare "/reason" (with or without trailing whitespace,
// and nothing after it) can never retain a trailing space to match this
// prefix -- it falls straight through as ordinary, un-escalated prompt
// text. The same prefix check also means "/reasonfoo" (no space after the
// command word) and a bare "/" never match either. No separate "empty
// question" guard is needed; it falls out of prefix+trim for free.
const (
	commandReason   = "/reason "
	commandRefactor = "/refactor "
	commandModel    = "/model"
)

// promptKindReason and promptKindRefactor are the wire values sent as
// protocol.PromptRequest.PromptKind. They MUST match daemon/router.go's
// PromptKindReason/PromptKindRefactor exactly -- the two packages are
// separate Go modules with no shared importable constant for this, so the
// contract is enforced by convention and this comment, the same way
// protocol.Turn.Role's "user"/"assistant" contract is (see its doc
// comment), not by the type system.
const (
	promptKindReason   = "reason"
	promptKindRefactor = "refactor"
)

// parsePromptKind checks raw (already trimmed) for an exact recognized
// slash-command prefix. On a match, it returns the wire PromptKind value
// and the remainder with the command stripped and re-trimmed -- the user's
// real question, not the literal command glued to the front of it. On no
// match -- unrecognized "/word", a bare command with nothing after it, a
// bare "/", or plain text -- it returns "" and raw completely unchanged,
// so passthrough behavior is byte-identical to before this function
// existed.
func parsePromptKind(raw string) (kind, prompt string) {
	switch {
	case strings.HasPrefix(raw, commandReason):
		return promptKindReason, strings.TrimSpace(strings.TrimPrefix(raw, commandReason))
	case strings.HasPrefix(raw, commandRefactor):
		return promptKindRefactor, strings.TrimSpace(strings.TrimPrefix(raw, commandRefactor))
	default:
		return "", raw
	}
}

// parseModelCommand recognizes /model and /model <tier>. Returns ok=false
// when raw is not a model command (caller should treat it as a normal prompt).
func parseModelCommand(raw string) (arg string, ok bool) {
	if raw == commandModel {
		return "", true
	}
	if strings.HasPrefix(raw, commandModel+" ") {
		return strings.TrimSpace(strings.TrimPrefix(raw, commandModel+" ")), true
	}
	return "", false
}

// startTurn handles Enter while idle or errored: it's a no-op while busy or
// on empty input, otherwise it appends the user's turn and kicks off the
// stream.
func (m chatModel) startTurn() (tea.Model, tea.Cmd) {
	if m.state == stateSending || m.state == stateStreaming {
		return m, nil
	}
	raw := strings.TrimSpace(m.input.Value())
	if raw == "" {
		return m, nil
	}
	if arg, isModel := parseModelCommand(raw); isModel {
		m.input.SetValue("")
		return m.handleModelCommand(arg)
	}
	if sp := parseSlash(raw); !sp.RawPassthrough {
		m.input.SetValue("")
		return m.handleSlash(sp)
	}
	promptKind, prompt := parsePromptKind(raw)

	// Built from the transcript BEFORE the current prompt is appended below,
	// so the not-yet-answered prompt can never end up in its own History.
	history := buildHistory(m.turns)

	m.turns = append(m.turns, turn{role: roleUser, text: prompt})
	m.input.SetValue("")
	m.input.Blur()
	m.state = stateSending
	m.statusErr = ""
	m.lastGrounding = nil
	m.lastRedactions = nil
	m.lastDegraded = nil
	m.lastProvider = ""
	m.lastHistoryTruncated = false
	m.streamAssistant = -1
	m.activityTurns = nil
	m.resizeViewport()
	m.refreshViewport()

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	ch := make(chan tea.Msg)
	m.streamCh = ch

	return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, m.workspace, prompt, promptKind, m.preferredTier, history, ch))
}

func (m chatModel) handleSlash(sp slashParse) (tea.Model, tea.Cmd) {
	if sp.Def == nil {
		return m, nil
	}
	if sp.UsageOnly {
		msg := fmt.Sprintf("usage: /%s <args…>", sp.Def.Name)
		m.turns = append(m.turns, turn{role: roleAssistant, text: msg})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	switch sp.Def.Kind {
	case slashLocal:
		return m.handleLocalSlash(sp.Def.Name, sp.Args)
	case slashSteered:
		prompt := steeredPrompt(sp.Def, sp.Args)
		promptKind := sp.Def.PromptKind
		history := buildHistory(m.turns)
		m.turns = append(m.turns, turn{role: roleUser, text: "/" + sp.Def.Name + " " + sp.Args})
		m.input.Blur()
		m.state = stateSending
		m.statusErr = ""
		m.lastGrounding = nil
		m.lastRedactions = nil
		m.lastDegraded = nil
		m.lastProvider = ""
		m.lastHistoryTruncated = false
		m.streamAssistant = -1
		m.activityTurns = nil
		m.resizeViewport()
		m.refreshViewport()
		ctx, cancel := context.WithCancel(context.Background())
		m.streamCancel = cancel
		ch := make(chan tea.Msg)
		m.streamCh = ch
		return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, m.workspace, prompt, promptKind, m.preferredTier, history, ch))
	}
	return m, nil
}

func (m chatModel) handleLocalSlash(name, args string) (tea.Model, tea.Cmd) {
	var reply string
	switch name {
	case "help":
		reply = formatSlashHelp()
	case "clear":
		m.turns = nil
		m.lastGrounding = nil
		m.lastRedactions = nil
		m.lastDegraded = nil
		m.lastProvider = ""
		reply = "transcript cleared"
	case "compact":
		const keep = 8
		if len(m.turns) > keep {
			m.turns = append([]turn(nil), m.turns[len(m.turns)-keep:]...)
			reply = fmt.Sprintf("kept last %d turns", keep)
		} else {
			reply = "transcript already compact"
		}
	case "context":
		tier := m.preferredTier
		if tier == "" {
			tier = "(default)"
		}
		var g string
		if m.lastGrounding != nil {
			g = fmt.Sprintf("chunks=%d truncated=%v mismatch=%v",
				m.lastGrounding.Chunks, m.lastGrounding.Truncated, m.lastGrounding.WorkspaceMismatch)
		} else {
			g = "(none this turn)"
		}
		reply = fmt.Sprintf("workspace: %s\nmodel tier: %s\ngrounding: %s", m.workspace, tier, g)
	case "git":
		reply = runGitStatus(m.workspaceRoot)
		if reply == "(git status: empty)" || strings.HasPrefix(reply, "git status failed") {
			alt := runGitStatus(m.workspace)
			if alt != "" {
				reply = alt
			}
		}
	case "init":
		ws := m.workspace
		if ws == "" {
			ws = m.workspaceRoot
		}
		reply = formatInitChecklist(ws)
	case "mcp-server":
		// EMPTY, not "./models.json". That relative path resolved against the
		// TUI's working directory -- the repository the user opened -- and
		// `mcp list` does not merely READ a config: its own usage text says
		// "Starts the configured MCP servers to ask them", and buildRegistry
		// does. So a repository shipping a models.json with an mcp.servers
		// entry had its command RUN. CONFIRMED by execution 2026-08-08.
		//
		// acknowledged_unconfined, the gate documented as a written
		// acknowledgement a human types, is a field in that same
		// attacker-written file.
		//
		// Empty makes the daemon resolve its own config (resolveConfigPath:
		// <exedir>/models.json, then <exedir>/../models.json), which is the
		// installed one, or the repo's own when run from a checkout.
		reply = runMCPServerList("")
	case "search":
		reply = runSearch(m.clientName, m.workspaceRoot, args)
	case "exit":
		return m, tea.Quit
	default:
		reply = "unknown local command"
	}
	m.turns = append(m.turns, turn{role: roleAssistant, text: reply})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// handleModelCommand implements /model and /model <tier> without starting a
// model turn. Listing hits the daemon status surface so the menu always
// matches models.json.
func (m chatModel) handleModelCommand(arg string) (tea.Model, tea.Cmd) {
	if arg == "" || arg == "list" {
		tiers, err := fetchAvailableTiers(m.clientName)
		if err != nil {
			m.statusErr = "could not list models: " + err.Error()
			return m, nil
		}
		var b strings.Builder
		b.WriteString("models (active only — /model <name> to select; /model clear to reset):\n")
		current := m.preferredTier
		if current == "" {
			current = "(default)"
		}
		fmt.Fprintf(&b, "current: %s\n", current)
		for _, t := range tiers {
			if !t.Active {
				continue
			}
			mark := "  "
			if t.Name == m.preferredTier || (m.preferredTier == "" && t.Name == "primary") {
				mark = "* "
			}
			fmt.Fprintf(&b, "%s%s  %s\n", mark, t.Name, t.Slug)
		}
		m.turns = append(m.turns, turn{role: roleAssistant, text: strings.TrimRight(b.String(), "\n")})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	if arg == "clear" || arg == "default" {
		m.preferredTier = ""
		m.turns = append(m.turns, turn{role: roleAssistant, text: "model reset to default tier (models.json default_tier)"})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	tiers, err := fetchAvailableTiers(m.clientName)
	if err != nil {
		m.statusErr = "could not resolve model: " + err.Error()
		return m, nil
	}
	var found *protocol.StatusTier
	for i := range tiers {
		if tiers[i].Name == arg {
			found = &tiers[i]
			break
		}
	}
	if found == nil {
		m.turns = append(m.turns, turn{role: roleAssistant, text: fmt.Sprintf("unknown model tier %q — try /model for the list", arg)})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	if !found.Active {
		m.turns = append(m.turns, turn{role: roleAssistant, text: fmt.Sprintf("tier %q is inactive in models.json", arg)})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	m.preferredTier = found.Name
	m.turns = append(m.turns, turn{role: roleAssistant, text: fmt.Sprintf("model set to %s (%s)", found.Name, found.Slug)})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// endStream releases the finished turn's stream state.
//
// CALLING cancel IS THE POINT, not clearing the field (L1). context.WithCancel
// attaches the child context to its parent and to a goroutine that watches for
// completion; dropping the CancelFunc without calling it leaks both, once per
// turn, for the life of a session that may run for hours. Setting
// m.streamCancel = nil -- which is what the two terminal paths used to do --
// looks like cleanup and is the opposite of it: it discards the only handle
// that could ever have released the resources.
//
// Cancelling a context whose work has already finished is a no-op, which is why
// this is safe on the done path as well as the error one. Clearing the field
// afterwards still matters, so a later ctrl+c cannot fire cancel against a turn
// that is already over.
func (m *chatModel) endStream() {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamCh = nil
}

// buildHistory converts the transcript so far into the PromptRequest.History
// the next prompt will carry, oldest first. roleSystem turns (edit-review
// summaries, parse-error notices) are TUI-only chrome, not conversation
// content, so they're dropped here rather than sent — the daemon only
// accepts "user"/"assistant" roles anyway (daemon/history.go) and would
// drop anything else itself.
//
// That drop is why turn.incomplete exists. The "answer cut off" notice is one
// of those roleSystem turns, so for as long as it was the only record of the
// event, the fact died here: the model got the truncated text back as ordinary
// history and no indication it was truncated. The flag travels on the assistant
// turn instead, where it survives the filter.
//
// This does no capping of its own: Layer 1 already caps and logs truncation
// server-side (maxHistoryTurns in daemon/history.go), so a long session just
// sends its whole transcript and lets the daemon decide what fits.
func buildHistory(turns []turn) []protocol.Turn {
	var history []protocol.Turn
	for _, t := range turns {
		switch t.role {
		case roleUser:
			history = append(history, protocol.Turn{Role: "user", Content: t.text})
		case roleAssistant:
			// t.incomplete rides along: an answer that was cut off must reach
			// the model marked as cut off, or the next turn sees a truncated
			// reply as a finished one. The daemon renders the wording from
			// this slug (daemon/history.go) -- the client never sends prose.
			history = append(history, protocol.Turn{Role: "assistant", Content: t.text, Incomplete: t.incomplete})
		}
	}
	return history
}

// clearConversation handles ctrl+n: it starts a fresh conversation with no
// carried-over history. A no-op while a request/stream is in flight (mid-
// stream review is unreachable here — stateEditReview routes to
// handleReviewKey instead, which doesn't bind ctrl+n), matching the same
// ignore-while-busy rule Enter follows.
//
// The live transcript is cleared immediately, synchronously, for a snappy
// UI. Clearing the daemon's cross-session store (see PromptRequest.Reset)
// is a network round-trip, so it's fired as a background Cmd instead —
// Update must never block. A failure there doesn't undo the local clear;
// it's surfaced as a system-role note (see the resetErrMsg case in Update)
// rather than silently swallowed.
func (m chatModel) clearConversation() (tea.Model, tea.Cmd) {
	if m.state == stateSending || m.state == stateStreaming {
		return m, nil
	}
	m.turns = nil
	m.lastGrounding = nil
	m.lastRedactions = nil
	m.lastDegraded = nil
	m.lastProvider = ""
	m.lastHistoryTruncated = false
	m.statusErr = ""
	m.state = stateIdle
	m.resizeViewport()
	m.refreshViewport()

	ch := make(chan tea.Msg, 1)
	return m, startReset(context.Background(), m.clientName, ch)
}

// handleToken appends msg to the in-progress assistant turn (starting one
// on the first token of a response) and re-issues waitForNext so the next
// message on the same channel keeps arriving without Update ever blocking.
func (m chatModel) handleToken(msg tokenMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	if m.state == stateSending {
		m.state = stateStreaming
	}
	m.turns[m.ensureAssistantTurn()].text += string(msg)
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}

// ensureAssistantTurn returns the index of this stream's assistant turn,
// creating it if the stream has not spoken yet. Wherever the tool-activity
// notices happen to have landed, the answer keeps going to the same place.
func (m *chatModel) ensureAssistantTurn() int {
	if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) {
		return m.streamAssistant
	}
	m.streamAssistant = len(m.turns)
	m.turns = append(m.turns, turn{role: roleAssistant})
	return m.streamAssistant
}

// checkForEditBlocks runs once a stream finishes: it parses the just-
// completed assistant turn for SEARCH/REPLACE edit blocks (the same
// editapply.ParseEditBlocks the daemon already runs to log and, as of the
// EditProposals protocol field, surface to other clients — see
// parseAndLogEditBlocks in daemon/server.go). The TUI ignores that new
// field entirely and keeps parsing client-side, same as before. No blocks,
// or a parse error, means
// there's nothing to review: back to normal idle chat, unchanged from
// before this feature existed. Blocks found means entering the modal
// edit-review state instead.
func (m chatModel) checkForEditBlocks() (tea.Model, tea.Cmd) {
	m.state = stateIdle
	text := lastAssistantText(m.turns)
	if text == "" {
		return m, m.input.Focus()
	}

	blocks, rejected := editapply.ParseEditBlocks(text)
	// A refused block is shown as its own system turn and costs only itself
	// (Fix B): the readable blocks in the same response still go to review,
	// where before a single bad block sent the whole reply to this message and
	// nothing was offered.
	for _, bad := range rejected {
		m.turns = append(m.turns, turn{role: roleSystem, text: fmt.Sprintf("(edit block at line %d refused: %v)", bad.Line, bad.Reason)})
	}
	if len(blocks) == 0 {
		if len(rejected) > 0 {
			m.refreshViewport()
		}
		return m, m.input.Focus()
	}
	if len(rejected) > 0 {
		m.refreshViewport()
	}

	m.reviewBlocks = blocks
	m.reviewIndex = 0
	m.reviewApplied = 0
	m.reviewSkipped = 0
	m.reviewRefused = 0
	m.reviewRefusals = nil
	m.reviewBackupDir = ""
	m.state = stateEditReview
	return m.advanceReview()
}

func lastAssistantText(turns []turn) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == roleAssistant {
			return turns[i].text
		}
	}
	return ""
}

// advanceReview prepares the next not-yet-processed block (calling
// editapply.PrepareEdit, a bounded local file-read-plus-go/parser-parse —
// not a network wait, so doing it synchronously inside Update doesn't
// violate the never-block-Update rule the streaming design follows). Any
// block that fails to prepare is refused immediately, with no confirm
// prompt, exactly mirroring the CLI's applyEditBlocks. Once every block has
// been processed, it finishes the review.
func (m chatModel) advanceReview() (tea.Model, tea.Cmd) {
	for m.reviewIndex < len(m.reviewBlocks) {
		block := m.reviewBlocks[m.reviewIndex]
		prepared, err := editapply.PrepareEdit(m.workspaceRoot, block)
		if err != nil {
			m.reviewRefused++
			m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%s: %v", block.FilePath, err))
			m.reviewIndex++
			continue
		}
		m.reviewPrepared = prepared
		m.refreshViewport()
		return m, nil
	}
	return m.finishReview()
}

// handleReviewKey handles keypresses while m.state == stateEditReview. Only
// a literal 'y' applies — the same strict default-deny confirm philosophy
// as the CLI's `[y/N]` prompt. 'n' skips just the current block; 'q' cancels
// every remaining block (including the current one) as skipped.
func (m chatModel) handleReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "y":
		return m.applyCurrentReviewEdit()
	case "n":
		m.reviewSkipped++
		m.reviewIndex++
		return m.advanceReview()
	case "q":
		m.reviewSkipped += len(m.reviewBlocks) - m.reviewIndex
		m.reviewIndex = len(m.reviewBlocks)
		return m.finishReview()
	}
	return m, nil
}

// handleApprovalKey handles keypresses while an agent turn is paused on a tool
// call. Only a literal 'y' or 'a' approves -- the same strict default-deny
// philosophy as the edit review's `[y/N]` and the CLI's, and for a stronger
// reason: a Lane B tool is an unconfined subprocess, and consent is the only
// protection standing in front of it.
//
// EVERY OTHER KEY IS A NO-OP. Not a deny, not a dismiss: a stray keystroke from
// a user typing into what they thought was the input box must not answer a
// security question on their behalf in either direction.
func (m chatModel) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		return m.answerApproval(protocol.ApprovalApprove)
	case "a":
		return m.answerApproval(protocol.ApprovalApproveForTurn)
	case "n":
		return m.answerApproval(protocol.ApprovalDeny)
	case "q", "esc":
		return m.answerApproval(protocol.ApprovalCancelTurn)
	case "ctrl+c":
		// Stop the task first, so the daemon is not left holding a tool call
		// open while this process exits, then quit as ctrl+c does everywhere.
		model, _ := m.answerApproval(protocol.ApprovalCancelTurn)
		if m.streamCancel != nil {
			m.streamCancel()
		}
		return model, tea.Quit
	}
	return m, nil
}

// answerApproval sends one decision back to the waiting stream goroutine and
// returns the UI to streaming.
//
// The send is inline rather than wrapped in a tea.Cmd, which is safe for
// exactly one reason worth stating: approvalReply is buffered with capacity 1
// and receives exactly one send, so it cannot block Update. Anything less
// certain than that belongs in a Cmd.
func (m chatModel) answerApproval(decision string) (tea.Model, tea.Cmd) {
	if m.approvalReply == nil {
		return m, nil
	}
	m.approvalReply <- decision

	m.turns = append(m.turns, turn{role: roleSystem, text: approvalOutcomeLine(*m.pendingApproval, decision)})
	m.pendingApproval = nil
	m.approvalReply = nil
	m.state = stateStreaming
	m.refreshViewport()
	return m, nil
}

// approvalOutcomeLine is the permanent scrollback record of what the user
// decided. It stays in the transcript after the panel is gone, because "did I
// approve that?" is a question worth being able to answer by scrolling up.
func approvalOutcomeLine(req protocol.ToolApprovalRequest, decision string) string {
	name := req.Server + "__" + req.Tool
	switch decision {
	case protocol.ApprovalApprove:
		return "✓ approved " + name
	case protocol.ApprovalApproveForTurn:
		return "✓ approved " + name + " for the rest of this task"
	case protocol.ApprovalCancelTurn:
		return "✗ stopped the task at " + name
	default:
		return "✗ denied " + name
	}
}

// noteToolActivity records one step of an agent turn in the transcript,
// rewriting the call's existing line rather than adding another.
//
// The 'requested' and 'approved' phases are deliberately not rendered: the user
// has just answered a modal about that exact call, and narrating it back to
// them is noise. What earns a line is the call actually running, and how it
// ended.
func (m *chatModel) noteToolActivity(a protocol.ToolActivity) {
	line := toolActivityLine(a)
	if line == "" {
		return
	}
	if m.activityTurns == nil {
		m.activityTurns = map[string]int{}
	}
	if idx, ok := m.activityTurns[a.CallID]; ok && idx < len(m.turns) {
		m.turns[idx].text = line
		return
	}
	m.activityTurns[a.CallID] = len(m.turns)
	m.turns = append(m.turns, turn{role: roleSystem, text: line})
}

func toolActivityLine(a protocol.ToolActivity) string {
	name := a.Server + "__" + a.Tool
	switch a.Phase {
	case protocol.ToolPhaseRunning:
		return "⚙ running " + name + "…"
	case protocol.ToolPhaseSucceeded:
		// ResultBytes is what actually went back to the model after scrubbing
		// and truncation -- in agent mode that is the quantity leaving the
		// machine, and a privacy-positioned product should let a user watch it.
		return fmt.Sprintf("⚙ %s — %d bytes to the model in %dms", name, a.ResultBytes, a.DurationMS)
	case protocol.ToolPhaseFailed:
		return "⚠ " + name + " failed" + detailSuffix(a.Detail)
	case protocol.ToolPhaseDenied:
		return "✗ " + name + " not run" + detailSuffix(a.Detail)
	}
	return ""
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

// applyCurrentReviewEdit backs up and writes m.reviewPrepared using the same
// editapply backup+write calls the CLI's applyEditBlocks uses — the same
// .codeterminal/backups/<session>/{before,after}/ layout, restorable via
// the CLI's `edits undo`. A backup or write failure is treated as a refusal
// for that block (recorded with its reason) rather than aborting the whole
// review, since later blocks may still be perfectly applicable.
func (m chatModel) applyCurrentReviewEdit() (tea.Model, tea.Cmd) {
	p := m.reviewPrepared
	if m.reviewBackupDir == "" {
		dir, err := editapply.NewBackupSessionDir(m.workspaceRoot)
		if err != nil {
			m.reviewRefused++
			m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%s: creating backup dir: %v", p.Block.FilePath, err))
			m.reviewIndex++
			return m.advanceReview()
		}
		m.reviewBackupDir = dir
	}

	if err := editapply.Apply(m.workspaceRoot, p, m.reviewBackupDir); err != nil {
		m.reviewRefused++
		m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%v", err))
		m.reviewIndex++
		return m.advanceReview()
	}

	m.reviewApplied++
	m.reviewIndex++
	return m.advanceReview()
}

// finishReview appends an outcome summary to the transcript and returns to
// idle chat.
func (m chatModel) finishReview() (tea.Model, tea.Cmd) {
	lines := []string{fmt.Sprintf("edits: %d applied, %d skipped, %d refused", m.reviewApplied, m.reviewSkipped, m.reviewRefused)}
	for _, r := range m.reviewRefusals {
		lines = append(lines, "  refused: "+r)
	}
	if m.reviewApplied > 0 {
		lines = append(lines, fmt.Sprintf("  backups: %s (restore with: edits undo)", m.reviewBackupDir))
	}
	m.turns = append(m.turns, turn{role: roleSystem, text: strings.Join(lines, "\n")})

	m.reviewBlocks = nil
	m.reviewPrepared = nil
	m.reviewIndex = 0
	m.state = stateIdle
	m.refreshViewport()
	return m, m.input.Focus()
}

func (m *chatModel) refreshViewport() {
	content := renderTranscript(m.turns)
	if m.state == stateEditReview && m.reviewPrepared != nil {
		content += "\n\n" + renderReviewPanel(m.reviewIndex, len(m.reviewBlocks), m.reviewPrepared)
	}
	if m.state == stateToolApproval && m.pendingApproval != nil {
		content += "\n\n" + renderApprovalPanel(*m.pendingApproval)
	}
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

func renderTranscript(turns []turn) string {
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch t.role {
		case roleUser:
			b.WriteString(userStyle.Render("You: " + t.text))
		case roleAssistant:
			// Thinking (if any) renders first, dimmed and labelled, so it is
			// visibly SEPARATE from the answer and never mistaken for it -- the
			// answer text is what carries back as history and gets parsed for edit
			// blocks; the reasoning never does.
			if t.reasoning != "" {
				b.WriteString(helpStyle.Render("💭 thinking: " + t.reasoning))
				b.WriteString("\n")
			}
			b.WriteString(assistantStyle.Render("Mochiii: " + t.text))
		case roleSystem:
			b.WriteString(helpStyle.Render(t.text))
		}
	}
	return b.String()
}

// renderReviewPanel shows one edit block as a diff: the whole SEARCH block
// as removed lines, the whole REPLACE block as added lines, plus the
// syntax-check note — mirroring the CLI's printEditDiff (daemon/apply_cmd.go)
// so both surfaces present the same information about the same edit.
func renderReviewPanel(index, total int, p *editapply.PreparedEdit) string {
	var b strings.Builder
	b.WriteString(brandStyle.Render(fmt.Sprintf("--- edit %d/%d: %s (lines %d-%d) ---", index+1, total, p.Block.FilePath, p.StartLine, p.EndLine)))
	for _, l := range strings.Split(p.Block.Search, "\n") {
		b.WriteString("\n" + diffRemovedStyle.Render("- "+l))
	}
	for _, l := range strings.Split(p.Block.Replace, "\n") {
		b.WriteString("\n" + diffAddedStyle.Render("+ "+l))
	}
	// Surfaced next to the diff for the same reason the CLI does it: the user is
	// about to approve this write, and a match that needed whitespace/encoding
	// tolerance is something they should see before they do.
	if p.MatchNote != "" {
		b.WriteString("\n" + helpStyle.Render("match: "+p.MatchNote))
	}
	b.WriteString("\n" + helpStyle.Render("syntax check: "+p.SyntaxNote))
	return b.String()
}

// renderApprovalPanel shows one pending tool call the way the review panel
// shows one pending edit: everything the decision depends on, in front of the
// user, before they make it.
//
// THE ARGUMENTS ARE SHOWN IN FULL, never summarised. A consent prompt that
// displays less than what will run is not consent, and the daemon binds its
// approval to a digest of exactly these bytes.
//
// The confinement line is the one that must never be softened. For a
// third-party server it says plainly that nothing here can constrain what the
// tool touches -- that is the honest description of an ordinary subprocess
// running with the user's own privileges, and dressing it up as "sandboxed"
// would be the single most damaging sentence this product could print.
func renderApprovalPanel(req protocol.ToolApprovalRequest) string {
	var b strings.Builder
	b.WriteString(brandStyle.Render(fmt.Sprintf("--- run %s__%s? (step %d of at most %d) ---",
		req.Server, req.Tool, req.Iteration, req.MaxIterations)))

	b.WriteString("\n" + helpStyle.Render("arguments:"))
	for _, line := range strings.Split(req.Arguments, "\n") {
		b.WriteString("\n" + diffAddedStyle.Render("  "+line))
	}

	if req.Confined {
		b.WriteString("\n" + helpStyle.Render("this tool ships with Mochiii; anything it changes goes through the same review you use for edits"))
	} else {
		b.WriteString("\n" + diffRemovedStyle.Render("NOT SANDBOXED: this is a separate program running with your full access. "+
			"Mochiii cannot limit what it reads or changes — your approval is the only thing in its way."))
	}
	if req.Destructive {
		b.WriteString("\n" + diffRemovedStyle.Render("the server describes this tool as destructive"))
	}
	if req.Detail != "" {
		b.WriteString("\n" + helpStyle.Render(req.Detail))
	}
	return b.String()
}

func (m chatModel) View() string {
	if m.state == stateSplash {
		content := renderSplash()
		if m.ready {
			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
		}
		return content
	}
	if !m.ready {
		return "booting…"
	}

	header := m.renderHeader()

	var bottomLine, help string
	if m.state == stateEditReview && m.reviewPrepared != nil {
		bottomLine = accentStyle.Render(fmt.Sprintf("edit %d/%d: %s", m.reviewIndex+1, len(m.reviewBlocks), m.reviewPrepared.Block.FilePath))
		help = helpStyle.Render(reviewHelpText)
	} else if m.state == stateToolApproval && m.pendingApproval != nil {
		bottomLine = accentStyle.Render(fmt.Sprintf("approve %s__%s?", m.pendingApproval.Server, m.pendingApproval.Tool))
		help = helpStyle.Render(approvalHelpText)
	} else {
		popupStr, _ := m.renderSlashPopup()
		if popupStr != "" {
			bottomLine = popupStr + "\n" + m.input.View()
		} else {
			bottomLine = m.input.View()
		}
		help = helpStyle.Render(helpText)
	}

	return header + "\n" + m.viewport.View() + "\n" + bottomLine + "\n" + help
}

func (m chatModel) slashMatches() []slashDef {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") || strings.Contains(val, " ") {
		return nil
	}
	prefix := strings.ToLower(val[1:])
	var matches []slashDef
	for _, d := range slashCatalog {
		if strings.HasPrefix(d.Name, prefix) {
			matches = append(matches, d)
		}
	}
	if strings.HasPrefix("model", prefix) && prefix != "model" {
		matches = append(matches, slashDef{Name: "model", Summary: "list or select a models.json tier (/model <name>)"})
	}
	return matches
}

var popupStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240")).
	Padding(0, 1)

var selectedItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
var unselectedItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

func (m chatModel) renderSlashPopup() (string, int) {
	matches := m.slashMatches()
	if len(matches) == 0 {
		return "", 0
	}
	idx := m.autocompleteIdx
	if idx < 0 {
		idx = 0
	}
	if idx >= len(matches) {
		idx = len(matches) - 1
	}

	var lines []string
	for i, match := range matches {
		name := "/" + match.Name
		sum := match.Summary
		if len(sum) > 50 {
			sum = sum[:47] + "..."
		}
		row := fmt.Sprintf("%-15s %s", name, sum)
		if i == idx {
			lines = append(lines, selectedItemStyle.Render("> "+row))
		} else {
			lines = append(lines, unselectedItemStyle.Render("  "+row))
		}
	}

	box := popupStyle.Render(strings.Join(lines, "\n"))
	return box, len(lines) + 2
}

// renderHeader renders the brand/state/history line, followed by zero or
// more notice lines (see noticeLines) -- one per line, never joined onto
// the same line as each other or as the brand line. That separation is
// deliberate: grounding and redactions used to be joined into ONE line via
// strings.Join, which meant a sufficiently long combination (e.g. a
// workspace-mismatch warning plus a redaction notice, both active on the
// same turn) could exceed the terminal's width and soft-wrap -- silently
// desyncing the fixed 1-row header assumption the viewport's height was
// computed from, which pushed content off-screen and could hide a notice
// entirely with no visible sign anything was wrong. See headerLineCount /
// resizeViewport, which this must always stay consistent with: every line
// this returns must be counted there, or the same class of bug recurs.
func (m chatModel) renderHeader() string {
	brand := brandStyle.Render(lotusGlyph + " " + brandName)
	parts := []string{brand, m.stateLabel()}
	if history := m.historyLabel(); history != "" {
		parts = append(parts, history)
	}
	lines := append([]string{strings.Join(parts, "  ")}, m.noticeLines()...)
	return strings.Join(lines, "\n")
}

// noticeLines returns grounding/redactions each on their own line, so two
// concurrently active notices always get their own guaranteed row instead
// of competing for space on one shared line (see renderHeader's doc
// comment for the bug this fixes). Each line is truncated (ANSI- and
// wide-rune-aware, via charmbracelet/x/ansi) to m.width with a trailing
// "…" if it would otherwise overflow the terminal -- truncation, not
// wrapping, so every notice always occupies EXACTLY one terminal row and
// headerLineCount's arithmetic never has to guess how many rows a wrapped
// line actually consumed.
func (m chatModel) noticeLines() []string {
	var lines []string
	if grounding := m.groundingLabel(); grounding != "" {
		lines = append(lines, truncateToWidth(grounding, m.width))
	}
	if history := m.historyTruncatedLabel(); history != "" {
		lines = append(lines, truncateToWidth(history, m.width))
	}
	if redactions := m.redactionsLabel(); redactions != "" {
		lines = append(lines, truncateToWidth(redactions, m.width))
	}
	if provider := m.providerLabel(); provider != "" {
		lines = append(lines, truncateToWidth(provider, m.width))
	}
	for _, degraded := range m.degradedLabels() {
		lines = append(lines, truncateToWidth(degraded, m.width))
	}
	return lines
}

// truncateToWidth truncates s (which may already carry ANSI styling, e.g.
// from errorStyle.Render) to at most width terminal cells, adding a "…"
// tail when it's cut. width <= 0 means the terminal size isn't known yet
// (before the first WindowSizeMsg) -- returned unchanged rather than
// truncated to nothing.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// headerLineCount is exactly how many terminal rows renderHeader's output
// occupies: the brand/state/history line, plus one more for each currently
// active notice (see noticeLines). resizeViewport MUST use this, not a
// hardcoded constant, so the viewport's height always accounts for
// whatever the header is actually rendering right now.
func (m chatModel) headerLineCount() int {
	return 1 + len(m.noticeLines())
}

// resizeViewport recomputes the viewport's height from the current
// terminal size and the header's CURRENT line count (see headerLineCount).
// Called on every terminal resize and every event that can change which
// notices are active (a new groundingMsg/redactionsMsg arriving, or either
// being cleared at the start of a turn / on ctrl+n) -- unlike the header's
// old single hardcoded headerLines=1, this stays correct no matter how many
// notice lines are currently showing, which is what the multi-notice bug
// above actually was: a stale row-count assumption, not a rendering typo.
func (m *chatModel) resizeViewport() {
	if !m.ready {
		return // terminal size not known yet; the first WindowSizeMsg sizes everything
	}
	const inputLines, helpLines = 1, 1
	vpHeight := m.height - m.headerLineCount() - inputLines - helpLines
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.viewport.Height = vpHeight
}

// historyLabel is a subtle indicator of how much conversation memory is
// active — how many turns the NEXT prompt will carry as History. Empty
// right after startup or a ctrl+n clear, so nothing is shown until there's
// actually something to carry.
func (m chatModel) historyLabel() string {
	n := len(buildHistory(m.turns))
	if n == 0 {
		return ""
	}
	return helpStyle.Render(fmt.Sprintf("mem: %d turn(s)", n))
}

// groundingLabel renders the most recently reported GroundingInfo, or ""
// before the first one has arrived (nothing is shown rather than guessing).
func (m chatModel) groundingLabel() string {
	g := m.lastGrounding
	if g == nil {
		return ""
	}
	if g.WorkspaceMismatch {
		return errorStyle.Render(fmt.Sprintf("⚠ grounded against %s, not %s", g.Workspace, m.workspace))
	}
	if g.Grounded {
		label := fmt.Sprintf("grounded ✓ %d chunk(s)", g.Chunks)
		if g.Truncated {
			// The retrieved context didn't all fit the budget and was trimmed --
			// the answer was written against a partial view, worth knowing.
			label += " (context truncated)"
		}
		return accentStyle.Render(label)
	}
	return helpStyle.Render(fmt.Sprintf("ungrounded (%s)", g.Reason))
}

// historyTruncatedLabel warns, on its own header line, when the daemon dropped
// the oldest conversation turns the client sent (see lastHistoryTruncated /
// protocol.HistoryInfo.Truncated). Empty when nothing was dropped (the common
// case), so it only ever appears on a turn where it actually happened. Uses the
// same "⚠ + red" errorStyle the other non-fatal-but-worth-noticing notices use.
func (m chatModel) historyTruncatedLabel() string {
	if !m.lastHistoryTruncated {
		return ""
	}
	return errorStyle.Render("⚠ older conversation history was dropped to fit the model's limit")
}

// redactionsLabel renders the most recently reported redaction kinds, or ""
// when nothing has been redacted this turn (the common case — nothing is
// shown rather than a persistent, noisy indicator). Uses errorStyle, the
// same "⚠ + red" treatment groundingLabel already uses for its own
// non-fatal-but-worth-noticing case (WorkspaceMismatch above) — marked and
// legible, not an alarm banner. Kinds only, never a matched value (see
// protocol.TokenResponse.Redactions).
func (m chatModel) redactionsLabel() string {
	if len(m.lastRedactions) == 0 {
		return ""
	}
	return errorStyle.Render(fmt.Sprintf("⚠ redacted %d suspected secret(s) before sending: %s", len(m.lastRedactions), strings.Join(m.lastRedactions, ", ")))
}

// incompleteText renders the daemon's cut-off explanation for the transcript
// notice (see protocol.IncompleteInfo). Prefers the daemon's client-safe Detail
// prose; falls back to the machine-readable Reason, then a bare generic, so the
// notice is never empty even if a future daemon sends a reason with no detail.
func incompleteText(info *protocol.IncompleteInfo) string {
	if info == nil {
		return "the answer may be incomplete"
	}
	if info.Detail != "" {
		return info.Detail
	}
	if info.Reason != "" {
		return "the model stopped early (" + info.Reason + ")"
	}
	return "the answer may be incomplete"
}

// providerLabel renders the upstream provider the daemon reported serving this
// turn, or "" when none was reported (the common case when OpenRouter omits the
// field, or before it has arrived this turn — nothing is shown rather than a
// placeholder). Deliberately NEUTRAL styling (helpStyle, the same subtle
// treatment historyLabel uses), NOT the "⚠ + red" errorStyle the degraded /
// redaction notices use: a served-by-X report is a plain fact, not a warning,
// and absence is not a fault. The label is strictly factual — "served by X" —
// and says nothing about whether X is a fallback or about X's ZDR status; both
// are unshowable from this data (see providerMsg and the D4 correction).
func (m chatModel) providerLabel() string {
	if m.lastProvider == "" {
		return ""
	}
	return helpStyle.Render("served by: " + m.lastProvider)
}

// degradedLabels renders one line per reduced subsystem the daemon reported,
// or nil when nothing is degraded (the common case — no persistent indicator
// is shown for a healthy daemon).
//
// One line each, rather than one joined line, for the reason renderHeader's
// doc comment records: a joined line can exceed the terminal width and
// soft-wrap, desyncing the row count the viewport height is computed from and
// silently hiding a notice. A degradation notice that can hide itself would
// defeat its own purpose.
//
// errorStyle, matching the "⚠ + red" treatment groundingLabel already gives
// WorkspaceMismatch and redactionsLabel gives its notice: marked and legible,
// not an alarm banner. The daemon's Detail text is rendered as sent — it is
// already written for a user and already free of paths and hosts, and
// paraphrasing it here would let the client's wording drift from the daemon's.
func (m chatModel) degradedLabels() []string {
	var lines []string
	for _, d := range m.lastDegraded {
		lines = append(lines, errorStyle.Render("⚠ degraded ("+d.Component+"): "+d.Detail))
	}
	return lines
}

func (m chatModel) stateLabel() string {
	switch m.state {
	case stateSending:
		return accentStyle.Render(m.spinner.View() + " sending…")
	case stateStreaming:
		return accentStyle.Render(m.spinner.View() + " streaming…")
	case stateError:
		return errorStyle.Render("error: " + m.statusErr)
	case stateEditReview:
		return accentStyle.Render("reviewing edits…")
	case stateToolApproval:
		return accentStyle.Render("waiting for your approval…")
	default:
		return helpStyle.Render("idle")
	}
}
