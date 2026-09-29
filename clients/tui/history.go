package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mochiii/protocol"

	tea "github.com/charmbracelet/bubbletea"
)

// /history: THE CHATS THE USER CHOSE TO SAVE, so work left half done can be
// found and picked up again. Nothing is saved unless the user asks -- that
// keeps disk use their choice -- and ctrl+n warns before discarding a chat
// that is not saved (handleCtrlN). The daemon keeps them (daemon/chatarchive.go:
// gzipped, one file per chat, under ~/.local/state/mochiii/history, bounded);
// this file only asks for them and draws them.
//
//	/history              list: the current chat, then saved ones, newest first
//	/history save [name]  save the current chat; again later updates that copy
//	/history <n>          read saved chat n
//	/history resume <n>   make saved chat n the current one (its copy stays)
//	/history delete <n>   delete saved chat n
//
// EVERYTHING HERE IS A roleSystem NOTE, never an assistant turn. buildHistory
// sends user and assistant turns to the model with the next prompt; a list of
// old chats, or an old chat read back, must not ride along into this one.

const (
	// maxHistoryRows bounds how many saved chats a list shows.
	maxHistoryRows = 20
	// historyAnswerRunes clips each answer when a saved chat is read back;
	// prompts are shown whole, since they are what the user is looking for.
	historyAnswerRunes = 600
)

const historyUsage = "usage: /history · /history save [name] · /history <n> · /history resume <n> · /history delete <n>"

func (m chatModel) handleHistoryCommand(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	var note string
	switch {
	case len(fields) == 0 || (len(fields) == 1 && fields[0] == "list"):
		note = m.listChats()
	case fields[0] == "save":
		if m.state == stateSending || m.state == stateStreaming {
			note = "wait for this turn to finish (or press esc), then save the chat"
			break
		}
		note = m.saveChat(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), "save")))
	case len(fields) == 1:
		note = m.showChat(fields[0])
	case len(fields) == 2 && (fields[0] == "resume" || fields[0] == "delete"):
		if m.state == stateSending || m.state == stateStreaming {
			note = "wait for this turn to finish (or press esc), then " + fields[0] + " a chat"
			break
		}
		if fields[0] == "resume" {
			return m.resumeChat(fields[1])
		}
		note = m.deleteChat(fields[1])
	default:
		note = historyUsage
	}
	m.appendTurn(turn{role: roleSystem, text: note})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// listChats asks for the list, remembers which id each number stands for, and
// renders it.
func (m *chatModel) listChats() string {
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistoryList, Workspace: m.workspaceRoot, Spec: m.activeSpec,
	})
	if err != nil {
		return "history: " + err.Error()
	}
	m.chatIDs = m.chatIDs[:0]
	var b strings.Builder
	saved := 0
	for _, e := range resp.Entries {
		if !e.Current {
			saved++
		}
	}
	if saved == 0 {
		b.WriteString("no saved chats in this project yet -- /history save [name] keeps the current chat")
	} else {
		b.WriteString("past chats -- /history <n> reads one, /history resume <n> continues it:")
	}
	now := time.Now()
	shown := 0
	number := map[string]int{} // saved chat id -> the number this list gives it
	for _, e := range resp.Entries {
		if !e.Current && len(number) < maxHistoryRows {
			number[e.ID] = len(number) + 1
		}
	}
	for _, e := range resp.Entries {
		label := "*" // the current chat, marked the way /model marks the current tier
		if !e.Current {
			if shown == maxHistoryRows {
				fmt.Fprintf(&b, "\n  … and %d older (the oldest are dropped past 50)", saved-shown)
				break
			}
			shown++
			m.chatIDs = append(m.chatIDs, e.ID)
			label = strconv.Itoa(shown)
		}
		fmt.Fprintf(&b, "\n  %-4s %-10s %3d turns  %q", label, chatWhen(e, now), e.Turns, e.Title)
		if e.Current {
			b.WriteString("  " + currentChatState(e, number))
		}
		// On a line of their own: beside the title they wrap mid-phrase in a
		// narrow terminal, and they are the part of the row being looked for.
		if marks := chatMarkers(e); marks != "" {
			b.WriteString("\n       ⚠ " + marks)
		}
		if e.LastPrompt != "" && e.LastPrompt != e.Title {
			fmt.Fprintf(&b, "\n       last: %q", e.LastPrompt)
		}
	}
	return b.String()
}

// currentChatState says whether the chat on screen is kept: saved, saved with
// newer turns since, or not saved at all.
func currentChatState(e protocol.HistoryEntry, number map[string]int) string {
	switch {
	case e.SavedAs != "" && !e.Unsaved:
		if n, ok := number[e.SavedAs]; ok {
			return fmt.Sprintf("(saved as %d)", n)
		}
		return "(saved)"
	case e.SavedAs != "":
		return "(changed since saved -- /history save updates it)"
	}
	return "(not saved -- /history save keeps it)"
}

// saveChat saves the chat on screen, or updates its saved copy.
func (m *chatModel) saveChat(name string) string {
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistorySave, Workspace: m.workspaceRoot, Spec: m.activeSpec, Name: name,
	})
	if err != nil {
		return "history: " + err.Error()
	}
	m.chatIDs = nil // the list's numbers move with a new or updated save
	msg := "saved"
	if e := resp.Entry; e != nil {
		msg = fmt.Sprintf("saved %q (%d turns) -- /history lists it; /history save again later updates it", e.Title, e.Turns)
	}
	if resp.Pruned > 0 {
		msg += fmt.Sprintf("\n(the oldest %d saved chat(s) were removed to keep history within its size limit)", resp.Pruned)
	}
	return msg
}

// currentChatUnsaved asks the daemon whether the chat on screen holds anything
// its saved copy does not. When the daemon cannot say, a chat with any question
// in it counts as unsaved: warning once too often costs a keypress, warning
// once too few costs the chat.
func (m *chatModel) currentChatUnsaved() bool {
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistoryList, Workspace: m.workspaceRoot, Spec: m.activeSpec,
	})
	if err == nil {
		for _, e := range resp.Entries {
			if e.Current {
				return e.Unsaved
			}
		}
		return false
	}
	for _, t := range m.turns {
		if t.role == roleUser {
			return true
		}
	}
	return false
}

// chatIDFor turns the number the user typed into the saved chat it stood for
// in the list they saw. With no list seen yet, one is fetched silently.
func (m *chatModel) chatIDFor(arg string) (string, string) {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return "", historyUsage
	}
	if len(m.chatIDs) == 0 {
		_ = m.listChats() // only for the numbering; an error shows up as "no chat n" below
	}
	if n > len(m.chatIDs) {
		return "", fmt.Sprintf("there is no saved chat %d -- /history lists them", n)
	}
	return m.chatIDs[n-1], ""
}

func (m *chatModel) showChat(arg string) string {
	id, problem := m.chatIDFor(arg)
	if problem != "" {
		return problem
	}
	resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistoryShow, Workspace: m.workspaceRoot, ID: id,
	})
	if err != nil {
		return "history: " + err.Error()
	}
	var b strings.Builder
	if e := resp.Entry; e != nil {
		fmt.Fprintf(&b, "saved chat %s -- %q, %d turns, %s", arg, e.Title, e.Turns, chatWhen(*e, time.Now()))
		if marks := chatMarkers(*e); marks != "" {
			b.WriteString("  ⚠ " + marks)
		}
	}
	for _, t := range resp.Turns {
		switch t.Role {
		case "user":
			b.WriteString("\n\n› " + t.Content)
		case "assistant":
			b.WriteString("\n\n" + clipRunes(t.Content, historyAnswerRunes))
		}
	}
	fmt.Fprintf(&b, "\n\n(/history resume %s to continue it)", arg)
	return b.String()
}

// resumeChat makes a saved chat the current one: the screen shows it, ↑↓
// recalls its prompts, and its spec is active again if it still exists. Its
// saved copy stays; /history save later updates it. The chat on screen is
// REPLACED, so when it is not saved the first resume only says so -- the same
// command again goes ahead, as a second ctrl+n does.
func (m chatModel) resumeChat(arg string) (tea.Model, tea.Cmd) {
	id, problem := m.chatIDFor(arg)
	if problem == "" && m.resumeArmed != id && m.currentChatUnsaved() {
		m.resumeArmed = id
		problem = "the chat on screen is not saved -- /history save keeps it; " +
			"/history resume " + arg + " again replaces it"
	}
	if problem == "" {
		m.resumeArmed = ""
		resp, err := sendHistory(m.clientName, protocol.HistoryRequest{
			Action: protocol.HistoryResume, Workspace: m.workspaceRoot, ID: id, Spec: m.activeSpec,
		})
		if err != nil {
			problem = "history: " + err.Error()
		} else {
			m.chatIDs = nil             // every number has moved
			m.chatUsage = usageTotals{} // a different chat; the session's total carries on
			m.stopsInChat = 0
			m.turns = turnsFromProtocol(resp.Turns)
			m.promptHistory = promptsFrom(resp.Turns)
			m.historyIdx, m.historyDraft = -1, ""
			m.lastGrounding, m.lastRedactions, m.lastDegraded = nil, nil, nil
			m.lastProvider, m.lastHistoryTruncated = "", false
			m.appendTurn(turn{role: roleSystem, text: m.resumedNote(resp.Entry, resp.Turns)})
			m.resizeViewport()
			m.refreshViewport()
			return m, nil
		}
	}
	m.appendTurn(turn{role: roleSystem, text: problem})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// resumedNote says where the chat left off, and restores its spec.
func (m *chatModel) resumedNote(e *protocol.HistoryEntry, turns []protocol.Turn) string {
	if e == nil {
		return fmt.Sprintf("resumed a saved chat (%d turns)", len(turns))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resumed %q (%d turns) -- /history save updates its saved copy", e.Title, e.Turns)
	if e.LastPrompt != "" {
		fmt.Fprintf(&b, "\nyou left off at: %q", e.LastPrompt)
	}
	if e.Incomplete != "" {
		b.WriteString("\n⚠ its last answer did not finish (" + incompleteReason(e.Incomplete) + ") -- ask it to continue")
	}
	if e.Spec != "" {
		if _, err := os.Stat(filepath.Join(m.workspaceRoot, filepath.FromSlash(e.Spec))); err == nil && isSpecPath(e.Spec) {
			m.activateSpec(e.Spec)
			b.WriteString("\nactive spec: " + e.Spec)
			if e.SpecTotal > 0 {
				fmt.Fprintf(&b, " (%d of %d criteria still open)", e.SpecOpen, e.SpecTotal)
			}
		} else {
			b.WriteString("\nits spec " + e.Spec + " no longer exists")
		}
	}
	return b.String()
}

func (m *chatModel) deleteChat(arg string) string {
	id, problem := m.chatIDFor(arg)
	if problem != "" {
		return problem
	}
	if _, err := sendHistory(m.clientName, protocol.HistoryRequest{
		Action: protocol.HistoryDelete, Workspace: m.workspaceRoot, ID: id,
	}); err != nil {
		return "history: " + err.Error()
	}
	m.chatIDs = nil // every later number has moved
	return "deleted saved chat " + arg
}

// chatMarkers says why a chat looks HALF DONE: its last answer did not finish,
// or its spec still has open criteria.
func chatMarkers(e protocol.HistoryEntry) string {
	var marks []string
	if e.Incomplete != "" {
		marks = append(marks, incompleteReason(e.Incomplete))
	}
	if e.Spec != "" && e.SpecOpen > 0 {
		marks = append(marks, fmt.Sprintf("spec %s: %d of %d open", e.Spec, e.SpecOpen, e.SpecTotal))
	}
	return strings.Join(marks, " · ")
}

// incompleteReason words an IncompleteInfo reason for a list.
func incompleteReason(reason string) string {
	switch reason {
	case protocol.IncompleteUserCancelled:
		return "you stopped it mid-turn"
	case protocol.IncompleteAgentBudget:
		return "it hit a turn limit"
	case protocol.IncompleteLength:
		return "the answer was cut off"
	case protocol.IncompleteBudgetExceeded:
		return "it hit the spending cap"
	case protocol.IncompleteProviderError:
		return "the provider failed mid-answer"
	case protocol.IncompleteContentFilter:
		return "a content filter stopped it"
	}
	return "it did not finish"
}

// chatWhen is how long ago a chat ended; the current chat is "now".
func chatWhen(e protocol.HistoryEntry, now time.Time) string {
	if e.Current {
		return "now"
	}
	t, err := time.Parse(time.RFC3339, e.Ended)
	if err != nil {
		return "?"
	}
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	return t.Local().Format("Jan 2")
}
