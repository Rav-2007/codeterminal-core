package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// WHAT A MODEL ON THIS MACHINE CAN DO (1.4.0).
//
// Whether a model can drive the agent is a property of that model, not of its
// size or of anything in Mochiii: measured 2026-10-24, qwen2.5-coder:3b -- which
// Ollama lists with the "tools" capability -- made no valid tool call in 15
// tries, with one tool or fifteen, short instructions or the full ones. So the
// server is asked what it knows, and the tool check (localcheck.go) settles the
// rest by trying.
//
// Each server says it differently, and means different things by it:
//
//	Ollama     /api/show capabilities: "tools" or not. A model without it is
//	           refused any request that carries tools (HTTP 400), so "no" here
//	           is final.
//	LM Studio  /api/v1/models capabilities.trained_for_tool_use, or /api/v0's
//	           capabilities ["tool_use"]. LM Studio gives every other model a
//	           fallback tool format, so its silence is "unknown", not "no".
//	llama.cpp  /props chat_template_caps.supports_tool_calls. Without it the
//	           server still offers a generic format: again "unknown".
//	vLLM       nothing. Tools work only if it was started with
//	           --enable-auto-tool-choice, which no endpoint reports.

// localToolSupport is what a server says about a model's tool calling.
type localToolSupport string

const (
	localToolsUnknown localToolSupport = ""
	localToolsYes     localToolSupport = "yes"
	localToolsNo      localToolSupport = "no"
)

// localModelFacts is what a server says about one of its models.
type localModelFacts struct {
	localWindow
	tools    localToolSupport
	params   string // the model's size as the server names it ("3.1B"); "" if unknown
	thinking bool   // the server says the model thinks before it answers
}

// localModelFactsAt asks the server at base about model. It loads the model
// first where the window is only reported for a loaded model (Ollama), as
// localContextWindow does.
func localModelFactsAt(ctx context.Context, base, model string) localModelFacts {
	f := localModelFacts{localWindow: localContextWindow(ctx, base, model)}
	origin := localOrigin(base)
	if origin == "" || model == "" {
		return f
	}
	switch f.kind.id {
	case "ollama":
		var show struct {
			Capabilities []string `json:"capabilities"`
			Details      struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		}
		if doLocalJSON(ctx, localHTTP, http.MethodPost, origin+"/api/show", map[string]any{"model": model}, &show) {
			f.params = show.Details.ParameterSize
			// An Ollama old enough to list no capabilities says nothing about
			// tools, which is not the same as saying "none".
			if show.Capabilities != nil {
				f.tools = localToolsNo
			}
			for _, c := range show.Capabilities {
				switch c {
				case "tools":
					f.tools = localToolsYes
				case "thinking":
					f.thinking = true
				}
			}
		}
	case "lmstudio":
		f.tools = lmstudioToolSupport(ctx, origin, model)
	case "llamacpp":
		var props struct {
			Caps map[string]any `json:"chat_template_caps"`
		}
		if getLocalJSON(ctx, origin+"/props", &props) {
			if v, ok := props.Caps["supports_tool_calls"].(bool); ok && v {
				f.tools = localToolsYes
			}
		}
	}
	return f
}

// lmstudioToolSupport reads LM Studio's model lists, the v1 API first.
func lmstudioToolSupport(ctx context.Context, origin, model string) localToolSupport {
	var v1 struct {
		Models []struct {
			Key          string `json:"key"`
			Capabilities struct {
				TrainedForToolUse bool `json:"trained_for_tool_use"`
			} `json:"capabilities"`
		} `json:"models"`
	}
	if getLocalJSON(ctx, origin+"/api/v1/models", &v1) && v1.Models != nil {
		for _, m := range v1.Models {
			if m.Key == model && m.Capabilities.TrainedForToolUse {
				return localToolsYes
			}
		}
		return localToolsUnknown
	}
	var v0 struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if getLocalJSON(ctx, origin+"/api/v0/models", &v0) {
		for _, m := range v0.Data {
			if m.ID != model {
				continue
			}
			for _, c := range m.Capabilities {
				if c == "tool_use" {
					return localToolsYes
				}
			}
		}
	}
	return localToolsUnknown
}

// localKnown remembers, per server address, which kind of model server it was
// found to be, and per address and model the window it was found to have. Set
// by a fit or a tool check -- that is, only when the terminal app asks -- and
// read on every request (localdialect.go). Package-level because the request
// path that reads it has no Server.
var localKnown struct {
	kinds   sync.Map // normalized base -> kind id
	windows sync.Map // dialectKey(base, model) -> window in tokens
	noTools sync.Map // dialectKey(base, model) -> true: the server refused tools for it, or said it has none
}

func rememberLocalModel(base, model string, w localWindow) {
	if w.kind.id == "" {
		return
	}
	localKnown.kinds.Store(strings.TrimRight(base, "/"), w.kind.id)
	if w.window > 0 && model != "" {
		localKnown.windows.Store(dialectKey(base, model), w.window)
	}
}

// localWindowFor is the window last read for model at base, or 0.
func localWindowFor(base, model string) int {
	if v, ok := localKnown.windows.Load(dialectKey(base, model)); ok {
		return v.(int)
	}
	return 0
}
