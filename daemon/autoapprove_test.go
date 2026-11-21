package main

import (
	"context"
	"encoding/json"
	"testing"

	"mochiii/protocol"
)

// AN AUTO-APPROVE ANSWER IS A YES, AND THE AUDIT SAYS WHOSE. The terminal
// client's auto-approve mode (owner, 2026-11-21) answers as y would, marked
// Auto; it is verified exactly as a person's answer is, and audited as
// auto_approve so the log does not claim a person read the call. The mark means
// nothing on any answer but a plain yes, and an answer without it is a person's.
//
// Neuter checks: drop Auto from Ask's result and the first row's source is
// user_approve; drop the Decision check in Ask and the turn-grant row says auto.
func TestAnAutoApproveAnswerIsAuditedAsOne(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision string
		approval bool
		auto     bool
		approved bool
		source   string
		wantAuto bool
	}{
		{"auto yes", protocol.ApprovalApprove, true, true, true, auditAutoApprove, true},
		{"a person's yes", protocol.ApprovalApprove, true, false, true, auditUserApprove, false},
		{"auto on a turn grant is not an auto answer", protocol.ApprovalApproveForTurn, true, true, true, auditUserForTurn, false},
		{"auto on a no is still a no", protocol.ApprovalDeny, false, true, false, auditDeniedUser, false},
		{"auto does not turn a false approval into a yes", protocol.ApprovalApprove, false, true, false, auditDeniedAsk, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			appr, client := approvalPipe(t, 2e9)
			req := approvalRequest("c1", `{"path":"a.txt"}`)
			go func() {
				var msg protocol.TokenResponse
				if err := json.NewDecoder(client).Decode(&msg); err != nil || msg.ToolApproval == nil {
					return
				}
				_ = json.NewEncoder(client).Encode(protocol.ToolApprovalResponse{
					ProtocolVersion: protocol.ProtocolVersion,
					Approval:        tc.approval,
					CallID:          msg.ToolApproval.CallID,
					ArgumentsSHA256: msg.ToolApproval.ArgumentsSHA256,
					Decision:        tc.decision,
					Auto:            tc.auto,
				})
			}()
			got := appr.Ask(context.Background(), req)
			if got.approved() != tc.approved {
				t.Fatalf("approved = %t, want %t (%+v)", got.approved(), tc.approved, got)
			}
			if got.Auto != tc.wantAuto {
				t.Errorf("Auto = %t, want %t: only a plain yes is auto-approve's", got.Auto, tc.wantAuto)
			}
			if source, _ := auditSourceFor(got); source != tc.source {
				t.Errorf("audited as %q, want %q (%+v)", source, tc.source, got)
			}
		})
	}
}
