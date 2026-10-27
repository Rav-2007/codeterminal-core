//go:build unix

package main

import (
	"net/http"
	"strings"
	"testing"

	"mochiii/protocol"
)

// ONE VIEW, END TO END (runplace.go, turnreport.go), through the real binary and
// the socket, with a scripted model: what the real-repo run broke is what these
// drive.

// The agent edits, tests through run_command, tries to commit, and claims the
// tests pass. The command sees the edit; the commit is refused without the user
// being asked; and the claim, backed by nothing that tests, gets Mochiii's record.
func TestE2ERunCommandSeesTheEditAndTheRecordAnswersTheClaim(t *testing.T) {
	model := newE2EModel(t, byToolResults(
		toolCallSSE("c1", "builtin__propose_edit", `{"path":"a.txt","search":"old","replace":"new"}`),
		toolCallSSE("c2", "builtin__run_command", `{"command":"cat a.txt"}`),
		toolCallSSE("c3", "builtin__run_command", `{"command":"git commit -am wip"}`),
		textSSE("Fixed. All tests pass."),
	))
	d := startE2E(t, model, map[string]string{"a.txt": "old\n"}, nil)
	turn := d.prompt(t, "fix a.txt and test it", e2eApproveAll)

	var asked []string
	for _, a := range turn.approvals {
		asked = append(asked, a.Tool)
		if a.Tool == "run_command" && a.InPlace {
			t.Errorf("a run in the working copy was put to the user as one in the real project: %+v", a)
		}
	}
	if strings.Join(asked, ",") != "propose_edit,run_command" {
		t.Errorf("the user was asked about %v; want the edit and the one command that could run", asked)
	}
	reqs := model.requests()
	results := reqs[len(reqs)-1].toolResults()
	if len(results) != 3 || !strings.Contains(results[1], "new") || strings.Contains(results[1], "old") {
		t.Fatalf("run_command did not see the edit: %q", results)
	}
	if !strings.Contains(results[2], "Not run, and the user was not asked") {
		t.Errorf("the git commit was not refused before the user was asked: %q", results[2])
	}
	if got := readWorkspaceFile(t, d, "a.txt"); got != "old\n" {
		t.Errorf("the real file changed before any review: %q", got)
	}
	text := turn.text.String()
	for _, want := range []string{"**Where this stands**", "Changed: a.txt",
		"The answer says the tests pass; this record shows no passing run of the project's tests on the final version."} {
		if !strings.Contains(text, want) {
			t.Errorf("the record under the answer does not say %q:\n%s", want, text)
		}
	}
	if len(turn.done.EditProposals) != 1 {
		t.Errorf("the edit was not offered for review: %+v", turn.done.EditProposals)
	}
}

// A provider that fails after the turn has worked, before it wrote a word: the
// edit is still offered, and the record says where the turn stopped.
func TestE2EAProviderFailureAfterWorkKeepsTheEdit(t *testing.T) {
	model := newE2EModel(t, func(n int, _ e2eChat) e2eReply {
		if n == 0 {
			return e2eReply{lines: toolCallSSE("c1", "builtin__propose_edit", `{"path":"a.txt","search":"old","replace":"new"}`)}
		}
		return e2eReply{status: http.StatusServiceUnavailable}
	})
	// Patience off: this is the turn that ends once the provider has failed
	// (patience.go has its own test of the wait).
	d := startE2E(t, model, map[string]string{"a.txt": "old\n"}, func(mcp map[string]any) {
		mcp["budget"] = map[string]any{"provider_patience_seconds": -1}
	})
	turn := d.prompt(t, "fix a.txt", e2eApproveAll)

	if inc := turn.done.Incomplete; inc == nil || inc.Reason != protocol.IncompleteProviderError {
		t.Fatalf("Done.Incomplete = %+v (error %q), want the provider's part-way stop", inc, turn.done.Error)
	}
	if len(turn.done.EditProposals) != 1 || turn.done.EditProposals[0].FilePath != "a.txt" {
		t.Errorf("the turn's edit was lost: %+v", turn.done.EditProposals)
	}
	text := turn.text.String()
	for _, want := range []string{"**Where this stands**", "Stopped: the model provider failed", "Changed: a.txt",
		"Not verified: nothing was built or tested on these changes."} {
		if !strings.Contains(text, want) {
			t.Errorf("the record does not say %q:\n%s", want, text)
		}
	}
}
