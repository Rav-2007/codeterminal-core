package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// A TOOL CALL WRITTEN AS TEXT (1.4.0).
//
// A model on this machine sometimes writes the call it means to make as plain
// JSON in its answer instead of the server's tool-call format. Measured
// 2026-10-24: asked with one tool, qwen2.5-coder:3b answered
// {"name": "builtin__read_file", "arguments": {"path": "greet.go"}} -- the
// right call, which Ollama did not recognise because the model left out the
// tags its template expects. Llama models write the same with "parameters".
//
// Such a reply is run as the call it names, for a server on this machine only
// and only for a tool on the turn's menu: the call then goes through the same
// lookup, policy and approval as any other, so writing a call as text gains a
// model nothing a real call would not.

const (
	// maxTextCallScan bounds how much of a reply is searched for a written call.
	maxTextCallScan = 8 << 10
	// maxTextCallObjects bounds how many JSON objects in it are tried.
	maxTextCallObjects = 32
)

// textToolCall finds the first JSON object in text that names an offered tool,
// with "name" and "arguments" (or "parameters"), and returns it as a call.
// resolve maps a name the model wrote to the tool's qualified name, or reports
// that no offered tool has it.
func textToolCall(text string, resolve func(string) (string, bool)) (toolCall, bool) {
	if len(text) > maxTextCallScan {
		text = text[:maxTextCallScan]
	}
	tried := 0
	for i := strings.IndexByte(text, '{'); i >= 0 && tried < maxTextCallObjects; tried++ {
		var obj struct {
			Name       string          `json:"name"`
			Arguments  json.RawMessage `json:"arguments"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&obj) == nil && obj.Name != "" {
			args := obj.Arguments
			if len(args) == 0 {
				args = obj.Parameters
			}
			if qualified, ok := resolve(obj.Name); ok {
				if encoded, ok := callArguments(args); ok {
					return toolCall{ID: "text-call", Type: "function", Function: toolCallFunction{Name: qualified, Arguments: encoded}}, true
				}
			}
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return toolCall{}, false
}

// callArguments is a written call's arguments as the JSON string a tool call
// carries: an object as it was written, or a string that holds one.
func callArguments(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return "{}", true
	case raw[0] == '{':
		return string(raw), true
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.HasPrefix(strings.TrimSpace(s), "{") && json.Valid([]byte(s)) {
			return s, true
		}
	}
	return "", false
}

// offeredTool resolves a written tool name against a turn's menu: its
// qualified name, or a built-in's bare name.
func offeredTool(tools []toolSpec) func(string) (string, bool) {
	return func(name string) (string, bool) {
		for _, t := range tools {
			if t.Function.Name == name || t.Function.Name == "builtin__"+name {
				return t.Function.Name, true
			}
		}
		return "", false
	}
}

// rescueTextCall is the agent loop's step for a reply with no tool call: when
// a server on this machine sent text that is a call to a tool on the menu, the
// text is taken back out of the answer and the call is returned to be run.
// Every other reply, and every other provider's, is returned as it was.
func (s *Server) rescueTextCall(apiBase string, tools []toolSpec, calls []toolCall, full *strings.Builder, textBefore int) []toolCall {
	if len(calls) > 0 || len(tools) == 0 || !isLoopbackBase(apiBase) || full.Len() <= textBefore {
		return calls
	}
	answer := full.String()
	call, ok := textToolCall(answer[textBefore:], offeredTool(tools))
	if !ok {
		return calls
	}
	s.logger.Printf("agent: the model wrote a call to %s as text; running it as the call it names", call.Function.Name)
	full.Reset()
	full.WriteString(answer[:textBefore])
	return []toolCall{call}
}
