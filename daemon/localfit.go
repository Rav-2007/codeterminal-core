package main

import (
	"context"
	"sync"
	"time"

	"mochiii/protocol"
)

// FITTING THE CODE CONTEXT INTO A LOCAL MODEL'S WINDOW (1.3.0).
//
// The code a question sends is bounded by contextBudgetChars, 32,000 characters
// by default -- about 10,000 tokens. Ollama gives a model 4,096 unless told
// otherwise and drops what does not fit without an error: measured 2026-10-23
// with qwen2.5-coder:3b, a 4,893-token request arrived as 2,050 tokens and the
// model answered about a tool that does not exist. So when the terminal app asks
// (LocalRequest.Fit), the window is read from the server and the budget cut to
// what fits, and the app says so once.
//
// The numbers are deliberately rough and on the safe side: what matters is
// that nothing is cut silently, not that the last few hundred characters fit.
const (
	// localChatOverheadTokens is the instructions, the question and recent
	// history around the code: 1,946 tokens in total for a short question with
	// two small files, measured with Ollama on 2026-10-23.
	localChatOverheadTokens = 2048
	// localAnswerReserveTokens is left for the answer itself.
	localAnswerReserveTokens = 1024
	// localCharsPerToken: code tokenizes densely, so 3 rather than the usual 4.
	localCharsPerToken = 3
	// localAgentNeedTokens is the least agent mode can work in: its first
	// request alone was 4,893 tokens (17 tools and their instructions), and
	// every tool result adds to it.
	localAgentNeedTokens = 8192
	// localFitTimeout bounds a fit, which may load the model first.
	localFitTimeout = localPreloadTimeout + 10*time.Second
)

// fitBudgetChars is the code budget that fits a window of the given size, and
// whether that is less than configured. An unknown window (0) changes nothing.
func fitBudgetChars(window, configured int) (chars int, fitted bool) {
	if window <= 0 {
		return configured, false
	}
	room := (window - localChatOverheadTokens - localAnswerReserveTokens) * localCharsPerToken
	if room < 0 {
		room = 0
	}
	if room >= configured {
		return configured, false
	}
	return room, true
}

// localFitState is the last fit, and which address and model it was for: a
// fit says nothing about another model, so it applies only while both are in
// force.
type localFitState struct {
	mu    sync.Mutex
	base  string
	model string
	chars int
	set   bool
}

func (f *localFitState) store(base, model string, chars int, fitted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.base, f.model, f.chars, f.set = base, model, chars, fitted
}

// any reports whether a fit has been made at all, so the common case -- no
// local model, no fit -- costs a lock and nothing else.
func (f *localFitState) any() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.set
}

func (f *localFitState) budgetFor(base, model string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.set || f.model != model || !sameAPIBase(f.base, base) {
		return 0, false
	}
	return f.chars, true
}

// budgetChars is the code budget for the next question: contextBudgetChars,
// or less when a fit for the address and model in force said so.
func (s *Server) budgetChars() int {
	if !s.localFit.any() {
		return s.contextBudgetChars
	}
	_, base := s.credentials()
	if chars, ok := s.localFit.budgetFor(base, s.route("", "").Slug); ok && chars < s.contextBudgetChars {
		return chars
	}
	return s.contextBudgetChars
}

// fitLocal reads the context window of the model in force from the server on
// this machine, and fits the code budget into it. An address that is not on
// this machine answers Ok with no Context: there is nothing to fit.
func (s *Server) fitLocal(ctx context.Context) protocol.LocalResponse {
	_, base := s.credentials()
	if !isLoopbackBase(base) {
		return protocol.LocalResponse{Ok: true}
	}
	model := s.route("", "").Slug
	fctx, cancel := context.WithTimeout(ctx, localFitTimeout)
	defer cancel()
	w := localContextWindow(fctx, base, model)
	rememberLocalModel(base, model, w) // the request wording follows it: localdialect.go
	chars, fitted := fitBudgetChars(w.window, s.contextBudgetChars)
	s.localFit.store(base, model, chars, fitted)
	if fitted {
		s.logger.Printf("local model: %s has a %d-token window at %s; code context cut from %d to %d characters to fit",
			model, w.window, base, s.contextBudgetChars, chars)
	}
	return protocol.LocalResponse{Ok: true, Context: &protocol.LocalContext{
		APIBase: base, Kind: w.kind.id, Model: model,
		Window: w.window, Trained: w.trained,
		BudgetChars: chars, Fitted: fitted,
		AgentFits: w.window >= localAgentNeedTokens,
		Raise:     localRaiseAdvice(w.kind.id),
	}}
}
