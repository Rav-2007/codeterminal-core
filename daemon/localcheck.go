package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mochiii/protocol"
)

// THE TOOL CHECK (1.4.0): does the model in force really call tools?
//
// No list or size rule can say. Measured 2026-10-24, qwen2.5-coder:3b -- listed
// by Ollama with the "tools" capability -- made no valid tool call in 15 tries
// across five setups, so the only reliable answer is to try: the model is asked,
// up to localCheckTries times, to do the one thing a tool is needed for, with
// a single tool, and passes when it calls that tool with the right argument in
// localCheckPasses of them. A call it writes as JSON text counts, as it does in
// a real turn (localtextcall.go).
//
// TWO STAGES, BECAUSE THE INSTRUCTIONS DECIDE IT TOO. Measured the same day,
// qwen2.5-coder:7b wrote a right call in two of three tries with short
// instructions, and asked for the file to be pasted every time under the full
// agent's -- with one tool or fifteen. So a model is tried first with the
// compact agent's instructions, and only a model that passes, with a window
// that holds the full agent, is tried with the full agent's as well.
//
// The server's own answer comes first and can settle it: an Ollama model
// without the "tools" capability is refused any request carrying tools, so it
// is not tried at all. Nothing here leaves this machine: the check is asked
// only for a server on it.

const (
	localCheckTries  = 3
	localCheckPasses = 2
	// localCheckAnswerTokens caps each try's reply: a call fits easily, and a
	// model that starts writing prose instead stops soon.
	localCheckAnswerTokens = 256
	// localFullAgentNeedTokens is the least the full agent works in: its first
	// request alone is 6,000 to 7,000 tokens (localprofile.go), and every file
	// it reads adds to that. localAgentNeedTokens (localfit.go) is the compact
	// agent's.
	localFullAgentNeedTokens = 24576
	// localCheckTimeout bounds a whole check: loading the model, then the tries,
	// each a few seconds on a GPU and up to half a minute on a laptop's CPU.
	localCheckTimeout = localPreloadTimeout + 3*time.Minute
)

// localCheckQuestion is what each try asks: a question only the tool answers.
const localCheckQuestion = "Read the file notes/todo.txt in this project and tell me what its first line says."

// localCheckTool is the one tool each try offers, worded as the real read_file.
var localCheckTool = toolSpec{Type: "function", Function: toolSpecFunction{
	Name:        "builtin__read_file",
	Description: "Read a file in the user's project and return its text.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string",` +
		`"description":"The file's path, relative to the project."}},"required":["path"]}`),
}}

// checkLocal answers LocalRequest.Check for the model in force.
func (s *Server) checkLocal(ctx context.Context) protocol.LocalResponse {
	apiKey, base := s.credentials()
	if !isLoopbackBase(base) {
		return protocol.LocalResponse{Ok: true}
	}
	model := s.route("", "").Slug
	cctx, cancel := context.WithTimeout(ctx, localCheckTimeout)
	defer cancel()
	facts := localModelFactsAt(cctx, base, model)
	rememberLocalModel(base, model, facts.localWindow)
	check := &protocol.LocalCheck{
		APIBase: base, Kind: facts.kind.id, Model: model, Window: facts.window,
		Params: facts.params, Tools: string(facts.tools), Raise: localRaiseAdvice(facts.kind.id),
	}
	if facts.tools == localToolsNo {
		localKnown.noTools.Store(dialectKey(base, model), true)
	} else {
		localKnown.noTools.Delete(dialectKey(base, model))
		check.Tried, check.Passed = s.localCheckTries(cctx, base, apiKey, model, compactAgentPrompt)
		if check.Passed >= localCheckPasses && facts.window >= localFullAgentNeedTokens {
			check.FullTried, check.FullPassed = s.localCheckTries(cctx, base, apiKey, model, s.fullAgentPrompt())
		}
	}
	check.Way, check.Reason = localWay(facts, check, serverName(facts.kind))
	s.logger.Printf("local model: %s at %s: tools %q, called the tool in %d of %d tries (full instructions: %d of %d), window %d: %s",
		model, base, facts.tools, check.Passed, check.Tried, check.FullPassed, check.FullTried, facts.window, check.Way)
	return protocol.LocalResponse{Ok: true, Check: check}
}

// localCheckTries asks up to localCheckTries times under system, stopping as
// soon as the outcome is settled, and counts the right calls.
func (s *Server) localCheckTries(ctx context.Context, base, apiKey, model, system string) (tried, passed int) {
	for tried < localCheckTries {
		tried++
		if s.localCheckTry(ctx, base, apiKey, model, system) {
			passed++
		}
		if passed >= localCheckPasses || tried-passed > localCheckTries-localCheckPasses {
			break
		}
	}
	return tried, passed
}

// fullAgentPrompt is the instructions the full agent opens with.
func (s *Server) fullAgentPrompt() string {
	if s.systemPrompt != "" {
		return s.systemPrompt
	}
	return defaultSystemPrompt
}

// localCheckTry asks once, and reports whether the reply was the right call.
func (s *Server) localCheckTry(ctx context.Context, base, apiKey, model, system string) bool {
	var text strings.Builder
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: localCheckQuestion},
	}
	calls, err := streamCompletion(ctx, base, apiKey, model, messages, []toolSpec{localCheckTool},
		providerRouting{maxTokens: localCheckAnswerTokens},
		func(token string) error { text.WriteString(token); return nil }, nil, nil, nil)
	if err != nil {
		s.logger.Printf("local model: a tool check try failed: %s", asModelError(err).Detail())
		return false
	}
	if len(calls) == 0 {
		call, ok := textToolCall(text.String(), offeredTool([]toolSpec{localCheckTool}))
		if !ok {
			return false
		}
		calls = []toolCall{call}
	}
	name := calls[0].Function.Name
	return (name == localCheckTool.Function.Name || name == "read_file") && strings.Contains(calls[0].Function.Arguments, "todo.txt")
}

// localWay is how a model with these facts and check results should work, and
// why, in a sentence for the person.
func localWay(f localModelFacts, c *protocol.LocalCheck, server string) (way, reason string) {
	chat := "it answers with your code and proposes edits for you to review"
	tried, passed := c.Tried, c.Passed
	switch {
	case f.tools == localToolsNo:
		return protocol.LocalWayChat, fmt.Sprintf("%s says this model cannot use tools, so %s", server, chat)
	case passed < localCheckPasses:
		return protocol.LocalWayChat, fmt.Sprintf("it did not call the tool it was given (%d of %d tries), so %s. "+
			"Agent mode needs a model that can call tools", passed, tried, chat)
	case f.window > 0 && f.window < localAgentNeedTokens:
		return protocol.LocalWayChat, fmt.Sprintf("it can call tools, but its %s-token window is too small for agent mode, "+
			"which needs %s, so %s", groupThousands(f.window), groupThousands(localAgentNeedTokens), chat)
	case c.FullPassed >= localCheckPasses:
		return protocol.LocalWayFull, fmt.Sprintf("it called the tool in %d of %d tries with the full agent's instructions, "+
			"and its %s-token window has room for them", c.FullPassed, c.FullTried, groupThousands(f.window))
	case c.FullTried > 0:
		return protocol.LocalWayCompact, fmt.Sprintf("it called the tool with short instructions (%d of %d tries) but not "+
			"with the full agent's (%d of %d), so it works as the compact agent: five tools and short instructions",
			passed, tried, c.FullPassed, c.FullTried)
	case f.window == 0:
		return protocol.LocalWayCompact, fmt.Sprintf("it called the tool in %d of %d tries; its window could not be read, "+
			"so it works as the compact agent: five tools and short instructions", passed, tried)
	}
	return protocol.LocalWayCompact, fmt.Sprintf("it called the tool in %d of %d tries; with a %s-token window it works as "+
		"the compact agent: five tools and short instructions (the full agent needs %s)", passed, tried,
		groupThousands(f.window), groupThousands(localFullAgentNeedTokens))
}

// serverName is how a kind of server is written in a sentence.
func serverName(k localServerKind) string {
	if k.name != "" {
		return k.name
	}
	return "the model server"
}
