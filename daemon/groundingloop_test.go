package main

import (
	"strings"
	"testing"
)

// webCfg puts the network tools ON THE MENU, which is the state the nudge
// checks for.
//
// THE POLICY MUST BE "ask", NOT "deny", and the first version of these tests
// got that wrong in a way worth recording: Registry.Advertised drops every
// denied tool before the model ever sees it, deliberately, so "deny" produced
// an empty menu and the nudge correctly declined to fire. "ask" advertises the
// tool and then -- with runLoop's nil approver, which refuses everything it
// cannot ask a human about -- refuses the call. Advertised but unrunnable is
// exactly the fixture these tests want, and it makes them hermetic: no request
// can leave, because no call can start.
func webCfg() MCPConfig {
	return MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{
			"web_search": PolicyAsk,
			"web_fetch":  PolicyAsk,
		}},
	}
}

// THE REGRESSION TEST FOR THE REPORTED BEHAVIOUR.
//
// A model answers a live-fact question from memory, appends the "no real-time
// access" note, and stops -- with web_search sitting unused on its menu. The
// turn must not end there.
func TestAHedgedAnswerWithAnUnusedWebToolIsSentBackOnce(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		textSSE(observedHedge),
		textSSE("According to example.org, the office is held by A. Person."),
	)
	s := loopServer(t, base, webCfg())

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("made %d model call(s), want 2 — the hedge was accepted as a finished answer", n)
	}
	if !strings.Contains(res.FinalText, "A. Person") {
		t.Errorf("final text does not contain the second answer: %q", res.FinalText)
	}
	// The user watched the hedge stream in. The follow-up needs a reason.
	if !strings.Contains(res.FinalText, strings.TrimSpace(nudgeNotice)) {
		t.Error("the extra text arrived with no explanation to the user")
	}
}

// The nudge must be a one-shot. A model that hedges, is nudged, and hedges
// again in the same words would otherwise drive a loop whose exit condition is
// the model changing its mind.
func TestTheNudgeFiresAtMostOncePerTurn(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		textSSE(observedHedge),
		textSSE(observedHedge),
		textSSE(observedHedge),
	)
	s := loopServer(t, base, webCfg())

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("made %d model call(s), want exactly 2 — the nudge is repeating", n)
	}
}

// No web tool on the menu means no nudge. Otherwise the loop spends an
// iteration steering the model toward something it was never offered, and then
// gets the same hedge back anyway -- strictly worse than accepting the first.
func TestNoNudgeWhenTheWebToolsAreDisabled(t *testing.T) {
	base, calls, _ := agentUpstream(t, textSSE(observedHedge))
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Web:     MCPWebConfig{Disabled: true},
	})

	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("made %d model call(s), want 1 — a model with no web tool was told to search", n)
	}
	if strings.Contains(res.FinalText, strings.TrimSpace(nudgeNotice)) {
		t.Error("the user was told a search was happening when no search tool exists")
	}
}

// An ordinary answer must cost exactly one model call. This is the test that
// stops the detector becoming a tax on every turn.
func TestAnOrdinaryAnswerIsNotNudged(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		textSSE("The retry loop gives up after three attempts and logs the failure."))
	s := loopServer(t, base, webCfg())

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("made %d model call(s), want 1 — a normal answer triggered a re-ask", n)
	}
}

// A model that actually searched and then qualified its answer is being
// careful. Sending it back would tell it to do what it just did.
func TestNoNudgeAfterTheModelAlreadySearched(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__web_search", `{"query":"who is the pm of india"}`),
		textSSE("I searched but the sources disagree, and this may have changed since."),
	)
	// THIS ONE NEEDS THE CALL TO ACTUALLY RUN, because turn.toolNames -- which
	// is what webToolUsed reads -- records a call that dispatched, not one that
	// was refused. So the policy is "allow" and the search endpoint points at a
	// loopback address.
	//
	// That address is what keeps the test hermetic, and it does so through the
	// production code rather than around it: guardedDialContext refuses
	// 127.0.0.1 before a socket is opened, so no packet leaves, the handler
	// returns a tool ERROR (which still counts as the tool having run), and the
	// assertion below is about the loop's behaviour rather than the internet's.
	cfg := MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"web_search": PolicyAllow}},
		Web:     MCPWebConfig{Endpoint: "http://127.0.0.1:1/", TimeoutSeconds: 2},
	}
	s := loopServer(t, base, cfg)

	if _, _, err := runLoop(t, s); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n > 2 {
		t.Fatalf("made %d model call(s), want 2 — a model that already reached for the web was nudged anyway", n)
	}
}

// THE SECOND REPORTED FAILURE, AS A LOOP TEST.
//
// "who is the current cm of tn" -> a confident, unhedged, three-month-stale
// answer with no tool call, in a session where the previous question HAD been
// searched. Nothing in the answer says it might be wrong, so the hedge detector
// that shipped first had nothing to match.
const staleConfidentAnswer = "The current Chief Minister of Tamil Nadu (TN) is **M. K. Stalin**. " +
	"He has been in office since 7 May 2021, leading the Dravida Munnetra Kazhagam (DMK) government."

func TestAConfidentUnhedgedAnswerToALiveQuestionIsStillSentBack(t *testing.T) {
	// Asserted, not assumed: if this ever starts matching, the test would pass
	// for the wrong reason and stop covering the case it exists for.
	if looksLikeStalenessHedge(staleConfidentAnswer) {
		t.Fatal("fixture is wrong: this answer must NOT read as a hedge, that is the whole point")
	}

	base, calls, bodies := agentUpstream(t,
		textSSE(staleConfidentAnswer),
		textSSE("Correction: according to Wikipedia, the Chief Minister is now someone else."),
	)
	s := loopServer(t, base, webCfg())

	res, _, err := runLoopWithPrompt(t, s, "who is the current cm of tn")
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("made %d model call(s), want 2 — a confidently stale answer was accepted as final", n)
	}
	if !strings.Contains(res.FinalText, "Correction") {
		t.Errorf("the corrected answer is missing: %q", res.FinalText)
	}

	// PRE-FLIGHT MUST HAVE STEERED THE FIRST CALL, not just the retry. That is
	// the half that costs nothing, and if it silently stopped working every
	// live question would pay for an extra round trip.
	if len(*bodies) == 0 {
		t.Fatal("no request bodies captured")
	}
	if !strings.Contains(string((*bodies)[0]), "FOR THIS TURN") {
		t.Error("the first request carried no pre-flight directive; steering is not reaching the model")
	}

	// And the retry must use the nudge written for a confident answer, not the
	// one that tells the model it hedged.
	if !strings.Contains(string((*bodies)[1]), "confidence is not evidence") {
		t.Error("the retry used the hedge nudge; a confident model would be told it said something it did not")
	}
}

// A codebase question must cost exactly one call and carry no directive. This
// is the test that stops the classifier becoming a tax on ordinary work.
func TestACodebaseQuestionIsNeitherSteeredNorRetried(t *testing.T) {
	base, calls, bodies := agentUpstream(t,
		textSSE("The current implementation reads the config once at startup."))
	s := loopServer(t, base, webCfg())

	if _, _, err := runLoopWithPrompt(t, s, "what is the current implementation of runAgentLoop"); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("made %d model call(s), want 1 — a codebase question bought a web search", n)
	}
	if strings.Contains(string((*bodies)[0]), "FOR THIS TURN") {
		t.Error("a codebase question was steered toward the web")
	}
}

// With retrieval on -- the shipped default -- the user's question arrives
// wrapped in kilobytes of their own source. If the classifier read the whole
// message, filePathish would veto everything and the feature would be silently
// dead on the only configuration that ships.
func TestTheFeatureSurvivesRetrievalGrounding(t *testing.T) {
	grounded := "<retrieved_context>\n[1] daemon/agentloop.go:1-40\nfunc runAgentLoop() {}\n</retrieved_context>\n" +
		"<user_request>\nwho is the current cm of tn\n</user_request>"

	base, calls, bodies := agentUpstream(t,
		textSSE(staleConfidentAnswer),
		textSSE("Correction: ..."),
	)
	s := loopServer(t, base, webCfg())

	if _, _, err := runLoopWithPrompt(t, s, grounded); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("made %d model call(s), want 2 — grounding disabled the live-question check", n)
	}
	if !strings.Contains(string((*bodies)[0]), "FOR THIS TURN") {
		t.Error("pre-flight did not fire on a grounded message")
	}
}

// The same bug, at the loop level: a model that searches by bare name must not
// be re-asked to search.
func TestNoNudgeWhenTheModelSearchedByBareName(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		// The bare name, which is what live models actually emit.
		toolCallSSE("c1", "web_search", `{"query":"current cm of tamil nadu"}`),
		textSSE(staleConfidentAnswer),
	)
	cfg := MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"web_search": PolicyAllow}},
		Web:     MCPWebConfig{Endpoint: "http://127.0.0.1:1/", TimeoutSeconds: 2},
	}
	s := loopServer(t, base, cfg)

	if _, _, err := runLoopWithPrompt(t, s, "who is the current cm of tn"); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("made %d model call(s), want 2 — a bare-name search was not counted and the loop re-asked", n)
	}
}
