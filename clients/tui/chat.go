package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// chatState is where the chat model is in one request/response cycle.
// stateSplash is a one-time phase before the first cycle begins.
type chatState int

const (
	stateSplash     chatState = iota // welcome screen; any key dismisses it
	stateIdle                        // ready to accept a prompt
	stateSending                     // waiting for the first token or an immediate error
	stateStreaming                   // tokens are arriving
	stateError                       // last turn failed; shown in the header, input re-enabled
	stateEditReview                  // the last answer contained edit blocks; reviewing them one at a time
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
}

// helpText is the persistent hint shown under the input. Conversational
// memory is on: every prompt after the first sends the transcript so far as
// PromptRequest.History (see buildHistory), so follow-ups can build on
// earlier turns. ctrl+n clears the transcript to start a fresh conversation
// with no carried-over history.
const helpText = "enter to send · ctrl+n new conversation · ctrl+c to quit"

// reviewHelpText is shown instead of helpText while reviewing edit blocks.
const reviewHelpText = "y apply · n skip · q cancel remaining"

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

	clientName    string
	workspace     string // sent to the daemon so it can flag a workspace mismatch
	workspaceRoot string // real (symlink-resolved) workspace root edits are confined to
	width         int
	height        int
	ready         bool // true once the first WindowSizeMsg has sized the viewport
}

func newChatModel(clientName, workspace, workspaceRoot string) chatModel {
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
		state:         stateSplash,
		input:         ti,
		spinner:       sp,
		viewport:      vp,
		clientName:    clientName,
		workspace:     workspace,
		workspaceRoot: workspaceRoot,
	}
}

func (m chatModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		const headerLines, inputLines, helpLines = 1, 1, 1
		vpHeight := m.height - headerLines - inputLines - helpLines
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.viewport.Width = m.width
		m.viewport.Height = vpHeight
		m.input.Width = m.width - len(m.input.Prompt) - 2
		m.ready = true
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
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.streamCancel != nil {
				m.streamCancel()
			}
			return m, tea.Quit
		case "enter":
			return m.startTurn()
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
		m.input, cmd = m.input.Update(msg)
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
		return m, waitForNext(m.streamCh)

	case tokenMsg:
		return m.handleToken(msg)

	case streamDoneMsg:
		m.streamCancel = nil
		m.streamCh = nil
		return m.checkForEditBlocks()

	case streamErrMsg:
		m.state = stateError
		m.statusErr = msg.err.Error()
		m.streamCancel = nil
		m.streamCh = nil
		return m, m.input.Focus()

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

// startTurn handles Enter while idle or errored: it's a no-op while busy or
// on empty input, otherwise it appends the user's turn and kicks off the
// stream.
func (m chatModel) startTurn() (tea.Model, tea.Cmd) {
	if m.state == stateSending || m.state == stateStreaming {
		return m, nil
	}
	prompt := strings.TrimSpace(m.input.Value())
	if prompt == "" {
		return m, nil
	}

	// Built from the transcript BEFORE the current prompt is appended below,
	// so the not-yet-answered prompt can never end up in its own History.
	history := buildHistory(m.turns)

	m.turns = append(m.turns, turn{role: roleUser, text: prompt})
	m.input.SetValue("")
	m.input.Blur()
	m.state = stateSending
	m.statusErr = ""
	m.lastGrounding = nil
	m.refreshViewport()

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	ch := make(chan tea.Msg)
	m.streamCh = ch

	return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, m.workspace, prompt, history, ch))
}

// buildHistory converts the transcript so far into the PromptRequest.History
// the next prompt will carry, oldest first. roleSystem turns (edit-review
// summaries, parse-error notices) are TUI-only chrome, not conversation
// content, so they're dropped here rather than sent — the daemon only
// accepts "user"/"assistant" roles anyway (daemon/history.go) and would
// drop anything else itself.
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
			history = append(history, protocol.Turn{Role: "assistant", Content: t.text})
		}
	}
	return history
}

// clearConversation handles ctrl+n: it starts a fresh conversation with no
// carried-over history. A no-op while a request/stream is in flight (mid-
// stream review is unreachable here — stateEditReview routes to
// handleReviewKey instead, which doesn't bind ctrl+n), matching the same
// ignore-while-busy rule Enter follows.
func (m chatModel) clearConversation() (tea.Model, tea.Cmd) {
	if m.state == stateSending || m.state == stateStreaming {
		return m, nil
	}
	m.turns = nil
	m.lastGrounding = nil
	m.statusErr = ""
	m.state = stateIdle
	m.refreshViewport()
	return m, nil
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
		m.turns = append(m.turns, turn{role: roleAssistant})
	}
	if n := len(m.turns); n > 0 {
		m.turns[n-1].text += string(msg)
	}
	m.refreshViewport()
	return m, waitForNext(m.streamCh)
}

// checkForEditBlocks runs once a stream finishes: it parses the just-
// completed assistant turn for SEARCH/REPLACE edit blocks (the same
// editapply.ParseEditBlocks the daemon already runs to log them — see
// logEditBlocks in daemon/server.go). No blocks, or a parse error, means
// there's nothing to review: back to normal idle chat, unchanged from
// before this feature existed. Blocks found means entering the modal
// edit-review state instead.
func (m chatModel) checkForEditBlocks() (tea.Model, tea.Cmd) {
	m.state = stateIdle
	text := lastAssistantText(m.turns)
	if text == "" {
		return m, m.input.Focus()
	}

	blocks, err := editapply.ParseEditBlocks(text)
	if err != nil {
		m.turns = append(m.turns, turn{role: roleSystem, text: fmt.Sprintf("(could not parse edit blocks: %v)", err)})
		m.refreshViewport()
		return m, m.input.Focus()
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

	if err := editapply.BackupOriginal(m.reviewBackupDir, m.workspaceRoot, p); err != nil {
		m.reviewRefused++
		m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%s: backing up: %v", p.Block.FilePath, err))
		m.reviewIndex++
		return m.advanceReview()
	}
	if err := os.WriteFile(p.TargetPath, []byte(p.NewContent), p.FileMode); err != nil {
		m.reviewRefused++
		m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%s: writing: %v", p.Block.FilePath, err))
		m.reviewIndex++
		return m.advanceReview()
	}
	if err := editapply.BackupAfter(m.reviewBackupDir, m.workspaceRoot, p); err != nil {
		m.reviewRefusals = append(m.reviewRefusals, fmt.Sprintf("%s: post-apply backup snapshot failed: %v (edit still applied)", p.Block.FilePath, err))
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
	b.WriteString("\n" + helpStyle.Render("syntax check: "+p.SyntaxNote))
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
	} else {
		bottomLine = m.input.View()
		help = helpStyle.Render(helpText)
	}

	return header + "\n" + m.viewport.View() + "\n" + bottomLine + "\n" + help
}

func (m chatModel) renderHeader() string {
	brand := brandStyle.Render(lotusGlyph + " " + brandName)
	parts := []string{brand, m.stateLabel()}
	if grounding := m.groundingLabel(); grounding != "" {
		parts = append(parts, grounding)
	}
	if history := m.historyLabel(); history != "" {
		parts = append(parts, history)
	}
	return strings.Join(parts, "  ")
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
		return accentStyle.Render(fmt.Sprintf("grounded ✓ %d chunk(s)", g.Chunks))
	}
	return helpStyle.Render(fmt.Sprintf("ungrounded (%s)", g.Reason))
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
	default:
		return helpStyle.Render("idle")
	}
}
