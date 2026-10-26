package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"mochiii/daemon/mcp"
)

// TOOL ARGUMENTS IN THE MODEL'S SPELLING (2026-10-26).
//
// MiniMax called grep with "regex": "true" -- a string where the schema says
// boolean -- and the whole call was refused by grep's strict decode ("cannot
// unmarshal string into ... of type bool"). Two dozen handlers decode their
// arguments that way, so the fix is in one place, before any of them: a
// top-level value whose JSON type the tool's own schema does not allow, and
// whose text plainly is one it does, is rewritten to that type. Nothing is
// guessed past that -- not nested values, not "yes" for true, not a number
// with text after it -- and a value the schema already allows is never touched.
//
// IT HAPPENS BEFORE ANYONE IS ASKED. The rewritten arguments are what the
// approval prompt shows, what the grant is keyed on, what the tool runs and
// what the audit records: one string for all four, as dispatchToolCall needs.

// coerceArgs returns args with each such value rewritten, and how many were;
// args itself, byte for byte, when there was nothing to rewrite.
func coerceArgs(schema json.RawMessage, args string) (string, int) {
	var s struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return args, 0
	}
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var obj map[string]any
	if dec.Decode(&obj) != nil {
		return args, 0
	}
	n := 0
	for name, v := range obj {
		text, isString := v.(string)
		prop, declared := s.Properties[name]
		if !isString || !declared {
			continue
		}
		types := schemaTypes(prop.Type)
		if types["string"] || len(types) == 0 {
			continue // a string is what it may be
		}
		t := strings.TrimSpace(text)
		switch {
		case types["boolean"] && (t == "true" || t == "false"):
			obj[name] = t == "true"
		case types["integer"] && isInteger(t):
			obj[name] = json.Number(t)
		case types["number"] && isFiniteNumber(t):
			obj[name] = json.Number(t)
		default:
			continue
		}
		n++
	}
	if n == 0 {
		return args, 0
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "a && b" stays "a && b" for the prompt and the audit
	if enc.Encode(obj) != nil {
		return args, 0
	}
	return strings.TrimSuffix(buf.String(), "\n"), n
}

// schemaTypes is a JSON Schema "type", one name or a list of them, as a set.
func schemaTypes(raw json.RawMessage) map[string]bool {
	set := map[string]bool{}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		set[one] = true
		return set
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, t := range many {
			set[t] = true
		}
	}
	return set
}

func isInteger(t string) bool {
	_, err := strconv.ParseInt(t, 10, 64)
	return err == nil
}

func isFiniteNumber(t string) bool {
	f, err := strconv.ParseFloat(t, 64)
	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// coercedArguments is call's arguments in its tool's types, rewritten into
// call itself so every later step reads the same bytes (dispatchToolCall).
func (s *Server) coercedArguments(ctx context.Context, registry *mcp.Registry, call *toolCall) string {
	qualified, _ := registry.Canonicalize(call.Function.Name)
	spec, _, err := registry.Lookup(ctx, qualified)
	if err != nil {
		return call.Function.Arguments // refused later, as it always was
	}
	if fixed, n := coerceArgs(spec.Schema, call.Function.Arguments); n > 0 {
		s.logger.Printf("agent: %q had %d argument(s) in the wrong JSON type for its schema; read them as the schema's",
			call.Function.Name, n)
		call.Function.Arguments = fixed
	}
	return call.Function.Arguments
}
