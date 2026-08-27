package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// The orchestrator: runs a pipeline of specialists, one at a time, threading
// each one's output into the next one's context.
//
// It sits ABOVE runAgentLoop rather than inside it, and that is the design's
// main structural choice. The loop is the most heavily tested code in this
// package (agentloop_test.go, agentrepeat_test.go, toolconsent_test.go, plus the
// eval suites), and all of that testing is about one specialist's behaviour:
// budgets biting, repeats being recognised, consent being bound to the exact
// argument bytes. Wrapping it keeps every one of those properties. Rewriting it
// to be phase-aware internally would have forfeited them to gain nothing --
// nothing about running four loops in sequence requires the loop to know it.
//
// WHAT THE PHASES SHARE, deliberately:
//
//   - The turn deadline. turnStart is passed unchanged to every phase, so
//     turn_timeout_seconds bounds the WHOLE pipeline. Per-phase ceilings that
//     each look reasonable are exactly how a turn nobody authorised gets spent.
//   - The proposal sink. The Coder's edits land in the same per-turn sink the
//     unorchestrated agent uses, so the Done message carries them unchanged and
//     the human review path is untouched.
//   - The consent channel. One phase runs at a time, so connApprover is never
//     asked two questions at once -- which is the property that makes this
//     design work at all rather than a thing to be careful about.

// phaseOutcome is what one specialist produced.
type phaseOutcome struct {
	role *agentRole
	text string
}

// runOrchestrated executes phases in order and returns the combined result.
//
// onToken receives ONLY the answer phase's prose. Earlier phases are narrated
// through onActivity instead: a user who asked a question wants an answer, not
// a plan and an implementation stapled together and presented as one reply. The
// intermediate text is not discarded -- it is threaded into the next phase,
// which is the entire point -- it just is not the reply.
func (s *Server) runOrchestrated(
	ctx context.Context,
	turnStart time.Time,
	registry *mcp.Registry,
	model string,
	mode string,
	messages []chatMessage,
	routing providerRouting,
	appr approver,
	onToken func(string) error,
	onActivity func(protocol.ToolActivity),
	onProvider func(string),
	onReasoning func(string),
	onDegraded func(protocol.Degradation),
	phases []*agentRole,
) (agentResult, error) {
	// AN EMPTY PIPELINE MUST STILL PRODUCE AN ANSWER.
	//
	// runAgentTurn already routes an unconfigured pipeline to the single loop, so
	// reaching here with no phases means a config in which every named role was
	// unknown -- three typos, say. Returning an empty result would hand the user a
	// blank reply indistinguishable from a crash, for what is really a spelling
	// mistake. Degrading to the unorchestrated agent gives them their answer; the
	// caller has already logged which names it did not recognise.
	if len(phases) == 0 {
		s.logger.Print("agent: no usable phases in mcp.pipeline; running unorchestrated")
		return s.runAgentLoop(ctx, turnStart, registry, model, mode, messages, routing, appr,
			onToken, onActivity, onProvider, onReasoning, onDegraded, nil, nil)
	}

	answerAt := answerPhase(phases)
	baseSystem, history, userPrompt := splitMessages(messages)

	// A degradation is a fact about the TURN, not about one phase, but it is
	// raised inside the loop -- so a truncated tool menu would be reported once
	// per phase and a four-phase turn would tell the user the same thing four
	// times. Deduped here rather than in the loop, because the loop is right to
	// report it every time it runs: it is the orchestrator that made it run
	// repeatedly.
	if onDegraded != nil {
		seen := map[string]bool{}
		inner := onDegraded
		onDegraded = func(d protocol.Degradation) {
			key := string(d.Component) + "\x00" + d.Detail
			if seen[key] {
				return
			}
			seen[key] = true
			inner(d)
		}
	}

	// ONE ledger for the whole pipeline. This is what stops max_total_tool_bytes
	// and the user's approve-for-turn grants resetting at every phase boundary.
	ledger := newTurnLedger()

	// THE ANSWERING PHASE GETS A GUARANTEED SHARE OF THE TURN'S TOOL BUDGET.
	//
	// One ledger means one 128 KiB pool for every phase, which is correct as a
	// privacy bound and was starving the only phase that matters. MEASURED live
	// on a cross-file question: the Researcher consumed the pool and the
	// pipeline stopped at phase 2 of 4, so the user got raw partial research
	// where they had asked a question. A single agent answered the same question
	// inside the same budget.
	//
	// So the phases BEFORE the answer share a slice proportional to how many
	// they are, and the answering phase and everything after it are guaranteed
	// the remainder. Proportional rather than a fixed fraction so the rule
	// degenerates correctly: a one-phase pipeline reserves nothing from itself,
	// and a pipeline that is all research and one writer still leaves the writer
	// its share.
	//
	// This only ever LOWERS a phase's ceiling (see turnLedger.toolByteCap), so
	// no configuration spends more than the user allowed.
	fullToolBudget := s.cfg.MCP.Budget.resolvedMaxTotalToolBytes()
	turnTimeout := s.cfg.MCP.Budget.resolvedTurnTimeout()
	turnIterationCeiling := s.cfg.MCP.Budget.resolvedMaxTurnIterations()

	preAnswerShare, preAnswerDeadline := preAnswerReservation(fullToolBudget, turnTimeout, turnStart, answerAt, len(phases))

	var (
		combined agentResult
		outcomes []phaseOutcome
	)

	for i, role := range phases {
		if err := ctx.Err(); err != nil {
			return combined, err
		}

		s.logger.Printf("agent: phase %d/%d (%s)", i+1, len(phases), role.Name)
		narratePhase(onActivity, role, i, len(phases))

		// Reserve for the answer: phases before it may not draw the pool below
		// what the answering phase is owed. From the answering phase onward the
		// cap is lifted, and the configured turn budget is the only bound.
		if i < answerAt {
			ledger.toolByteCap = preAnswerShare
			ledger.deadlineCap = preAnswerDeadline
		} else {
			ledger.toolByteCap = 0
			ledger.deadlineCap = time.Time{}
		}

		// EVERY PHASE FROM THE ANSWERING ONE ONWARD REACHES THE USER, not just
		// the answering one itself.
		//
		// The four-phase pipeline puts the Tester AFTER the Coder, and the Coder
		// is what marks StreamsAnswer. Streaming only the exact answer index
		// meant the Tester ran -- a model call plus sandbox_exec running the
		// project's real test suite -- and its verdict was then dropped on the
		// floor. Tests failed and nobody was told. A specialist whose whole job
		// is to report cannot have its report discarded by the plumbing.
		streams := i >= answerAt
		phaseMessages := buildPhaseMessages(baseSystem, history, userPrompt, role, outcomes,
			s.cfg.MCP.Budget.resolvedMaxToolResultBytes())

		// A non-answer phase's tokens are captured, not streamed. The captured
		// text becomes the next phase's context.
		var captured strings.Builder
		emit := onToken
		if !streams {
			emit = func(token string) error {
				captured.WriteString(token)
				return nil
			}
		}
		// A phase after the answering one needs a header, or its prose runs on
		// from the previous specialist's as though one voice wrote both.
		//
		// THE CONDITION IS "IS THERE ANYTHING ABOVE ME", NOT "AM I AFTER THE
		// ANSWER PHASE". Those look equivalent and are not: an answering phase
		// that produces no prose is the ORDINARY case for a Coder, whose work
		// goes to the proposal sink rather than into text. Position said "yes,
		// separate" while content said "nothing to separate from", and the two
		// conditions were applied to the two different sinks -- so the user's
		// reply opened with a horizontal rule above nothing, while the
		// transcript recorded the same reply without it.
		//
		// Evaluated BEFORE the phase runs, because the header has to precede the
		// tokens it introduces, and reused afterwards so the stream and
		// FinalText cannot disagree about it.
		separator := ""
		if streams && combined.FinalText != "" {
			separator = fmt.Sprintf("\n\n---\n\n**%s step:**\n\n", role.Display)
			if onToken != nil {
				if err := onToken(separator); err != nil {
					return combined, err
				}
			}
		}

		result, err := s.runAgentLoop(ctx, turnStart, registry, model, mode, phaseMessages, routing, appr,
			emit, onActivity, onProvider, onReasoning, onDegraded, role, ledger)
		if err != nil {
			// A phase that fails fails the turn, exactly as the single loop
			// does. Partial phases are not silently passed off as an answer.
			return combined, fmt.Errorf("%s step: %w", role.Display, err)
		}

		text := result.FinalText
		if !streams {
			text = captured.String()
		}
		outcomes = append(outcomes, phaseOutcome{role: role, text: text})

		combined.ToolNames = append(combined.ToolNames, result.ToolNames...)
		combined.ToolSignatures = append(combined.ToolSignatures, result.ToolSignatures...)
		combined.Iterations += result.Iterations
		if streams {
			// Appended, not assigned: a post-answer phase adds to the reply
			// rather than replacing what the answering phase already said. The
			// separator is the exact string already streamed above -- recomputing
			// the decision here is what let the two sinks disagree.
			combined.FinalText += separator + result.FinalText
		}

		// A PHASE THAT SPENT ITS OWN RESERVATION IS NOT A TURN THAT RAN OUT.
		//
		// A pre-answer phase stopping on the share reserved for it -- of bytes
		// or of time -- must not deny the user an answer. The answering phase
		// still has what it was guaranteed, and handing it partial research plus
		// the original question is strictly better than handing the USER partial
		// research.
		//
		// The three turn-wide bounds are checked HERE, directly, rather than
		// inferring which limit bit from the Incomplete. That is what makes one
		// condition cover both reservations: whatever stopped the phase, if none
		// of the turn's own bounds is exhausted then the turn has room to
		// continue, and if one of them is this falls through to the real stop
		// below. A new reservation needs no new case here.
		if result.Incomplete != nil && i < answerAt &&
			ledger.toolBytes < fullToolBudget &&
			ledger.iterations < turnIterationCeiling &&
			time.Since(turnStart) < turnTimeout {
			s.logger.Printf("agent: the %s step used its reserved share; continuing to the %s step",
				role.Name, phases[i+1].Name)
			continue
		}

		// A budget that bit mid-pipeline stops the pipeline. Continuing would
		// hand the next specialist a truncated plan and let it act on it as
		// though it were complete, which is worse than stopping and saying so.
		if result.Incomplete != nil {
			combined.Incomplete = result.Incomplete
			s.logger.Printf("agent: pipeline stopped during the %s step", role.Name)
			if !streams {
				// THE WORK SO FAR MUST BE STREAMED, NOT MERELY RETURNED.
				//
				// The Done message carries no text (agentturn.go): the user's
				// answer is exactly what went through onToken, and FinalText is
				// read only for edit blocks and for the history record.
				// Assigning here without emitting therefore produced the precise
				// outcome this function exists to prevent -- a BLANK reply with
				// an incomplete badge, indistinguishable from a crash -- while
				// silently persisting into the transcript an assistant turn the
				// user never saw.
				//
				// MEASURED LIVE: a four-phase turn whose Researcher exhausted
				// max_iterations at phase 2 returned 698 bytes and streamed 0.
				// The incomplete notice then told the user "what you see above
				// is everything that was done", above which was nothing.
				text := incompletePhaseText(outcomes)
				combined.FinalText = text
				if text != "" && onToken != nil {
					if err := onToken(text); err != nil {
						return combined, err
					}
				}
			}
			return combined, nil
		}
	}

	return combined, nil
}

// preAnswerReservation computes what the phases BEFORE the answering one are
// allowed to spend, so the answering phase is guaranteed the rest.
//
// EXTRACTED FROM runOrchestrated rather than left inline, because the two
// interesting properties are arithmetic ones and testing them through a live
// pipeline means driving a scripted model to observe a division. They are:
//
//  1. it may only ever TIGHTEN -- the byte share never exceeds the turn's
//     budget and the deadline never falls after the turn's own deadline; and
//  2. the byte share is never ZERO, which is not a rounding nicety.
//     turnLedger.toolByteCap reads zero as "no phase cap at all", and integer
//     division reaches zero from a small enough configured budget, so the
//     reservation would silently switch ITSELF off in the one case where the
//     answering phase can least afford to be starved. The result is unreachable
//     at any budget a person would set -- it needs max_total_tool_bytes below
//     the phase count -- so this is a latent hole rather than a live bug, and
//     the floor exists so that changing this formula later cannot make it live.
//
// A zero-length TIME share is left alone rather than floored, because unlike
// the byte cap it is unambiguous: deadlineCap is a time.Time and turnStart is
// not the zero value, so a degenerate turn_timeout means the pre-answer phases
// stop immediately and the answering phase gets the whole (tiny) turn. That is
// the right degradation, and flooring it would invent time the user did not
// allow.
//
// answerAt == 0 means the first phase answers, so there is nothing to reserve
// FROM: the full budget and a zero deadline (no cap) are returned unchanged.
func preAnswerReservation(fullToolBudget int, turnTimeout time.Duration, turnStart time.Time, answerAt, phases int) (int, time.Time) {
	if answerAt <= 0 || phases <= 0 {
		return fullToolBudget, time.Time{}
	}
	share := fullToolBudget * answerAt / phases
	if share < 1 {
		share = 1
	}
	if share > fullToolBudget {
		share = fullToolBudget
	}
	return share, turnStart.Add(turnTimeout * time.Duration(answerAt) / time.Duration(phases))
}

// narratePhase tells the client which specialist is starting.
//
// It reuses ToolActivity rather than adding a protocol message, because the TUI
// already renders activity and a new message type would mean a client that does
// not understand it seeing nothing at all. The phase reads as a step in the same
// stream of steps the user is already watching.
//
// ToolPhaseStep, NOT ToolPhaseRequested. The first version used "requested",
// which both shipping clients render as the empty string on purpose (see
// clients/tui/chat.go's noteToolActivity). Every phase marker this function
// emitted was therefore discarded by the only clients that exist, and the
// narration feature did nothing at all -- visibly nothing, which is why no test
// of the daemon side could catch it.
//
// The label is split across Tool and Detail rather than pre-formatted into one
// string, so a client decides how a step looks instead of parsing prose.
func narratePhase(onActivity func(protocol.ToolActivity), role *agentRole, i, n int) {
	if onActivity == nil {
		return
	}
	onActivity(protocol.ToolActivity{
		Phase:  protocol.ToolPhaseStep,
		Tool:   role.Display,
		Detail: fmt.Sprintf("step %d/%d", i+1, n),
	})
}

// splitMessages separates the system prompt, the prior history, and this turn's
// user prompt, so each phase can be given a different system prompt over the
// same conversation.
//
// The LAST user message is this turn's prompt; everything between the system
// message and it is history. That mirrors buildChatMessages (provider.go),
// which is what produced this list.
func splitMessages(messages []chatMessage) (system string, history []chatMessage, prompt string) {
	for i, m := range messages {
		switch {
		case m.Role == "system" && i == 0:
			system = m.Content
		case m.Role == "user" && i == len(messages)-1:
			prompt = m.Content
		default:
			history = append(history, m)
		}
	}
	return system, history, prompt
}

// buildPhaseMessages assembles one specialist's context: the base system prompt
// plus its role prompt, the conversation history, and a user message carrying
// this turn's request and whatever earlier specialists established.
//
// THIS FUNCTION IS THE FEATURE. Everything else here is plumbing; the reason
// the pipeline reduces context dilution is that a phase is handed the previous
// phases' CONCLUSIONS rather than the transcript of how they were reached --
// none of the tool results, dead ends, or files read along the way.
// maxHandoff bounds ONE earlier phase's contribution. Zero means unbounded,
// which only tests should ever pass.
func buildPhaseMessages(system string, history []chatMessage, prompt string, role *agentRole, prior []phaseOutcome, maxHandoff int) []chatMessage {
	var messages []chatMessage

	roleSystem := system
	if role != nil && role.Prompt != "" {
		if roleSystem != "" {
			roleSystem += "\n\n"
		}
		roleSystem += role.Prompt
	}
	if roleSystem != "" {
		messages = append(messages, chatMessage{Role: "system", Content: roleSystem})
	}
	messages = append(messages, history...)

	var b strings.Builder
	b.WriteString(prompt)
	for _, out := range prior {
		if strings.TrimSpace(out.text) == "" {
			continue
		}
		// out.Display(), not out.role.Display: the direct dereference panics on a
		// zero-valued outcome, and the guarded accessor already exists two
		// functions down. One of the two spellings had to go.
		//
		// A PHASE WITH NO TOOLS COULD NOT HAVE CHECKED ANYTHING IT SAYS, and the
		// handoff has to say so. MEASURED live: on a "trace what happens" question
		// the tool-less Planner invented `src/agent/agent.ts` and `src/agent/tools.ts`
		// -- TypeScript paths, in a Go repository -- and the later phases carried
		// them into the answer as though they were findings. Both of the
		// four-phase pipeline's losses in that A/B were attributed by the judge to
		// exactly those invented paths.
		//
		// The role prompt already tells the Planner to "say plainly when you do
		// not know one rather than inventing a plausible path". It invented anyway,
		// which is the ordinary result of using a prompt as a control. Labelling
		// the handoff is a property of the pipeline instead: the next specialist
		// is told what kind of claim it is reading.
		fmt.Fprintf(&b, "\n\n--- %s step produced%s ---\n%s",
			out.Display(), unverifiedNote(out.role), truncateHandoff(out.text, maxHandoff))
	}
	// An EMPTY user message is not appended. splitMessages yields an empty
	// prompt whenever the list does not end in a user turn, and sending
	// {"role":"user","content":""} is a malformed request that costs a round
	// trip to be told so. Omitting it leaves the phase with the system prompt
	// and history, which is a coherent request.
	if b.Len() > 0 {
		messages = append(messages, chatMessage{Role: "user", Content: b.String()})
	}
	return messages
}

// truncateHandoff bounds one phase's contribution to the next phase's context.
//
// WITHOUT IT THE HANDOFF IS UNBOUNDED. Each phase's full prose is appended to
// every later phase's user message, so the context grows with
// phases x output-size and nothing anywhere caps it. MEASURED: three phases
// emitting 200KB each produced a 600KB request. The failure that causes is
// expensive and late -- the model refuses on context length at the LAST phase,
// after the turn has already paid for every earlier one.
//
// THIS IS NOT AN EGRESS LEAK, and saying so precisely matters because the
// neighbouring budget looks like it should cover it. max_total_tool_bytes bounds
// what leaves the machine BECAUSE OF TOOLS, and it still does: the Researcher's
// file reads are charged to it in full. What gets re-sent here is the model's
// OWN prose, which that same provider generated a moment earlier -- echoing it
// back tells the provider nothing it did not just say. The problem is size, not
// disclosure, and the fix is a bound rather than a charge against the privacy
// ledger.
//
// The marker is left in place of the tail rather than the text being silently
// cut, so a later specialist reading a plan that stops mid-sentence knows it was
// truncated instead of treating it as the whole plan.
func truncateHandoff(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "\n\n[... this step's output was truncated here; it exceeded the per-step handoff limit ...]"
}

// incompletePhaseText is what the user sees when a budget stopped the pipeline
// before the answering specialist ran. Without it they would get an incomplete
// notice attached to an empty answer, which reads as a crash rather than as the
// bounded stop it is.
func incompletePhaseText(outcomes []phaseOutcome) string {
	var b strings.Builder
	for _, out := range outcomes {
		if strings.TrimSpace(out.text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "**%s step:**\n\n%s", out.Display(), out.text)
	}
	return b.String()
}

// Display is a small helper so incompletePhaseText reads cleanly.
func (o phaseOutcome) Display() string {
	if o.role == nil {
		return "Earlier"
	}
	return o.role.Display
}

// unverifiedNote marks a handoff from a phase that had no tools.
//
// Such a phase cannot have opened a file, so every path and symbol it names is
// a guess. Saying so costs a clause and changes what the next specialist does
// with it -- which is the difference between a plan and a hallucination that
// three later phases treat as established fact.
func unverifiedNote(role *agentRole) string {
	if role == nil || len(role.Tools) > 0 {
		return ""
	}
	return " (this step had NO TOOLS and verified nothing: treat any file path or " +
		"symbol it names as a guess to check, not as a finding)"
}
