// The tool-call audit log.
//
// The product's claim about agent mode is "you approve every call and
// everything is audited". This file is the second half of that sentence. Until
// it existed the claim rested on stderr, which vanishes with the process, so
// the honest version was "you approve every call and can watch it scroll past".
//
// WHAT IT DOES NOT RECORD IS THE DESIGN. Tool arguments are unscrubbed model
// output: a search query, a file path, a chunk of somebody's source. They are
// the one field that would make this file worth stealing, so it carries their
// SHA-256 and their length instead. The digest is enough to bind a record to
// the exact call that ran -- it is the same digest the user's client echoed
// back to approve it -- and not enough to reconstruct anything.
package main

import (
	"time"

	"codeterminal/daemon/mcp"
	"codeterminal/protocol"
)

// How a call came to run, or not run. Recorded because "was this authorised?"
// has more than two interesting answers: a call the user explicitly approved, a
// call covered by a grant they gave earlier in the same turn, and a call that
// never asked because their config said allow are three different stories about
// the same log line.
const (
	auditConfigAllow  = "config_allow"          // policy allow: never prompted
	auditUserApprove  = "user_approve"          // the human said yes to this call
	auditUserForTurn  = "user_approve_for_turn" // the human said yes and widened it to the turn
	auditTurnGrant    = "turn_grant"            // covered by an earlier approve_for_turn
	auditDeniedConfig = "denied_config"         // policy deny, or a tool that does not exist
	auditDeniedUser   = "denied_user"           // the human said no, or cancelled
	auditDeniedAsk    = "denied_ask"            // nobody answered, or the answer did not verify
)

// Terminal outcomes.
const (
	auditOutcomeOK      = "ok"
	auditOutcomeError   = "error"     // the tool ran and failed
	auditOutcomeRefused = "refused"   // it never ran
	auditOutcomeCancel  = "cancelled" // it never ran, and the turn ended here
)

// toolAuditEvent is one dispatch decision, written as a single JSON line.
//
// EVERY decision, including the ones nobody was prompted about. An audit that
// covered only the calls the user already watched happen would be a log of
// things they already knew.
type toolAuditEvent struct {
	Ts        string `json:"ts"`        // RFC3339 UTC, so a rate over time is computable
	Iteration int    `json:"iteration"` // which loop step, so a runaway turn is visible as one
	Server    string `json:"server"`
	Tool      string `json:"tool"`
	// Lane and Confined are the honest pair. Confined is false for every Lane B
	// tool unconditionally (see mcp.Tool), and this record must not soften that:
	// the whole reason to keep an audit is that consent is the only protection
	// on that lane.
	Mode     string `json:"mode,omitempty"` // manual | plan | auto
	Lane     string `json:"lane"`
	Confined bool   `json:"confined"`
	Policy   string `json:"policy"` // deny | ask | allow, as resolved from config
	Source   string `json:"source"` // one of the audit* constants above
	// DenyCause distinguishes a human saying no from a client that never
	// answered (see the denyBy* constants). Empty when the call ran.
	DenyCause string `json:"deny_cause,omitempty"`
	// ArgumentsSHA256 and ArgumentsBytes stand in for the arguments themselves,
	// which are deliberately absent -- see the file comment. The digest is the
	// same one the approval was bound to.
	ArgumentsSHA256 string `json:"arguments_sha256"`
	ArgumentsBytes  int    `json:"arguments_bytes"`
	Outcome         string `json:"outcome"`
	// ResultBytes is post-scrub, post-truncation: the number of bytes that
	// actually went back to the model, which in agent mode is the quantity that
	// leaves the machine.
	ResultBytes int   `json:"result_bytes,omitempty"`
	DurationMS  int64 `json:"duration_ms,omitempty"`
	// WaitedMS is how long the human took to answer. Recorded because a log full
	// of sub-second approvals is evidence of a user clicking through prompts
	// without reading them, which is a thing worth being able to notice.
	WaitedMS int64 `json:"waited_ms,omitempty"`
}

// toolAuditMaxBytes bounds the active file before rotation; one rotation keeps
// a single .1 backup, so on-disk usage stays bounded at ~2x this however long
// the daemon runs.
const toolAuditMaxBytes = 5 << 20 // 5 MiB

// toolAuditSink is the durable, append-only, local-only home for those records.
// Its safety properties (no network seam, O_NOFOLLOW, rotation, swallowed
// errors, nil-is-a-no-op) come from jsonlSink, which warnsink.go shares.
type toolAuditSink struct {
	sink *jsonlSink
}

// newToolAuditSink returns a sink writing to path, or nil (a valid no-op) when
// path is empty -- so a daemon with agent mode off, and every test that does
// not care, needs no special case.
func newToolAuditSink(path string) *toolAuditSink {
	if path == "" {
		return nil
	}
	return &toolAuditSink{sink: newJSONLSink(path, toolAuditMaxBytes)}
}

// record appends ev as one JSON line. Safe on a nil receiver, and every error
// is swallowed: an audit-write failure must never fail a turn. That is a real
// trade -- a full disk means a call runs unrecorded -- and it is the same one
// warnsink.go makes, for the same reason: the alternative is a product that
// stops working when its log directory does.
func (s *toolAuditSink) record(ev toolAuditEvent) {
	if s == nil {
		return
	}
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format(time.RFC3339)
	}
	s.sink.append(ev)
}

// auditFor starts a record from what is known before a call is dispatched. The
// tool is passed by value and may be a zero Tool (an unknown or denied name),
// in which case the qualified name the model asked for is what gets recorded --
// a request for a tool that does not exist is exactly the kind of thing an
// audit should keep.
func auditFor(iteration int, mode string, qualified string, tool mcp.Tool, policy mcp.Policy, arguments string) toolAuditEvent {
	server, name, err := mcp.SplitQualifiedName(qualified)
	if err != nil {
		// Not a name this daemon would ever generate. Recorded whole, in the
		// server field, rather than dropped: an unparseable tool name is a
		// finding, not a formatting problem.
		server, name = qualified, ""
	}
	if tool.Server != "" {
		server, name = tool.Server, tool.Name
	}
	return toolAuditEvent{
		Iteration:       iteration,
		Mode:            mode,
		Server:          server,
		Tool:            name,
		Lane:            tool.Lane,
		Confined:        tool.Confined,
		Policy:          string(policy),
		ArgumentsSHA256: argumentsDigest(arguments),
		ArgumentsBytes:  len(arguments),
	}
}

// auditSourceFor maps an approval decision onto its audit source and outcome.
func auditSourceFor(d approvalDecision) (source, outcome string) {
	switch d.Decision {
	case protocol.ApprovalApprove:
		return auditUserApprove, auditOutcomeOK
	case protocol.ApprovalApproveForTurn:
		return auditUserForTurn, auditOutcomeOK
	case protocol.ApprovalCancelTurn:
		return auditDeniedUser, auditOutcomeCancel
	}
	if d.Cause == denyByUser {
		return auditDeniedUser, auditOutcomeRefused
	}
	return auditDeniedAsk, auditOutcomeRefused
}
