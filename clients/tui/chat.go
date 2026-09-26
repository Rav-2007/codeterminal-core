package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/editapply"
	"mochiii/protocol"
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
	stateConnect                       // entering a provider API key, masked; see connect.go
)

type turnRole int

const (
	roleUser turnRole = iota
	roleAssistant
	roleSystem // parse-error notices and end-of-review summaries
	// roleSevered marks a prompt that never reached the daemon at all.
	//
	// IT EXISTS BECAUSE THE TRANSCRIPT USED TO SAY NOTHING. A failed connection
	// set m.statusErr -- one line, in the header, overwritten by the next
	// failure with identical text. Five prompts in a row against a stopped
	// daemon therefore produced five "You: ..." entries with no reply under any
	// of them and one header line that looked stale rather than fresh, which
	// reads exactly like a model ignoring you. Observed live.
	//
	// Distinct from roleSystem because the two are not the same event and must
	// not look alike: a roleSystem notice annotates something that HAPPENED,
	// this records something that did NOT. Like roleSystem it is display-only
	// and buildHistory drops it -- a message that never left must never reach
	// the model as though it had.
	roleSevered
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
// helpText names /mouse because the thing it toggles is invisible until it
// bites: with capture on, dragging to select text does nothing and there is no
// error to search for.
const helpText = "enter to send · ctrl+n new conversation · /mouse to select text · ctrl+c to quit"

// reviewHelpText is shown instead of helpText while reviewing edit blocks.
const reviewHelpText = "y apply · n skip · q cancel remaining"

// quitConfirmHelpText replaces the footer once ctrl+c has been pressed with
// nothing to stop. See handleCtrlC for why quitting asks first.
const quitConfirmHelpText = "press ctrl+c again to quit · any other key cancels"

// interruptHelpText replaces helpText while a turn is in flight. The hint
// changes because the available action does: an interrupt nobody can find is
// the same as not having one, and this is the moment the user is looking at
// the footer wondering how to make it stop.
const interruptHelpText = "esc or ctrl+c to stop this turn"

// approvalHelpText is shown while an agent turn is paused on a tool call.
// Worded in the same shape as reviewHelpText because it is the same kind of
// moment: the product has stopped and is waiting for a person to decide.
const approvalHelpText = "y run once · a allow this tool for the turn · n deny · q stop the task"

// chatModel is the Bubble Tea model for Mochiii's interactive chat.
type chatModel struct {
	state chatState
	turns []turn

	// transcript caches the rendered form of each turn so a streamed token
	// re-renders one turn instead of the whole conversation. It validates each
	// block against the turn it claims to render rather than relying on
	// invalidation hooks -- see rendercache.go for why that distinction is the
	// whole design.
	transcript transcriptCache

	// refreshPending and refreshScheduled coalesce repaints while a stream is
	// running: see refreshSoon.
	refreshPending   bool
	refreshScheduled bool

	// mouseCaptured mirrors whether the terminal is reporting mouse events to
	// us rather than handling selection itself. See handleMouseToggle.
	mouseCaptured bool

	// needsAPIKey is the daemon's answer to "would a prompt sent now have no
	// credential?", taken from the handshake (protocol.HandshakeResponse.
	// NeedsAPIKey). It gates the FIRST submitted question rather than startup:
	// see beginConnectForPrompt in connect.go for why that is the right moment.
	needsAPIKey bool

	// pendingPrompt holds a question that was typed before a key existed, so the
	// user types it once. Empty except while the key prompt is open on its behalf.
	pendingPrompt string

	// limits bounds the transcript; evictedTurns and evictedBytes are the
	// running totals the eviction marker reports. See transcriptbound.go.
	limits       transcriptLimits
	evictedTurns int
	evictedBytes int

	// sanAnswer and sanReasoning strip terminal escapes from the two streams
	// the daemon sends (see sanitize.go). They are stateful, so they live here
	// rather than being created per token: a sequence split across two tokens
	// is the bypass they exist to close.
	//
	// TWO PARSERS, NOT ONE, because answer text and reasoning are separate
	// streams interleaved on a single channel. Sharing one would let a
	// half-finished sequence in the answer be completed by the next reasoning
	// chunk -- a bypass built out of our own multiplexing. Both are released
	// by endStream.
	sanAnswer    escSanitizer
	sanReasoning escSanitizer

	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	statusErr string

	streamCh     chan tea.Msg       // the active stream's channel; nil when idle
	streamCancel context.CancelFunc // cancels the in-flight request; nil when idle

	// quitArmed is set by a ctrl+c that had nothing to stop, and cleared by any
	// other key. Only a ctrl+c pressed while it is set actually quits. See
	// handleCtrlC.
	quitArmed bool

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
	// daemonProposals holds what the DAEMON parsed, when it sent any
	// (protocol.TokenResponse.EditProposals). It is preferred over a local
	// re-parse because the daemon's list is strictly larger: it merges the
	// blocks in the assistant text with the edits the model filed through the
	// propose_edit tool, and only the daemon can see the second kind.
	// gotDaemonProposals distinguishes "the daemon sent an empty list" from
	// "the daemon never sent the field", which is the older-daemon case the
	// local fallback exists for.
	daemonProposals    []protocol.EditBlockWire
	gotDaemonProposals bool

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
	ti.Placeholder = defaultInputPlaceholder
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
		mouseCaptured:   true, // main.go starts the program with WithMouseCellMotion
		limits:          loadTranscriptLimits(),
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
//
// The text is sanitized for the same reason (see sanitize.go): these are model
// words that were written to disk by a previous session and read back, so they
// are untrusted twice over, and hydration puts them on screen before the user
// has typed anything at all.
func turnsFromProtocol(protoTurns []protocol.Turn) []turn {
	var turns []turn
	for _, t := range protoTurns {
		switch t.Role {
		case "user":
			turns = append(turns, turn{role: roleUser, text: sanitizeText(t.Content)})
		case "assistant":
			turns = append(turns, turn{role: roleAssistant, text: sanitizeText(t.Content)})
		}
	}
	return turns
}

// inputWidthFor sizes the prompt box for a terminal of the given width.
//
// IT MUST NEVER RETURN A NEGATIVE NUMBER, and that is a crash rather than a
// cosmetic rule. bubbles/textinput.placeholderView allocates `make([]rune,
// m.Width+1)` with no guard of its own, so a Width of -2 panics the whole
// client with "makeslice: len out of range" -- not a mis-drawn line, a stack
// trace where the UI was.
//
// MEASURED, by running the client under a pty with no window size: bubbletea
// delivers WindowSizeMsg{0, 0}, the old expression `m.width - len(Prompt) - 2`
// gave -4, and the client died on its first render after the splash. A terminal
// narrower than the prompt plus its padding reaches the same place, and so does
// any host that cannot report a size.
//
// Zero rather than one: textinput treats Width 0 as "unbounded", which for a
// terminal this small is the least wrong of the available behaviours -- the
// line will not fit whatever we pass, and a client that draws badly is worth
// more than one that is not there.
func inputWidthFor(terminalWidth int, prompt string) int {
	w := terminalWidth - len(prompt) - 2
	if w < 0 {
		return 0
	}
	return w
}

func (m chatModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case groundingMsg:
		return m.handleGrounding(msg)

	case redactionsMsg:
		return m.handleRedactions(msg)

	case degradedMsg:
		return m.handleDegraded(msg)

	case providerMsg:
		return m.handleProvider(msg)

	case reasoningMsg:
		return m.handleReasoning(msg)

	case historyMsg:
		return m.handleHistory(msg)

	case toolActivityMsg:
		return m.handleToolActivity(msg)

	case connectResultMsg:
		return m.handleConnectResult(msg)

	case toolApprovalMsg:
		return m.handleToolApproval(msg)

	case tokenMsg:
		return m.handleToken(msg)

	case refreshTickMsg:
		return m.handleRefreshTick()

	case incompleteMsg:
		return m.handleIncomplete(msg)

	case editProposalsMsg:
		return m.handleEditProposals(msg)

	case streamDoneMsg:
		return m.handleStreamDone()

	case streamErrMsg:
		return m.handleStreamErr(msg)

	case resetErrMsg:
		return m.handleResetErr(msg)

	case spinner.TickMsg:
		return m.handleSpinnerTick(msg)
	}

	return m, nil
}

func (m chatModel) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	m.ready = true
	m.viewport.Width = m.width
	m.resizeViewport()
	m.input.Width = inputWidthFor(m.width, m.input.Prompt)
	m.refreshViewport()
	return m, nil
}

func (m chatModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// FIRST, BEFORE ANYTHING READS THE KEY. Every branch below reaches for
	// msg.String(), and for a rune burst that builds a string as long as
	// the burst -- so a megabyte paste was stringified twice before it got
	// anywhere near the input. Bounding it after that point measured no
	// improvement at all, which is how the real cost was found.
	msg = m.boundPaste(msg)

	if m.state == stateSplash {
		m.state = stateIdle
		return m, m.input.Focus()
	}
	// ctrl+c is answered in ONE place for every state, above the per-state
	// handlers, so the key cannot mean "quit" in one corner of the UI and
	// "stop" in another. That split is what made it feel like a trapdoor.
	if msg.String() == "ctrl+c" {
		return m.handleCtrlC()
	}
	m.quitArmed = false
	if m.state == stateEditReview {
		return m.handleReviewKey(msg)
	}
	if m.state == stateToolApproval {
		return m.handleApprovalKey(msg)
	}
	// Before every ordinary key path: what is being typed is a credential, and
	// falling through to the prompt path would send it to the model as a question.
	if m.state == stateConnect {
		return m.handleConnectKey(msg)
	}
	switch msg.String() {
	case "esc":
		// ESC NEVER QUITS THE PROGRAM. It means "stop what is happening
		// now", and that is the only reading of it that is safe to press by
		// reflex: a user who hits esc a beat after the answer finished must
		// not lose their session for it. With nothing in flight it does
		// nothing at all.
		return m.interruptTurn()
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
}

func (m chatModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m chatModel) handleGrounding(msg groundingMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.lastGrounding = msg.info
	m.resizeViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleRedactions(msg redactionsMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.lastRedactions = sanitizeAll(msg.kinds)
	m.resizeViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleDegraded(msg degradedMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.lastDegraded = sanitizeDegradations(msg.items)
	m.resizeViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleProvider(msg providerMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.lastProvider = sanitizeText(msg.provider)
	m.resizeViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleReasoning(msg reasoningMsg) (tea.Model, tea.Cmd) {
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
	m.turns[m.ensureAssistantTurn()].reasoning += m.sanReasoning.Write(msg.text)
	return m, tea.Batch(m.refreshSoon(), waitForNext(m.streamCh))
}

func (m chatModel) handleHistory(msg historyMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.lastHistoryTruncated = msg.info != nil && msg.info.Truncated
	m.resizeViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleToolActivity(msg toolActivityMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	m.noteToolActivity(msg.activity)
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleToolApproval(msg toolApprovalMsg) (tea.Model, tea.Cmd) {
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
}

func (m chatModel) handleRefreshTick() (tea.Model, tea.Cmd) {
	// One repaint, if anything asked for one. Nothing re-arms the tick
	// here: refreshSoon does that when the next token arrives, so a stream
	// that has gone quiet stops ticking instead of waking the process
	// sixty times a second to do nothing.
	m.refreshScheduled = false
	if m.refreshPending {
		m.refreshViewport()
	}
	return m, nil
}

func (m chatModel) handleIncomplete(msg incompleteMsg) (tea.Model, tea.Cmd) {
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
	m.appendTurn(turn{role: roleSystem, text: "⚠ answer cut off: " + incompleteText(msg.info)})
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleEditProposals(msg editProposalsMsg) (tea.Model, tea.Cmd) {
	if m.streamCh == nil {
		return m, nil // a stray message from an already-abandoned stream
	}
	// Recorded, not acted on: streamDoneMsg follows immediately and is what
	// starts the review. Keep draining.
	m.daemonProposals = msg.blocks
	m.gotDaemonProposals = true
	return m, waitForNext(m.streamCh)
}

func (m chatModel) handleStreamDone() (tea.Model, tea.Cmd) {
	m.endStream()
	return m.checkForEditBlocks()
}

func (m chatModel) handleStreamErr(msg streamErrMsg) (tea.Model, tea.Cmd) {
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
	} else {
		// NOTHING STREAMED, so the branch above has no partial answer to
		// mark -- and until this existed that meant the transcript recorded
		// the failure nowhere. The header still carries the error, but a
		// header is one line that the next identical failure overwrites;
		// this puts the failure UNDER THE MESSAGE THAT CAUSED IT, where a
		// reader looking for their answer is already looking.
		m.appendTurn(turn{role: roleSevered, text: sanitizeText(msg.err.Error())})
	}
	m.state = stateError
	m.statusErr = sanitizeText(msg.err.Error())
	m.endStream()
	return m, m.input.Focus()
}

func (m chatModel) handleResetErr(msg resetErrMsg) (tea.Model, tea.Cmd) {
	// The live transcript was already cleared synchronously in
	// clearConversation; only the daemon-side half failed. Reported as
	// a transcript note rather than statusErr/stateError, since the
	// user's chat is not actually in an error state — they can keep
	// typing normally.
	m.appendTurn(turn{role: roleSystem, text: sanitizeText(fmt.Sprintf("(local chat cleared, but clearing it on the daemon failed: %v)", msg.err))})
	m.refreshViewport()
	return m, nil
}

func (m chatModel) handleSpinnerTick(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	if m.state != stateSending && m.state != stateStreaming {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
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
	// commandTeam runs ONE turn through the specialist pipeline, whatever the
	// daemon is configured to do. Same trailing space, load-bearing for the same
	// reason as the two above.
	commandTeam = "/team "

	// commandTeamShape names the phases explicitly: "/team:researcher,coder q".
	//
	// A COLON RATHER THAN A SPACE, because a space would make "/team planner
	// what does this do" ambiguous forever -- is "planner" the first phase or
	// the first word of the question? Every disambiguation rule for that is a
	// rule the user has to know, and the one they would hit is the one where
	// their question happened to start with a role name. The colon binds the
	// shape to the command token, so the question is always everything after
	// the first space and never has to be guessed at.
	commandTeamShape = "/team:"
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

// teamPipeline is the shape "/team" asks for, sent as
// protocol.PromptRequest.Pipeline. The role names MUST match daemon/roles.go's
// roleNameResearcher/roleNameCoder exactly -- separate Go modules, no shared
// constant, the same convention-and-comment contract parsePromptKind's wire
// values live under.
//
// TWO PHASES, and specifically not four. Blind pairwise judging against a
// budget-matched single agent (docs/MULTI_AGENT_DESIGN.md §14) put
// researcher-then-coder ahead 2 wins to 1 at 1.02x the tokens, and the
// four-phase shape behind at 1 to 2 for the same tokens and three and a half
// times the wall-clock. A command that offers a user "more specialists" and
// hands them the measured loser is a worse command than none.
var teamPipeline = []string{"researcher", "coder"}

// parseTeamCommand checks raw (already trimmed) for "/team ".
//
// WHY THIS IS A COMMAND AND NOT A DETECTOR. §14 established that the right
// number of phases depends on the question: multi-hop questions want
// specialists, single lookups want one agent at a quarter of the cost. §15 then
// tried twice to tell those apart from local retrieval and failed both times for
// mechanical reasons -- the fused scores are reciprocal-RANK values with no
// relevance magnitude, and lexical breadth saturates on any natural-language
// question. So the daemon does not guess, and the person who asked the question
// gets to say. That is the same stance daemon/router.go takes on model tier, in
// its words: "triggered by explicit signals only -- never by reading or guessing
// at prompt content".
//
// "/team q" runs the measured winner. "/team:planner,coder q" runs exactly what
// it names, which is how the planner and tester -- roles that have existed and
// been unreachable from here since they were written -- become usable without
// editing a config file and restarting the daemon.
//
// THE ROLE NAMES ARE NOT VALIDATED HERE, on purpose. What roles exist is
// daemon/roles.go's fact, and a copy of that list in the TUI is a copy that
// drifts: the day someone adds a fifth role, a client that "helpfully" rejected
// it would be the reason it does not work. Unknown names are dropped by
// resolvePipeline and reported back as a protocol.DegradedPipelineShape notice,
// which is both the authoritative answer and one the user can read.
func parseTeamCommand(raw string) (pipeline []string, prompt string, ok bool) {
	if rest, found := strings.CutPrefix(raw, commandTeamShape); found {
		// THE SHAPE ENDS AT THE FIRST SPACE THAT DOES NOT FOLLOW A COMMA.
		//
		// Cutting at the first space full stop is the obvious rule and it is
		// wrong, because "researcher, coder why is this slow" is what a person
		// actually types -- and that rule reads the shape as "researcher," and
		// silently swallows "coder" as the first word of the question. A
		// trailing comma means "another role follows", which is the same thing
		// it means in prose, so it is a rule nobody has to be taught.
		end := 0
		for {
			next := strings.IndexByte(rest[end:], ' ')
			if next < 0 {
				// No space at all: a shape with nothing to ask.
				return nil, raw, false
			}
			end += next
			if !strings.HasSuffix(rest[:end], ",") {
				break
			}
			if end++; end >= len(rest) {
				return nil, raw, false
			}
		}
		spec, question := rest[:end], strings.TrimSpace(rest[end:])
		if question == "" {
			// "/team:coder " with nothing to ask, exactly like a bare "/team".
			return nil, raw, false
		}
		var names []string
		for _, n := range strings.Split(spec, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			// "/team: q" named no phases at all. That is not a request for the
			// default shape -- the user typed a colon meaning to choose -- so it
			// falls through as text rather than silently picking for them.
			return nil, raw, false
		}
		return names, question, true
	}
	if !strings.HasPrefix(raw, commandTeam) {
		return nil, raw, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(raw, commandTeam))
	if rest == "" {
		// "/team" with no question is not a request for a pipeline over nothing.
		// Falls through as ordinary text, exactly as a bare "/reason" does.
		return nil, raw, false
	}
	return teamPipeline, rest, true
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
	// A QUESTION IS THE MOMENT A KEY IS ACTUALLY NEEDED -- and this is below the
	// slash handling deliberately, so /connect, /help and everything else still
	// work on a client that has no credential yet. Only a real prompt is held.
	if m.needsAPIKey {
		m.input.SetValue("")
		return m.beginConnectForPrompt(raw)
	}
	pipeline, prompt, isTeam := parseTeamCommand(raw)
	promptKind := ""
	if !isTeam {
		promptKind, prompt = parsePromptKind(raw)
	}
	// A free-typed prompt selects no mode. "" is the wire's "unset" and the
	// daemon reads it as the ordinary full menu.
	mode := ""

	// PASTED TEXT IS UNTRUSTED. A prompt can be pasted from a web page, a log,
	// or another model's answer, and it is echoed straight back into the
	// transcript. Sanitized once here, before the turn is built, so the bytes
	// shown on screen and the bytes sent to the daemon are the same bytes.
	prompt = sanitizeText(prompt)

	// BOUND THE TRANSCRIPT FIRST, so that what is sent and what is shown are
	// the same conversation. Running it after buildHistory would send the
	// daemon turns the user can no longer see, which is the /compact
	// inconsistency this deliberately avoids. See enforceTranscriptBound for
	// why the start of a turn is the only index-safe place to do this.
	m.enforceTranscriptBound()

	// Built from the transcript BEFORE the current prompt is appended below,
	// so the not-yet-answered prompt can never end up in its own History.
	history := buildHistory(m.turns)

	m.appendTurn(turn{role: roleUser, text: prompt})
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
	m.daemonProposals = nil
	m.gotDaemonProposals = false
	m.resizeViewport()
	m.refreshViewport()

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	ch := make(chan tea.Msg)
	m.streamCh = ch

	return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, m.workspace, prompt, promptKind, mode, m.preferredTier, pipeline, history, ch))
}

func (m chatModel) handleSlash(sp slashParse) (tea.Model, tea.Cmd) {
	if sp.Def == nil {
		return m, nil
	}
	if sp.UsageOnly {
		msg := fmt.Sprintf("usage: /%s <args…>", sp.Def.Name)
		m.appendTurn(turn{role: roleAssistant, text: msg})
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
		mode := sp.Def.Mode
		var pipeline []string // steered slash commands do not choose a shape
		history := buildHistory(m.turns)
		m.appendTurn(turn{role: roleUser, text: sanitizeText("/" + sp.Def.Name + " " + sp.Args)})
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
		m.daemonProposals = nil
		m.gotDaemonProposals = false
		m.resizeViewport()
		m.refreshViewport()
		ctx, cancel := context.WithCancel(context.Background())
		m.streamCancel = cancel
		ch := make(chan tea.Msg)
		m.streamCh = ch
		return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, m.workspace, prompt, promptKind, mode, m.preferredTier, pipeline, history, ch))
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
	case "mouse":
		return m.handleMouseToggle()
	case "connect":
		// TWO LITERAL SUBCOMMANDS, AND NOTHING ELSE. The rule was never "no
		// arguments" -- it is that a KEY must not be typed here, because it would
		// be left in the transcript and sent on with the next prompt. "show" and
		// "forget" are not keys, so they are allowed; anything else is refused on
		// the assumption that it is one. See connect.go.
		switch strings.TrimSpace(args) {
		case "":
			return m.beginConnect()
		case "show":
			return m, submitConnect(m.clientName, protocol.ConnectRequest{
				ProtocolVersion: protocol.ProtocolVersion, Connect: true, Show: true,
			})
		case "forget":
			return m, submitConnect(m.clientName, protocol.ConnectRequest{
				ProtocolVersion: protocol.ProtocolVersion, Connect: true, Forget: true,
			})
		default:
			reply = "/connect takes no key as an argument: one typed on the command line would be left in " +
				"this transcript and sent with your next prompt. Run /connect on its own and paste it at " +
				"the masked prompt. The only arguments are `show` and `forget`."
		}
	case "compact":
		const keep = 8
		if len(m.turns) > keep {
			// A re-slice of turns that appendTurn already sanitized; nothing
			// new enters the transcript here.
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
	m.appendTurn(turn{role: roleAssistant, text: reply})
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
		b.WriteString("models (/model <name> to select; /model clear to reset):\n")
		current := m.preferredTier
		if current == "" {
			current = "(default)"
		}
		fmt.Fprintf(&b, "current: %s\n", current)
		for _, t := range tiers {
			mark := "  "
			if t.Name == m.preferredTier || (m.preferredTier == "" && t.Name == "primary") {
				mark = "* "
			}
			status := ""
			if !t.Active {
				status = " [inactive]"
			}
			fmt.Fprintf(&b, "%s%s  %s%s\n", mark, t.Name, t.Slug, status)
		}
		m.appendTurn(turn{role: roleAssistant, text: strings.TrimRight(b.String(), "\n")})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	if arg == "clear" || arg == "default" {
		m.preferredTier = ""
		m.appendTurn(turn{role: roleAssistant, text: "model reset to default tier (models.json default_tier)"})
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
		m.appendTurn(turn{role: roleAssistant, text: fmt.Sprintf("unknown model tier %q — try /model for the list", arg)})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	if !found.Active {
		m.appendTurn(turn{role: roleAssistant, text: fmt.Sprintf("tier %q is inactive in models.json", arg)})
		m.resizeViewport()
		m.refreshViewport()
		return m, nil
	}
	m.preferredTier = found.Name
	m.appendTurn(turn{role: roleAssistant, text: fmt.Sprintf("model set to %s (%s)", found.Name, found.Slug)})
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
	// RELEASE THE ESCAPE FILTERS FIRST, before anything reads the turn. A
	// stream that stops mid-sequence leaves bytes held inside the parser, and
	// they are the tail of the user's answer -- dropping them would be silent
	// data loss on every interrupted turn. Both callers that inspect the turn
	// afterwards (streamErrMsg's "did anything stream" check, and
	// checkForEditBlocks) run after this, so both see the complete text.
	if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) {
		m.turns[m.streamAssistant].text += m.sanAnswer.Flush()
		m.turns[m.streamAssistant].reasoning += m.sanReasoning.Flush()
	} else {
		// No turn to append to, but the parsers must still be reset or the
		// next stream inherits a half-read sequence from this one.
		m.sanAnswer.Flush()
		m.sanReasoning.Flush()
	}
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamCh = nil

	// THE GUARANTEED FINAL REPAINT. Token repaints are coalesced onto a tick
	// (see refreshSoon), so the last few tokens of an answer and the sanitizer
	// tail flushed just above are, at this instant, in the model and not on the
	// screen. Nothing else is obliged to draw them: the ordinary end of an
	// ordinary answer runs streamDoneMsg -> checkForEditBlocks, which returns
	// without a refresh when the answer contains no edit blocks, and that is
	// the common case. Without this line the visible answer would stop up to
	// one tick short of what the model actually said, and stay that way until
	// the user happened to type something.
	m.refreshViewport()
}

// handleCtrlC answers every ctrl+c in the program, in every state.
//
// ONE PRESS NEVER ENDS THE SESSION. That was the complaint that started this:
// "when I press esc / ctrl+c it exits". The stop key landed, the turn ended,
// and the NEXT ctrl+c -- pressed a beat later, out of the same habit that had
// just been rewarded -- quit the program and took the transcript with it. A key
// whose meaning flips from "stop" to "exit" depending on whether a turn
// happened to still be running is a trapdoor, and the fix is that the exit half
// asks first.
//
// So, in order:
//
//  1. a turn in flight is stopped (interruptTurn);
//  2. a turn paused at an approval prompt is stopped through the approval
//     channel, which tells the daemon why rather than hanging up on it;
//  3. with nothing to stop, the FIRST press only arms the quit and says so in
//     the footer. Only a second consecutive ctrl+c exits.
//
// Any other key disarms it (see the Update case above), so the armed state can
// never survive long enough to surprise anyone. There is no timer: a keypress
// is a better disarm signal than a clock, and it is deterministic to test.
// /exit still quits outright for anyone who wants one keystroke's worth of
// certainty.
func (m chatModel) handleCtrlC() (tea.Model, tea.Cmd) {
	if m.turnInFlight() {
		m.quitArmed = false
		return m.interruptTurn()
	}
	if m.state == stateToolApproval {
		m.quitArmed = false
		return m.answerApproval(protocol.ApprovalCancelTurn)
	}
	if m.quitArmed {
		if m.streamCancel != nil {
			m.streamCancel()
		}
		return m, tea.Quit
	}
	m.quitArmed = true
	return m, nil
}

// turnInFlight reports whether there is a turn to stop: the daemon is working
// on this prompt right now, whether or not any of it has reached the screen.
// stateToolApproval is deliberately NOT in here -- a paused turn is stopped
// through the approval channel (handleApprovalKey), which tells the daemon WHY
// it stopped rather than hanging up on it mid-question.
func (m chatModel) turnInFlight() bool {
	return m.state == stateSending || m.state == stateStreaming
}

// interruptTurn stops the turn in flight and hands the session back to the
// user, instead of ending the process.
//
// IT EXISTS BECAUSE THERE WAS NO WAY TO SAY STOP. Once a prompt was sent, the
// only key that ended the wait was ctrl+c, and that quit -- so a turn that had
// gone somewhere useless (a long agent loop, a web fetch that was never going
// to answer the question, an answer already visibly wrong in its first
// sentence) could only be escaped by throwing away the whole conversation. That
// is a bad trade to force on someone, and it silently taught the habit of
// sitting through turns nobody wanted.
//
// Three things happen, and the second and third are what make this an interrupt
// rather than a way to lose work:
//
//  1. endStream cancels the request context. That is what actually stops
//     things: streamPrompt's watcher closes the connection, the daemon's next
//     write to this client fails, and it abandons the turn (see the write-error
//     cancel in daemon/agentturn.go) rather than finishing an answer nobody is
//     reading. Clearing streamCh is what makes every message still in flight
//     from the abandoned stream a no-op -- every handler in Update already
//     checks it.
//
//  2. Whatever streamed stays. It is real output the user watched arrive, and
//     deleting it on a keypress would make the key frightening to press.
//
//  3. The partial answer is MARKED user_cancelled, so the next prompt carries
//     it back to the model as a turn the user stopped rather than as a finished
//     reply (see buildHistory and daemon/history.go's incompleteHistoryNote).
//     Without this the model would read its own truncated answer as a
//     conclusion it had reached, which is the same loss the streamErrMsg branch
//     above exists to prevent -- through a different door.
func (m chatModel) interruptTurn() (tea.Model, tea.Cmd) {
	if !m.turnInFlight() {
		return m, nil
	}
	m.endStream()

	if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) &&
		strings.TrimSpace(m.turns[m.streamAssistant].text) != "" {
		m.turns[m.streamAssistant].incomplete = protocol.IncompleteUserCancelled
	}
	// A permanent scrollback line rather than a header notice, for the same
	// reason the cut-off notice is one: "why does this answer stop there?" is a
	// question asked while scrolling back, long after a header has been
	// overwritten by the next turn.
	// Worded as what the CLIENT knows for certain. It stopped listening and hung
	// up; the daemon stops on its next write (see agentturn.go), and a tool
	// already dispatched may still be finishing as this line is drawn. "nothing
	// further ran" would be a claim about the far end that this side cannot make.
	m.appendTurn(turn{role: roleSystem, text: "⏹ stopped — you interrupted this turn"})

	m.state = stateIdle
	m.statusErr = ""
	m.resizeViewport()
	m.refreshViewport()

	// 4. EDITS THE MODEL ALREADY FINISHED ARE STILL OFFERED.
	//
	// This used to return straight to an idle prompt, and that quietly
	// contradicted rule 2 above. "Whatever streamed stays" kept the TEXT of a
	// completed edit block on screen while throwing away the edit -- the one
	// thing in it the user could act on. Stopping a turn three files in meant
	// re-running the whole turn to get back the two files that had finished.
	//
	// checkForEditBlocks is the same review the normal streamDoneMsg path runs.
	// An interrupted turn never reaches the Done message, so the daemon's
	// EditProposals never arrived and it takes its local-parse fallback -- which
	// is exactly the case that fallback exists for. A block the model was still
	// mid-way through writing is unterminated, so the parser refuses it by line
	// number and the completed ones are unaffected (Fix B).
	return m.checkForEditBlocks()
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
	m.turns[m.ensureAssistantTurn()].text += m.sanAnswer.Write(string(msg))
	return m, tea.Batch(m.refreshSoon(), waitForNext(m.streamCh))
}

// appendTurn is THE ONLY WAY A TURN ENTERS THE TRANSCRIPT, and it sanitizes.
//
// WHY THIS IS A FUNCTION AND NOT A CONVENTION. The first pass at this filtered
// every ingest point it could find and left `m.turns = append(...)` as an
// ordinary statement anyone could write. Reviewing that same commit turned up
// FOUR appends it had missed -- the approval outcome line (an MCP server picks
// its own name), the daemon's incomplete-answer Detail, an edit-block refusal
// quoting the model's own markup, and the review summary's refusal reasons.
// Four misses in the commit that was paying attention is the measurement that
// says a convention does not hold here. The escape-hatch is now one function,
// and TestOnlyAppendTurnWritesTheTranscript fails on any new statement that
// goes around it.
//
// Sanitizing is idempotent and its clean path allocates nothing (sanitize.go),
// so text already filtered at its source passes through unchanged and the
// double pass costs nothing worth measuring. Appends are per-turn, never
// per-token.
func (m *chatModel) appendTurn(t turn) int {
	t.text = sanitizeText(t.text)
	t.reasoning = sanitizeText(t.reasoning)
	m.turns = append(m.turns, t)
	return len(m.turns) - 1
}

// ensureAssistantTurn returns the index of this stream's assistant turn,
// creating it if the stream has not spoken yet. Wherever the tool-activity
// notices happen to have landed, the answer keeps going to the same place.
func (m *chatModel) ensureAssistantTurn() int {
	if m.streamAssistant >= 0 && m.streamAssistant < len(m.turns) {
		return m.streamAssistant
	}
	m.streamAssistant = m.appendTurn(turn{role: roleAssistant})
	return m.streamAssistant
}

// checkForEditBlocks decides what, if anything, the just-finished turn asked
// to change on disk, and moves into the modal review state when the answer is
// "something". Nothing found means ordinary idle chat, unchanged from before
// this feature existed.
//
// It prefers the DAEMON's list (protocol.TokenResponse.EditProposals) and falls
// back to parsing the assistant text locally. That order is a fix, not a
// preference. This client used to only ever parse locally, which looks
// equivalent and is not: the daemon merges two sources into EditProposals
// (daemon/agentturn.go:232) -- blocks the model wrote as text, AND edits it
// filed through the propose_edit tool -- and the second kind never appears in
// the assistant text at all. So in agent mode every propose_edit proposal was
// silently invisible here, while daemon/agentturn.go's own comment asserts
// "there is no path by which an agent turn changes a file without the user
// seeing a diff first". The invariant held on the wire and this client broke it.
//
// The local parse stays as the fallback for two live cases: an older daemon
// that does not send the field at all, and an INTERRUPTED turn, which never
// reaches the Done message the field rides on. Blocks the model finished before
// the user pressed esc are still real edits, and interruptTurn routes here to
// offer them.
func (m chatModel) checkForEditBlocks() (tea.Model, tea.Cmd) {
	m.state = stateIdle

	var blocks []editapply.EditBlock
	var rejected []editapply.BlockError

	if m.gotDaemonProposals {
		blocks = blocksFromWire(m.daemonProposals)
	} else {
		text := lastAssistantText(m.turns)
		if text == "" {
			return m, m.input.Focus()
		}
		// ParseEditPayload rather than ParseEditBlocks, so a model that
		// answers with a unified diff is UNDERSTOOD here and not merely named:
		// its hunks become ordinary blocks and go to the same review panel.
		payload := editapply.ParseEditPayload(text)
		blocks, rejected = payload.Blocks, payload.Rejected

		// Edit-shaped and unreadable: say so rather than returning to an idle
		// prompt in silence. Without this the user watched an edit arrive and
		// then watched the client behave as though a question had been
		// answered.
		if payload.Format == editapply.FormatUnrecognised {
			m.appendTurn(turn{role: roleSystem,
				text: fmt.Sprintf("(no edits offered — line %d of the answer %s)", payload.Hint.Line, payload.Hint.Advice)})
			m.refreshViewport()
			return m, m.input.Focus()
		}
		if len(blocks) == 0 && len(rejected) == 0 {
			return m, m.input.Focus()
		}
	}

	// A refused block is shown as its own system turn and costs only itself
	// (Fix B): the readable blocks in the same response still go to review,
	// where before a single bad block sent the whole reply to this message and
	// nothing was offered.
	for _, bad := range rejected {
		m.appendTurn(turn{role: roleSystem, text: fmt.Sprintf("(edit block at line %d refused: %v)", bad.Line, bad.Reason)})
	}
	if len(rejected) > 0 {
		m.refreshViewport()
	}
	if len(blocks) == 0 {
		return m, m.input.Focus()
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

// blocksFromWire converts the daemon's proposals to the engine's own type. It
// is a field-for-field copy on purpose: protocol.EditBlockWire is the wire
// shape and editapply.EditBlock is what PrepareEdit takes, and keeping them
// distinct is what stops a protocol change from silently altering the engine's
// input.
func blocksFromWire(wire []protocol.EditBlockWire) []editapply.EditBlock {
	if len(wire) == 0 {
		return nil
	}
	blocks := make([]editapply.EditBlock, len(wire))
	for i, w := range wire {
		blocks[i] = editapply.EditBlock{FilePath: w.FilePath, Search: w.Search, Replace: w.Replace}
	}
	return blocks
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
// as the CLI's `[y/N]` prompt. 'n' skips just the current block; 'q' (and esc,
// which means "stop this" everywhere in this UI) cancels every remaining block
// (including the current one) as skipped.
func (m chatModel) handleReviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		return m.applyCurrentReviewEdit()
	case "n":
		m.reviewSkipped++
		m.reviewIndex++
		return m.advanceReview()
	case "q", "esc":
		// esc is 'q' here, not quit, under the same rule it follows everywhere
		// else in this UI: it stops the thing in front of you. The thing in
		// front of you is the review, and leaving it applies nothing further --
		// esc has never been able to apply an edit and still cannot.
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
		// Both stop the TASK and stay in the session. ctrl+c does the same, from
		// handleCtrlC: a paused turn is the one moment where hanging up is
		// strictly worse than answering, because the daemon is holding a tool
		// call open and only a decision on this channel closes it cleanly.
		return m.answerApproval(protocol.ApprovalCancelTurn)
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

	m.appendTurn(turn{role: roleSystem, text: approvalOutcomeLine(*m.pendingApproval, decision)})
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
	// Tool names and results are chosen by the server on the other end of an
	// MCP connection, not by us.
	line := sanitizeText(toolActivityLine(a))
	if line == "" {
		return
	}
	if m.activityTurns == nil {
		m.activityTurns = map[string]int{}
	}
	// An EMPTY CallID is not a call to rewrite, so it must never be used as a
	// key. Two things arrive with one: a pipeline phase marker (there is no
	// call), and -- MEASURED against a live provider -- a real tool call the
	// model emitted with no id at all. Keying on "" collapsed all of them onto
	// a single line, so four phase markers showed as one and the second of two
	// refusals silently overwrote the first.
	if a.CallID != "" {
		if idx, ok := m.activityTurns[a.CallID]; ok && idx < len(m.turns) {
			m.turns[idx].text = line
			return
		}
		m.activityTurns[a.CallID] = len(m.turns)
	}
	m.appendTurn(turn{role: roleSystem, text: line})
}

func toolActivityLine(a protocol.ToolActivity) string {
	name := qualifiedActivityName(a)
	switch a.Phase {
	case protocol.ToolPhaseStep:
		// A pipeline phase, not a tool call -- rendered differently on purpose,
		// so a user can see the specialists change hands rather than reading it
		// as one more tool the agent ran.
		return "▸ " + a.Detail + " — " + a.Tool
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

// qualifiedActivityName renders the tool's name for a human.
//
// Server is EMPTY when the model named a tool without its server prefix, and
// the unconditional a.Server+"__"+a.Tool that used to be here then rendered a
// leading "__" on a name the user was being asked to make sense of.
func qualifiedActivityName(a protocol.ToolActivity) string {
	if a.Server == "" {
		return a.Tool
	}
	return a.Server + "__" + a.Tool
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

// applyCurrentReviewEdit backs up and writes m.reviewPrepared using the same
// editapply backup+write calls the CLI's applyEditBlocks uses — the same
// .mochiii/backups/<session>/{before,after}/ layout, restorable via
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
	m.appendTurn(turn{role: roleSystem, text: strings.Join(lines, "\n")})

	m.reviewBlocks = nil
	m.reviewPrepared = nil
	m.reviewIndex = 0
	m.state = stateIdle
	m.refreshViewport()
	return m, m.input.Focus()
}

func (m *chatModel) refreshViewport() {
	// FOLLOW THE STREAM ONLY FOR SOMEONE WHO IS ALREADY AT THE BOTTOM.
	//
	// This used to be an unconditional GotoBottom, and refreshViewport runs on
	// every streamed token, so scrolling back through an answer while it was
	// still arriving was not awkward but impossible: the view snapped to the
	// bottom within milliseconds and the text being read was gone.
	//
	// Read BEFORE SetContent, because SetContent changes the line count and
	// therefore what "the bottom" means. Someone who scrolls back to the bottom
	// starts following again, which is why this is a question asked every time
	// rather than a flag set once.
	follow := m.viewport.AtBottom()
	m.refreshPending = false

	content := m.transcript.render(m.turns, m.viewport.Width)
	if m.state == stateEditReview && m.reviewPrepared != nil {
		content += "\n\n" + renderReviewPanel(m.reviewIndex, len(m.reviewBlocks), m.reviewPrepared)
	}
	if m.state == stateToolApproval && m.pendingApproval != nil {
		content += "\n\n" + renderApprovalPanel(*m.pendingApproval)
	}
	m.viewport.SetContent(wrapToWidth(content, m.viewport.Width))
	if follow {
		m.viewport.GotoBottom()
	}
}

// wrapToWidth reflows transcript content to the viewport's width.
//
// THE VIEWPORT DOES NOT WRAP. bubbles' viewport.SetContent splits on "\n" and
// nothing else, and View() then clips each line to Width -- so every answer
// longer than the terminal was readable up to the right edge and INVISIBLE
// after it, with no scrollbar, no ellipsis and nothing on screen to say text
// had been cut. A model's paragraph is one logical line, so in practice that
// meant most real answers.
//
// ansi.Wrap rather than ansi.Wordwrap, and the difference is not cosmetic:
// Wordwrap leaves a word longer than the limit intact, so a file path, a URL or
// a base64 blob -- exactly what a coding assistant emits -- would still run off
// the edge and still be unreadable. Wrap breaks inside an over-long word.
//
// It preserves ANSI styling and counts wide characters, which both matter here:
// the content arrives already styled by userStyle/assistantStyle, and a naive
// byte- or rune-based wrap would either cut an escape sequence in half or
// mis-measure CJK and emoji.
//
// Width 0 means the terminal size is not known yet (before the first
// WindowSizeMsg). Returned unchanged rather than wrapped to nothing -- the same
// tolerance truncateToWidth has, for the same reason.
func wrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Wrap(s, width, "")
}

// renderTranscript renders the conversation.
//
// It takes a WIDTH, which it did not before, because one thing in here has to
// be drawn to the edge of the terminal rather than wrapped to it: the severed
// rail below fades across the full line, and a rule that wrapToWidth folds onto
// a second line stops reading as a rule and starts reading as damage.
func renderTranscript(turns []turn, width int) string {
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString(turnSeparator)
		}
		b.WriteString(renderTurnBlock(t, width))
	}
	return b.String()
}

// turnSeparator is the blank line between turns. Named because the render cache
// concatenates blocks itself and has to write the identical separator.
const turnSeparator = "\n\n"

// renderTurnBlock renders ONE turn, with no dependence on the turns around it.
//
// Split out of renderTranscript so a block can be rendered and reused on its
// own. That independence is a real property and not just a convenience: it is
// what makes a per-turn cache correct, and what makes renderTranscript equal to
// the concatenation of its parts. Nothing here may consult a neighbouring turn.
func renderTurnBlock(t turn, width int) string {
	switch t.role {
	case roleUser:
		return userStyle.Render("You: " + t.text)
	case roleAssistant:
		// Thinking (if any) renders first, dimmed and labelled, so it is
		// visibly SEPARATE from the answer and never mistaken for it -- the
		// answer text is what carries back as history and gets parsed for edit
		// blocks; the reasoning never does.
		if t.reasoning != "" {
			return helpStyle.Render("💭 thinking: "+t.reasoning) + "\n" +
				assistantStyle.Render("Mochiii: "+t.text)
		}
		return assistantStyle.Render("Mochiii: " + t.text)
	case roleSystem:
		return helpStyle.Render(t.text)
	case roleSevered:
		return renderSevered(t.text, width)
	}
	return ""
}

// severedDecay is the fade the rail is drawn from: dense to sparse, left to
// right. Four stages rather than a gradient of colour because a terminal's
// colour support is a variable and its glyph set is not -- the fade still
// reads as a fade in a monochrome terminal, which is where an error most needs
// to be legible.
const severedDecay = "▓▒░·"

// severedLabel opens the rail. Block glyphs rather than an emoji: this line has
// to survive a terminal that renders emoji at double width and one that renders
// them as a replacement box, and "⚠" is already spoken for by the cut-off
// answer notice -- two different failures should not open with the same mark.
const severedLabel = "▚▚ LINK SEVERED "

// renderSevered draws a prompt that never left the machine.
//
// THE SHAPE IS THE MESSAGE. A boxed red error is what every program prints and
// is therefore what a reader's eye has learned to skip; this is a transmission
// visibly decaying to nothing across the width of the terminal, which is
// literally what happened. Underneath it, in plain text and unstyled by
// anything that could obscure it, the daemon's own words -- because a marker
// that looks striking and hides the actual error would be a worse bug than the
// silence it replaced.
func renderSevered(detail string, width int) string {
	var b strings.Builder

	// The rail fills whatever is left. Skipped entirely when there is no room:
	// a fade squeezed into four columns is not a fade, and this client is
	// expected to survive widths down to zero (see narrowterminal_test.go).
	// When it IS skipped the label loses its trailing space too -- a bold,
	// coloured space at the end of a line is invisible until someone selects
	// the text, and then it looks like a mistake.
	if remaining := width - lipgloss.Width(severedLabel); remaining > 8 {
		dense, sparse := severedRail(remaining)
		b.WriteString(severedStyle.Render(severedLabel))
		b.WriteString(errorStyle.Render(dense))
		b.WriteString(helpStyle.Render(sparse))
	} else {
		b.WriteString(severedStyle.Render(strings.TrimRight(severedLabel, " ")))
	}

	// Indented under the rail so the error belongs to it visually, and rendered
	// in the error colour rather than faint: this is the part the user actually
	// needs to read.
	b.WriteString("\n" + errorStyle.Render("  "+detail))
	b.WriteString("\n" + helpStyle.Render("  ↳ nothing was sent — this prompt never reached the daemon"))
	return b.String()
}

// severedRail builds the fade and splits it where the colour changes.
//
// Returned as two pieces rather than one string so the caller can style the
// dense head and the sparse tail differently, which is what turns a row of
// glyphs into something that looks like it is receding.
func severedRail(width int) (dense, sparse string) {
	stages := []rune(severedDecay)
	per := width / len(stages)
	if per < 1 {
		per = 1
	}
	var all []rune
	for _, r := range stages {
		for i := 0; i < per && len(all) < width; i++ {
			all = append(all, r)
		}
	}
	// Any remainder is the faintest glyph: a fade must end at its lightest, not
	// restart. Integer division leaves up to len(stages)-1 columns short.
	for len(all) < width {
		all = append(all, stages[len(stages)-1])
	}
	// The split point is where the fade stops being "solid". Half the rail is
	// dense, half is sparse.
	cut := per * 2
	if cut > len(all) {
		cut = len(all)
	}
	return string(all[:cut]), string(all[cut:])
}

// renderReviewPanel shows one edit block as a diff: the whole SEARCH block
// as removed lines, the whole REPLACE block as added lines, plus the
// syntax-check note — mirroring the CLI's printEditDiff (daemon/apply_cmd.go)
// so both surfaces present the same information about the same edit.
// SANITIZED HERE, AT RENDER, AND NOT AT INGEST -- the one place in this
// client that deviates from "clean the bytes on the way in".
//
// The reason is that these exact bytes are also what gets WRITTEN TO DISK if
// the user approves. A file may legitimately contain escape sequences (a
// terminal test fixture is the obvious case, and this repo has one), so
// cleaning the block on the way in would silently corrupt the edit it is
// about to apply. The block therefore stays byte-exact for the writer, and
// only the copy going to the screen is filtered.
func renderReviewPanel(index, total int, p *editapply.PreparedEdit) string {
	var b strings.Builder
	b.WriteString(brandStyle.Render(fmt.Sprintf("--- edit %d/%d: %s (lines %d-%d) ---", index+1, total, sanitizeText(p.Block.FilePath), p.StartLine, p.EndLine)))
	for _, l := range strings.Split(sanitizeText(p.Block.Search), "\n") {
		b.WriteString("\n" + diffRemovedStyle.Render("- "+l))
	}
	for _, l := range strings.Split(sanitizeText(p.Block.Replace), "\n") {
		b.WriteString("\n" + diffAddedStyle.Render("+ "+l))
	}
	// Surfaced next to the diff for the same reason the CLI does it: the user is
	// about to approve this write, and a match that needed whitespace/encoding
	// tolerance is something they should see before they do.
	if p.MatchNote != "" {
		b.WriteString("\n" + helpStyle.Render("match: "+sanitizeText(p.MatchNote)))
	}
	b.WriteString("\n" + helpStyle.Render("syntax check: "+sanitizeText(p.SyntaxNote)))
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
		sanitizeText(req.Server), sanitizeText(req.Tool), req.Iteration, req.MaxIterations)))

	// SANITIZED, and this is the screen where it matters most. The arguments
	// are written by the model; the daemon binds its approval to a digest of
	// exactly these bytes, so the request itself must stay untouched and only
	// the rendering is filtered. An escape sequence here could repaint the
	// question the user is answering -- which is not a display bug, it is
	// forged consent.
	b.WriteString("\n" + helpStyle.Render("arguments:"))
	for _, line := range strings.Split(sanitizeText(req.Arguments), "\n") {
		b.WriteString("\n" + diffAddedStyle.Render("  "+line))
	}

	switch {
	case req.ReachesNetwork:
		// CHECKED BEFORE Confined, because for a network tool the confinement
		// line is not the sentence the user needs. "Anything it changes goes
		// through the same review you use for edits" is TRUE of web_search --
		// it changes nothing -- and it is the wrong answer to the question
		// actually being asked, which is where the words above are about to go.
		b.WriteString("\n" + diffRemovedStyle.Render("LEAVES YOUR MACHINE: this sends the text above to a third party "+
			"over the internet and brings a reply back into the conversation."))
		b.WriteString("\n" + helpStyle.Render("Mochiii strips secrets on the way out and treats whatever comes back as untrusted data, "+
			"never as instructions — but it cannot vouch for the far end."))
	case req.LaunchesSubprocess:
		// BEFORE Confined and before the default, because neither of those
		// sentences is the one this user needs. "Ships with Mochiii" is true
		// and irrelevant -- the risk is not our code, it is the program our
		// code starts. "A separate program running with your full access" is
		// also true, and reads as though a third-party MCP server were being
		// approved, which sends the user looking for a server they never
		// configured.
		//
		// TRUE OF THIS CALL, not of the tool (register item 32). A language
		// server is started once and kept, and this case used to fire on every
		// prompt because the flag was the tool's -- so "STARTS" was printed
		// about a server already running, and the one approval that really
		// started it looked like all the rest. The daemon now sets the flag only
		// when approving THIS call starts the program, and names it.
		if program := sanitizeText(req.Program); program != "" {
			b.WriteString("\n" + diffRemovedStyle.Render("STARTS "+program+": approving this runs "+program+
				" from your PATH against this repository. It keeps running, and reading this project, until the daemon exits."))
		} else {
			// An older daemon, or a call whose program could not be determined:
			// the generic sentence, which over-states rather than hides.
			b.WriteString("\n" + diffRemovedStyle.Render("STARTS ANOTHER PROGRAM: this runs a language server from your "+
				"PATH against this repository — gopls, tsserver or pyright."))
		}
		b.WriteString("\n" + helpStyle.Render("Mochiii ships the tool but not that program. It reads configuration out of the "+
			"project you have open (a tsconfig.json can load plugins), so a repository you do not trust can influence it."))
	case req.Program != "":
		// THE OTHER HALF OF THE SAME TRUTH. Without this case a running-server
		// prompt -- Confined is false for these tools -- falls to the default,
		// whose "a separate program running with your full access" reads as a
		// third-party server being approved. What is true is narrower: the
		// program was started with the user's approval, and this call only asks
		// it a question.
		program := sanitizeText(req.Program)
		b.WriteString("\n" + helpStyle.Render("ASKS "+program+", WHICH IS ALREADY RUNNING: you approved starting it "+
			"earlier in this session. This call starts nothing new."))
	case req.Confined:
		b.WriteString("\n" + helpStyle.Render("this tool ships with Mochiii; anything it changes goes through the same review you use for edits"))
	default:
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
		bottomLine = accentStyle.Render(fmt.Sprintf("edit %d/%d: %s", m.reviewIndex+1, len(m.reviewBlocks), sanitizeText(m.reviewPrepared.Block.FilePath)))
		help = helpStyle.Render(reviewHelpText)
	} else if m.state == stateToolApproval && m.pendingApproval != nil {
		bottomLine = accentStyle.Render(fmt.Sprintf("approve %s__%s?", sanitizeText(m.pendingApproval.Server), sanitizeText(m.pendingApproval.Tool)))
		help = helpStyle.Render(approvalHelpText)
	} else {
		popupStr, _ := m.renderSlashPopup()
		if popupStr != "" {
			bottomLine = popupStr + "\n" + m.input.View()
		} else {
			bottomLine = m.input.View()
		}
		if m.turnInFlight() {
			help = helpStyle.Render(interruptHelpText)
		} else {
			help = helpStyle.Render(helpText)
		}
	}

	// Overrides whatever the state would otherwise hint, in every state: the
	// question "will this key exit?" outranks every other hint on the line, and
	// it is only on screen until the next keypress answers it.
	if m.quitArmed {
		help = helpStyle.Render(quitConfirmHelpText)
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
// comment for the bug this fixes).
//
// ONE ELEMENT IS ONE TERMINAL ROW. That is the contract headerLineCount's
// arithmetic depends on, and everything here exists to keep it exact: a notice
// that soft-wrapped would consume a row nobody counted, desyncing the viewport
// height and pushing content off-screen.
//
// It used to be kept by TRUNCATING each notice to m.width with a trailing "…",
// which held the invariant by throwing away the end of the sentence. That is
// the wrong half to give up: the longest notice this renders is the ZDR
// degradation, whose whole purpose is to disclose a weakened privacy guarantee,
// and on an 80-column terminal it was cut off around "...permits fallbacks
// outside the c…" -- disclosing that something was wrong while hiding what.
//
// Wrapping and returning one element PER ROW keeps the invariant and the text:
// every element is still exactly one row, len() is still the exact count, and
// nothing is hidden. A long notice on a narrow terminal now costs header rows,
// which is the correct trade -- resizeViewport floors the viewport at one row,
// so it can shrink the transcript but never break the layout.
// sanitizeAll and sanitizeDegradations clean the short free-text labels the
// daemon puts in the header. They are small and they are not the model's
// words, but they are still bytes from off this machine -- the provider name
// comes back from the provider's own API, and a degradation detail is written
// by whichever subsystem reduced itself. The header is one of the few places
// that draws OUTSIDE the viewport, so an escape here is not even bounded by
// the transcript.
func sanitizeAll(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = sanitizeText(s)
	}
	return out
}

func sanitizeDegradations(items []protocol.Degradation) []protocol.Degradation {
	if items == nil {
		return nil
	}
	out := make([]protocol.Degradation, len(items))
	for i, d := range items {
		d.Component = sanitizeText(d.Component)
		d.Detail = sanitizeText(d.Detail)
		out[i] = d
	}
	return out
}

func (m chatModel) noticeLines() []string {
	var lines []string
	add := func(s string) {
		if s == "" {
			return
		}
		lines = append(lines, wrapToRows(s, m.width)...)
	}
	add(m.groundingLabel())
	add(m.historyTruncatedLabel())
	add(m.redactionsLabel())
	add(m.providerLabel())
	for _, degraded := range m.degradedLabels() {
		add(degraded)
	}
	return lines
}

// wrapToRows wraps s to width and returns one string per terminal row, so a
// caller counting rows can count elements. See noticeLines for why that
// equivalence is load-bearing.
func wrapToRows(s string, width int) []string {
	return strings.Split(wrapToWidth(s, width), "\n")
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

// COALESCED REPAINTS.
//
// THE DEFECT: refreshViewport ran on every streamed token. Even with the render
// cache in place that is a wrap of the whole transcript plus a viewport
// SetContent, both linear in total bytes -- MEASURED at 240 prior turns, 2.69ms
// and 0.61ms, against a 0.5ms budget for the whole Update. Tokens arrive far
// faster than a terminal can show them, and Bubble Tea does not coalesce
// Updates, so most of that work was being done to produce frames nobody ever
// saw: the renderer only flushes at its own frame rate.
//
// So a token now marks the transcript dirty and asks for a repaint SOON rather
// than performing one. At most one repaint per refreshInterval, and at most one
// tick outstanding at a time.
//
// THE TRAILING-TOKEN HAZARD, which is the reason this is written as
// "pending plus scheduled" rather than a plain ticker: a stream that ends
// between two ticks must still show its last tokens. Two things prevent that
// loss. Every token that finds no tick scheduled arms one, so there is always a
// repaint after the final token; and endStream repaints unconditionally, which
// covers the ordinary ending, the error ending and the interrupt.
//
// WHAT HAPPENS WHEN TOKENS OUTPACE THE RENDERER -- the backpressure question,
// answered in one place so it is not left to be inferred. Nothing queues.
// Repaint requests are not events, they are a single bit: an arbitrary number
// of tokens between two ticks collapse into the one bit, and the repaint that
// follows draws the transcript as it is at that moment. No token is dropped
// (they are appended to the turn as they arrive, which is not throttled) and no
// unbounded structure grows (there is nothing to grow -- the bit is either set
// or not). The only thing discarded is intermediate FRAMES, which is the whole
// intent, and the daemon connection provides the real backpressure: it is a
// synchronous socket read, so a client that cannot keep up stops reading and
// the sender blocks.
const refreshInterval = 16 * time.Millisecond

// refreshTickMsg asks Update to repaint if anything has changed.
type refreshTickMsg struct{}

// refreshSoon records that the transcript has changed and returns a Cmd that
// will repaint, or nil when a repaint is already on its way.
func (m *chatModel) refreshSoon() tea.Cmd {
	m.refreshPending = true
	if m.refreshScheduled {
		return nil
	}
	m.refreshScheduled = true
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// BOUNDING A PASTE.
//
// THE COST: textinput processes every rune it is handed and then enforces its
// own CharLimit, so pasting a megabyte meant a million runes of work in order
// to keep four thousand. MEASURED, 9 samples, Update time for one paste:
//
//	  0 turns, 1MB: min 16.2ms  median 17.1ms  max 20.1ms
//	240 turns, 1MB: min 13.7ms  median 15.6ms  max 19.6ms
//
// Over D-1's 16ms frame ceiling at the median on an empty transcript, and
// straddling it at 240 turns. Note what those two rows say together: the cost
// is linear in the SIZE OF THE PASTE and independent of the transcript, so the
// render cache never touched it. That is why it is its own task.
//
// THE SILENT-TRUNCATION DEFECT, which is the more serious half and was already
// there. The prompt box has held 4000 characters since it was written, and
// everything past that was dropped without a word. Paste a 40KB file meaning to
// ask about it and the model is asked about its first 4000 characters instead,
// with nothing on screen to say so -- and the answer comes back confident and
// about the wrong input. Same rule as the transcript bound: dropping the user's
// data quietly is a defect, not a limit.
//
// So an over-long rune burst is cut to what could possibly be kept, before
// textinput ever sees it, and the header says what happened.
func (m *chatModel) boundPaste(msg tea.KeyMsg) tea.KeyMsg {
	if msg.Type != tea.KeyRunes {
		return msg
	}
	// CharLimit is the most that can survive however the input is arranged, so
	// anything past it is work whose result is guaranteed to be discarded.
	// Cutting at exactly the limit -- rather than at the room actually left --
	// keeps this a pure bound on WORK: it never drops a rune that textinput
	// would have kept, and textinput still applies its own limit afterwards.
	limit := m.input.CharLimit
	if limit <= 0 {
		limit = 4000 // an input with no limit of its own still gets one here
	}
	if len(msg.Runes) <= limit {
		return msg
	}

	total := len(msg.Runes)
	dropped := total - limit
	msg.Runes = msg.Runes[:limit]
	m.statusErr = fmt.Sprintf("paste of %s characters: kept the first %s, dropped %s. The prompt box holds %s.",
		withThousands(total), withThousands(limit), withThousands(dropped), withThousands(limit))
	return msg
}

// withThousands groups digits so a six-figure count reads as one. "1048576"
// and "1,048,576" are the same number and only one of them is legible at a
// glance in a single line of terminal chrome.
func withThousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// MOUSE CAPTURE IS A TRADE, AND IT USED TO BE MADE SILENTLY.
//
// tea.WithMouseCellMotion (main.go) tells the terminal to report mouse events to
// this program. It buys exactly one thing: wheel scrolling in the transcript
// viewport. It costs the terminal's own click-drag text selection, so copying
// anything out of the client requires holding Shift -- a convention that is
// real, but is not written anywhere the user can see and does not behave
// identically across terminals.
//
// That trade is a bad one to make silently for a CODING assistant, where
// copying the code the model just wrote is among the most common things anyone
// does with the output. Someone who does not know about Shift does not conclude
// "mouse reporting is on"; they conclude "I cannot copy from this program".
//
// So it is now a toggle with a name, reachable as /mouse, reported in /help and
// named in the idle hint. See docs/ADR-001-mouse-capture.md for the argument
// that the DEFAULT should also change, which is a product decision rather than
// an engineering one and is left explicitly open there.
func (m chatModel) handleMouseToggle() (tea.Model, tea.Cmd) {
	m.mouseCaptured = !m.mouseCaptured
	var cmd tea.Cmd
	var reply string
	if m.mouseCaptured {
		cmd = tea.EnableMouseCellMotion
		reply = "mouse capture ON — the wheel scrolls the transcript; hold shift to select text"
	} else {
		cmd = tea.DisableMouse
		reply = "mouse capture OFF — select and copy with the mouse as usual; pgup/pgdown scroll"
	}
	m.appendTurn(turn{role: roleSystem, text: reply})
	m.resizeViewport()
	m.refreshViewport()
	return m, cmd
}
