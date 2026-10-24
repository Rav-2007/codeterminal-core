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
// that holds the full agent, is tried as the full agent as well.
//
// THE FULL STAGE IS THE FULL AGENT'S OWN SITUATION (localFullCheck). It first
// offered one tool and asked to read a named file, as the compact stage does,
// and the 7B passed it at a 24,576-token window (2 of 2) -- then, working as
// the full agent in the real app, called no tool at all: it made up a
// greet.go and answered about it, and "ran" ls without running it. Asked with
// the full agent's instructions and menu about code it had not seen, it
// guessed the answer in 5 of 5 tries. So the full stage now offers the full
// agent's menu, asks such a question, and needs a look-up in every try: a
// model that guesses once is the compact agent, which still works.
//
// The server's own answer comes first and can settle it: an Ollama model
// without the "tools" capability is refused any request carrying tools, so it
// is not tried at all. Nothing here leaves this machine: the check is asked
// only for a server on it.

const (
	localCheckTries  = 3
	localCheckPasses = 2
	// localFullCheckPasses is how many of localCheckTries the full stage needs:
	// all of them, so it stops at the first guess.
	localFullCheckPasses = localCheckTries
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

// localCheckQuestion is what each compact-stage try asks: a question only the
// tool answers.
const localCheckQuestion = "Read the file notes/todo.txt in this project and tell me what its first line says."

// localFullCheckQuestion is what each full-stage try asks: about code the model
// has not seen, as a real question is, which only a look-up answers right.
const localFullCheckQuestion = "What does parseConfig in config/loader.go return when the file is empty?"

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
		check.Tried, check.Passed = s.localCheckTries(cctx, base, apiKey, model, localCompactCheck)
		if check.Passed >= localCheckPasses && facts.window >= localFullAgentNeedTokens {
			check.FullTried, check.FullPassed = s.localCheckTries(cctx, base, apiKey, model, s.localFullCheck(cctx))
		}
	}
	check.Way, check.Reason = localWay(facts, check, serverName(facts.kind))
	s.logger.Printf("local model: %s at %s: tools %q, called the tool in %d of %d tries (full instructions: %d of %d), window %d: %s",
		model, base, facts.tools, check.Passed, check.Tried, check.FullPassed, check.FullTried, facts.window, check.Way)
	return protocol.LocalResponse{Ok: true, Check: check}
}

// localCheckStage is one stage of the check: what each try sends, which call
// passes it, and how many of localCheckTries must pass.
type localCheckStage struct {
	system, question string
	tools            []toolSpec
	passes           func(toolCall) bool
	need             int
}

// localCompactCheck is the compact agent's stage: its instructions, one tool,
// and a file to read by name.
var localCompactCheck = localCheckStage{
	system: compactAgentPrompt, question: localCheckQuestion, tools: []toolSpec{localCheckTool},
	passes: func(c toolCall) bool {
		return strings.TrimPrefix(c.Function.Name, "builtin__") == "read_file" && strings.Contains(c.Function.Arguments, "todo.txt")
	},
	need: localCheckPasses,
}

// localFullCheck is the full agent's stage: its instructions, the menu it would
// offer this client's model now, and a question about code it has not seen.
func (s *Server) localFullCheck(ctx context.Context) localCheckStage {
	return localCheckStage{system: s.fullAgentPrompt(), question: localFullCheckQuestion, tools: s.fullAgentMenu(ctx),
		passes: looksUpTheCode, need: localFullCheckPasses}
}

// looksUpTheCode reports whether c looks up what localFullCheckQuestion names
// rather than guessing it: a read or search that names it, or a look at the
// project's layout.
func looksUpTheCode(c toolCall) bool {
	switch strings.TrimPrefix(c.Function.Name, "builtin__") {
	case "list_directory", "find_files", "repo_map":
		return true
	case "read_file", "search_code", "grep", "query_compiler_definition", "query_compiler_references":
		return containsAny(c.Function.Arguments, []string{"loader", "parseConfig", "config"})
	}
	return false
}

// fullAgentMenu is the tools a full agent's turn offers a model on this
// computer: the built-in ones, without the internet tools -- they cannot
// answer the stage's question, and such a model has them only after /web on --
// and never the compact menu, whatever this connection declared. The user's
// own MCP servers are not on it: a check never starts them. A daemon whose
// agent has no tools offers none, and the stage then cannot pass.
func (s *Server) fullAgentMenu(ctx context.Context) []toolSpec {
	cfg, _ := s.agentConfig()
	if cfg == nil {
		return nil
	}
	registry := s.builtinRegistry(cfg, s.logger, nil, "")
	defer func() { _ = registry.Close() }()
	ctx = withTurnMenu(context.WithValue(ctx, noNetworkToolsKey{}, true), turnMenu{})
	specs, _, _ := s.advertisedToolSpecs(ctx, registry, nil)
	return specs
}

// localCheckTries asks up to localCheckTries times, stopping as soon as the
// stage's outcome is settled, and counts the tries that passed.
func (s *Server) localCheckTries(ctx context.Context, base, apiKey, model string, stage localCheckStage) (tried, passed int) {
	for tried < localCheckTries {
		tried++
		if s.localCheckTry(ctx, base, apiKey, model, stage) {
			passed++
		}
		if passed >= stage.need || tried-passed > localCheckTries-stage.need {
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

// localCheckTry asks once, and reports whether the reply was a call that
// passes the stage.
func (s *Server) localCheckTry(ctx context.Context, base, apiKey, model string, stage localCheckStage) bool {
	var text strings.Builder
	messages := []chatMessage{
		{Role: "system", Content: stage.system},
		{Role: "user", Content: stage.question},
	}
	calls, err := streamCompletion(ctx, base, apiKey, model, messages, stage.tools,
		providerRouting{maxTokens: localCheckAnswerTokens},
		func(token string) error { text.WriteString(token); return nil }, nil, nil, nil)
	if err != nil {
		s.logger.Printf("local model: a tool check try failed: %s", asModelError(err).Detail())
		return false
	}
	if len(calls) == 0 {
		call, ok := textToolCall(text.String(), offeredTool(stage.tools))
		if !ok {
			return false
		}
		calls = []toolCall{call}
	}
	return stage.passes(calls[0])
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
	case c.FullPassed >= localFullCheckPasses:
		return protocol.LocalWayFull, fmt.Sprintf("given the full agent's instructions and tools, it looked the code up "+
			"in %d of %d tries, and its %s-token window has room for them", c.FullPassed, c.FullTried, groupThousands(f.window))
	case c.FullTried > 0:
		return protocol.LocalWayCompact, fmt.Sprintf("it called the tool with short instructions (%d of %d tries), but given "+
			"the full agent's instructions and tools it guessed instead of looking (looked in %d of %d), so it works as "+
			"the compact agent: five tools and short instructions", passed, tried, c.FullPassed, c.FullTried)
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
