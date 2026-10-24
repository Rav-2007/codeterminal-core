package main

import (
	"encoding/json"
	"strings"

	"mochiii/protocol"
)

// A TURN THAT STOPS OFFERING TOOLS (1.4.0).
//
// A model on this machine that cannot drive its tools does not stop trying.
// Measured 2026-10-24, qwen2.5-coder:3b "called" the function under discussion
// as though it were a tool, and a model told that a tool does not exist tends
// to name another that does not either. Each such call is a round trip -- on a
// laptop's CPU, tens of seconds -- that teaches the model nothing, until the
// turn's step limit ends it with no answer.
//
// So after maxUnusableCalls unusable calls in a row, the rest of the turn is
// asked with no tools and told to answer from what it has, and the person is
// told once. A call is unusable when it could never run as written: its tool
// is not on the turn's menu, its arguments are not a JSON object, or it leaves
// out an argument the tool requires. A call that can run -- even one that then
// fails, or that the person refuses -- resets the count.
//
// For a server on this machine only, like the text-call rescue it runs beside.

// maxUnusableCalls is how many unusable calls in a row end a turn's tools.
const maxUnusableCalls = 2

// toolsStoppedNote is said to the model, after its last calls' results, in
// every request once its tools are gone.
const toolsStoppedNote = "Your last tool calls could not be run: each named a tool that is not available, " +
	"or left out its arguments. No tools are available for the rest of this question. Answer it now, in " +
	"plain text, from what you have already read; if that is not enough, say what is missing."

// toolsStoppedDetail is what the person is told, once, when it happens.
const toolsStoppedDetail = "the model made tool calls in a row that could not be run, so it was asked to " +
	"answer from what it had already read, without more tools -- if this keeps happening, /agent off asks " +
	"this model as a chat"

// localCallGuard is one agent loop's count of unusable calls, and the words
// it is holding back from the screen (show). The zero value offers every tool.
type localCallGuard struct {
	unusable int
	stopped  bool

	// held is the step's words not shown yet: from where a call written as
	// text may start. holding says they do start one, so they are held until
	// the step ends.
	held    strings.Builder
	holding bool
	onToken func(string) error
}

// show is how the agent loop puts a step's words on the screen as they arrive.
//
// A CALL WRITTEN AS TEXT IS NOT SHOWN. Measured 2026-10-24 in the real app,
// qwen2.5-coder:7b wrote each of its calls as JSON text -- alone, after a
// sentence of what it was about to do, or inside a code fence -- and it was on
// the screen before it ran, once followed by a made-up result: three files for
// an ls that then listed two others. The rescue took the call out of the
// answer kept, but not off the screen.
//
// So the words stream as they arrive until a "{" or a code fence, and from
// there are held only as long as they may still open a call (callOpening):
// a few characters decide it. Words that do not are shown at once; a call is
// held with everything after it until the step ends, and then dropped if it
// was run (endStep). The answer kept is cut at the same place (localtextcall.go).
//
// Only for a server on this machine with tools on the menu. A call held when
// its stream fails part-way is kept in the answer but not shown.
func (g *localCallGuard) show(apiBase string, tools []toolSpec, token string, onToken func(string) error) error {
	if !isLoopbackBase(apiBase) || len(tools) == 0 || g.stopped {
		return onToken(token)
	}
	g.onToken = onToken
	if g.holding {
		g.held.WriteString(token)
		return nil
	}
	out, pending, call := splitAtCall(g.held.String() + token)
	g.held.Reset()
	g.held.WriteString(pending)
	g.holding = call
	if out == "" {
		return nil
	}
	return onToken(out)
}

// endStep shows what the step held, unless it was a call that ran; the next
// step starts with nothing held.
func (g *localCallGuard) endStep(ran bool) {
	if g.held.Len() > 0 && !(ran && g.holding) && g.onToken != nil {
		_ = g.onToken(g.held.String()) // a client that has gone is found by the turn's next write
	}
	g.held.Reset()
	g.holding = false
}

// The answers callOpening gives.
const (
	openingNone  = iota // these words do not open a call
	openingMaybe        // they may: the words so far are too few to say
	openingCall         // they open a call
)

// splitAtCall divides text into what can be shown and what starts at the first
// place that opens, or may still open, a call written as text; call reports
// that it does open one.
func splitAtCall(text string) (shown, pending string, call bool) {
	for from := 0; ; {
		i := strings.IndexAny(text[from:], "{`")
		if i < 0 {
			return text, "", false
		}
		at := from + i
		switch callOpening(text[at:]) {
		case openingCall:
			return text[:at], text[at:], true
		case openingMaybe:
			return text[:at], text[at:], false
		}
		from = at + 1
	}
}

// callOpening says whether s, which starts with "{" or "`", opens a call
// written as text: an object whose first key is one textToolCall reads, alone
// or as the first thing in a code fence.
func callOpening(s string) int {
	if s[0] == '`' {
		ticks := len(s) - len(strings.TrimLeft(s, "`"))
		if ticks < 3 {
			if ticks == len(s) {
				return openingMaybe
			}
			return openingNone
		}
		rest := strings.TrimLeft(s[ticks:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if trimmed == "" {
			return openingMaybe
		}
		if trimmed[0] != '{' {
			return openingNone
		}
		s = trimmed
	}
	rest := strings.TrimLeft(s[1:], " \t\r\n")
	if rest == "" {
		return openingMaybe
	}
	if rest[0] != '"' {
		return openingNone
	}
	key := rest[1:]
	for _, k := range []string{`name"`, `arguments"`, `parameters"`} {
		if strings.HasPrefix(key, k) {
			return openingCall
		}
		if strings.HasPrefix(k, key) {
			return openingMaybe
		}
	}
	return openingNone
}

// step is the agent loop's step for a reply's calls, before any of them runs:
// a call written as text is rescued (localtextcall.go), and each call is
// counted. Once the turn's tools are stopped, a call the model makes anyway is
// dropped, not run, so the reply's text is its answer. Every other provider's
// calls are returned as they were.
func (g *localCallGuard) step(s *Server, apiBase string, tools []toolSpec, calls []toolCall, full *strings.Builder,
	textBefore int, onDegraded func(protocol.Degradation)) []toolCall {
	if !isLoopbackBase(apiBase) {
		return calls
	}
	if g.stopped {
		g.endStep(false)
		if len(calls) > 0 {
			s.logger.Printf("agent: the model made %d call(s) after its tools were stopped; none of them runs", len(calls))
		}
		return nil
	}
	native := len(calls)
	calls = s.rescueTextCall(apiBase, tools, calls, full, textBefore)
	g.endStep(native == 0 && len(calls) > 0)
	for _, call := range calls {
		if usableCall(call, tools) {
			g.unusable = 0
			continue
		}
		g.unusable++
		s.logger.Printf("agent: the model's call to %q could not be run as written (%d in a row)", call.Function.Name, g.unusable)
	}
	if g.unusable >= maxUnusableCalls {
		g.stopped = true
		s.logger.Printf("agent: %d unusable calls in a row; the rest of this turn is asked without tools", g.unusable)
		if onDegraded != nil {
			onDegraded(protocol.Degradation{Component: protocol.DegradedLocalToolsStopped, Detail: toolsStoppedDetail})
		}
	}
	return calls
}

// menu is the tools a request offers: none once they are stopped.
func (g *localCallGuard) menu(tools []toolSpec) []toolSpec {
	if g.stopped {
		return nil
	}
	return tools
}

// messages is what a request sends: the turn so far, and once the tools are
// stopped, toolsStoppedNote after it. The turn itself is not changed, so the
// note is never kept in what the turn remembers.
func (g *localCallGuard) messages(messages []chatMessage) []chatMessage {
	if !g.stopped {
		return messages
	}
	return append(messages[:len(messages):len(messages)], chatMessage{Role: "user", Content: toolsStoppedNote})
}

// usableCall reports whether call could run as written: its tool is on the
// menu, and its arguments are a JSON object with every argument the tool's
// schema requires.
func usableCall(call toolCall, tools []toolSpec) bool {
	name, offered := offeredTool(tools)(call.Function.Name)
	if !offered {
		return false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
		return false
	}
	for _, t := range tools {
		if t.Function.Name != name {
			continue
		}
		var schema struct {
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(t.Function.Parameters, &schema)
		for _, required := range schema.Required {
			if _, given := args[required]; !given {
				return false
			}
		}
	}
	return true
}
