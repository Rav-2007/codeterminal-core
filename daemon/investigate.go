package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// investigate: a question answered by a short read-only sub-run with its OWN
// context, so the main segment receives the answer and not everything read to
// find it. Reading twenty files to learn where a value is set costs the main
// context twenty files on every call after; asking costs it fifteen lines.
//
// THE SUB-RUN IS A ROLE, enforced where calls run (resolveExecutable): read
// tools only, never investigate itself, never an edit or a command. It shares
// the task's budget -- its calls are counted and billed against the run -- and
// its approvals: "yes for this whole task" covers its calls as it covers the
// task's own. Its text is not streamed into the user's answer; its tool calls
// show as activity, like any other.

const (
	investigateCalls     = 8
	investigateMaxRunes  = 2000
	investigateMaxAskLen = 1000
)

// roleInvestigator is the sub-run's role. Deliberately absent from knownRoles:
// it is not a /team step, only the shape investigate runs in.
var roleInvestigator = agentRole{
	Name:    "investigator",
	Display: "Investigator",
	Tools:   []string{"read_file", "list_directory", "search_code", "grep", "repo_map", "git_history", "query_compiler_references"},
}

// investigatorDirective is what the sub-run's system message adds.
const investigatorDirective = "YOU ARE ANSWERING ONE QUESTION for the agent doing a longer task, and your " +
	"reply is all it will see of your work. Use the read-only tools to find the answer. Then reply in at most " +
	"15 lines: the answer first, then the facts behind it, each with its file:line. Say plainly what you could " +
	"not establish. You cannot change anything."

// investigatorWrapUpNote asks for the answer when the sub-run reaches its calls.
const investigatorWrapUpNote = "You have reached the limit for this question and cannot call any more tools. " +
	"Answer it now from what you have read, in at most 15 lines, citing file:line, and say what is still unknown."

// newInvestigator returns the sub-run a long task's investigate calls: the
// task's own registry, model, routing, approver and grants, a fresh context.
func (s *Server) newInvestigator(run *taskRun, turnStart time.Time, registry *mcp.Registry, model string,
	system string, routing providerRouting, appr approver, grants map[string]bool,
	onActivity func(protocol.ToolActivity), onProvider func(string)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, question string) (string, error) {
		msgs := []chatMessage{
			{Role: "system", Content: system + "\n\n" + investigatorDirective},
			{Role: "user", Content: "Question: " + neutralizeDelimiters(question)},
		}
		ledger := &turnLedger{grants: grants, segment: &segmentBudget{
			calls: investigateCalls, deadline: run.deadline,
			// n of the sub-run's calls, PLUS the main segment's call that asked
			// for it: the main loop counts a call at its next step, and this
			// whole investigation happens before that step.
			check:  func(n int) *protocol.IncompleteInfo { run.nested = n + 1; return run.check(run.segCalls) },
			wrapUp: func(*protocol.IncompleteInfo) string { return investigatorWrapUpNote },
			waited: func(d time.Duration) { run.deadline = run.deadline.Add(d) },
		}}
		res, err := s.runAgentLoop(ctx, turnStart, registry, model, run.ledger.Mode, msgs, routing, appr,
			func(string) error { return nil }, onActivity, onProvider, nil, nil, &roleInvestigator, ledger)
		run.doneCalls += res.Iterations
		run.nested = 0
		if err != nil {
			return "", err
		}
		answer := strings.TrimSpace(res.FinalText)
		if answer == "" {
			return "", fmt.Errorf("the investigation found no answer")
		}
		return truncateRunes(answer, investigateMaxRunes), nil
	}
}

func (s *Server) builtinInvestigate(ctx context.Context, raw json.RawMessage, p *proposalSink) (mcp.Result, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	q := strings.TrimSpace(args.Question)
	if q == "" {
		return toolError("give the question to answer")
	}
	if len([]rune(q)) > investigateMaxAskLen {
		return toolError("ask one question, in at most %d characters", investigateMaxAskLen)
	}
	if p == nil || p.task == nil || p.task.investigate == nil {
		return toolError("investigate runs only inside a long task")
	}
	answer, err := p.task.investigate(ctx, q)
	if err != nil {
		if ctx.Err() != nil {
			return mcp.Result{}, ctx.Err()
		}
		return toolError("the investigation stopped: %v", err)
	}
	return mcp.Result{Content: answer}, nil
}

// investigateTool is the built-in, offered in the long-task modes.
func (s *Server) investigateTool(p *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "investigate",
			Description: "Answer one question about the code with a short read-only sub-search, and get back only " +
				"the answer (at most 15 lines, with file:line), not everything read to find it. Use it when " +
				"finding something out would mean reading many files you will not need again.",
			Schema: schema(`{
				"type":"object",
				"properties":{"question":{"type":"string","description":"One specific question, e.g. where is the retry budget set, and who reads it?"}},
				"required":["question"],
				"additionalProperties":false
			}`),
			ReadOnlyHint: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinInvestigate(ctx, raw, p)
		},
	}
}
