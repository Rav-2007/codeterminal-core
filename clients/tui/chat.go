package main

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// chatState is where the chat model is in one request/response cycle.
// stateSplash is a one-time phase before the first cycle begins.
type chatState int

const (
	stateSplash    chatState = iota // welcome screen; any key dismisses it
	stateIdle                       // ready to accept a prompt
	stateSending                    // waiting for the first token or an immediate error
	stateStreaming                  // tokens are arriving
	stateError                      // last turn failed; shown in the header, input re-enabled
)

type turnRole int

const (
	roleUser turnRole = iota
	roleAssistant
)

type turn struct {
	role turnRole
	text string
}

// helpText is the persistent hint shown under the input. It deliberately
// says there's no memory yet: each turn sends only its own prompt (the
// daemon's wire protocol is one prompt per connection, with no history
// field — see daemonconn.go/stream.go), and the UI must not imply
// continuity it doesn't have.
const helpText = "enter to send · ctrl+c to quit · no chat memory yet (each message is independent)"

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

	clientName string
	width      int
	height     int
	ready      bool // true once the first WindowSizeMsg has sized the viewport
}

func newChatModel(clientName string) chatModel {
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
		state:      stateSplash,
		input:      ti,
		spinner:    sp,
		viewport:   vp,
		clientName: clientName,
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
		switch msg.String() {
		case "ctrl+c", "esc":
			if m.streamCancel != nil {
				m.streamCancel()
			}
			return m, tea.Quit
		case "enter":
			return m.startTurn()
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

	case tokenMsg:
		return m.handleToken(msg)

	case streamDoneMsg:
		m.state = stateIdle
		m.streamCancel = nil
		m.streamCh = nil
		return m, m.input.Focus()

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

	m.turns = append(m.turns, turn{role: roleUser, text: prompt})
	m.input.SetValue("")
	m.input.Blur()
	m.state = stateSending
	m.statusErr = ""
	m.refreshViewport()

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	ch := make(chan tea.Msg)
	m.streamCh = ch

	return m, tea.Batch(m.spinner.Tick, startStream(ctx, m.clientName, prompt, ch))
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

func (m *chatModel) refreshViewport() {
	m.viewport.SetContent(renderTranscript(m.turns))
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
		}
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
	help := helpStyle.Render(helpText)
	return header + "\n" + m.viewport.View() + "\n" + m.input.View() + "\n" + help
}

func (m chatModel) renderHeader() string {
	brand := brandStyle.Render(lotusGlyph + " " + brandName)
	return brand + "  " + m.stateLabel()
}

func (m chatModel) stateLabel() string {
	switch m.state {
	case stateSending:
		return accentStyle.Render(m.spinner.View() + " sending…")
	case stateStreaming:
		return accentStyle.Render(m.spinner.View() + " streaming…")
	case stateError:
		return errorStyle.Render("error: " + m.statusErr)
	default:
		return helpStyle.Render("idle")
	}
}
