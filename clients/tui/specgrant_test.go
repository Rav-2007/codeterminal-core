package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// "s yes while this spec is active": offered only where the daemon offered it,
// held in memory for the active spec only, sent with that spec's turns, and
// forgotten when the spec changes.

const testGrant = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// pausedOn puts m in the approval state for req, as a toolApprovalMsg would.
func pausedOn(m chatModel, req protocol.ToolApprovalRequest) (chatModel, chan string) {
	reply := make(chan string, 1)
	m.state = stateToolApproval
	m.pendingApproval = &req
	m.approvalReply = reply
	return m, reply
}

func execRequest(grant string) protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{CallID: "c1", Server: "builtin", Tool: "sandbox_exec",
		Arguments: `{"command":"go test ./..."}`, ArgumentsSHA256: "ab", SpecGrant: grant}
}

func activeSpecModel(t *testing.T) chatModel {
	m, _ := specWorkspace(t)
	model, _ := m.handleSpecCommand("use verbose-flag")
	return model.(chatModel)
}

func TestSGrantsTheCommandWhileTheSpecIsActive(t *testing.T) {
	m, reply := pausedOn(activeSpecModel(t), execRequest(testGrant))
	if !strings.Contains(m.View(), "s yes while this spec is active") {
		t.Error("the offered answer is not on screen")
	}
	model, _ := m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = model.(chatModel)
	select {
	case got := <-reply:
		if got != protocol.ApprovalApproveForSpec {
			t.Fatalf("answered %q", got)
		}
	default:
		t.Fatal("s sent no answer")
	}
	if got := m.specGrantDigests(); len(got) != 1 || got[0] != testGrant {
		t.Fatalf("the grant was not kept for the next turn: %v", got)
	}
	if !strings.Contains(m.specStatus(), "go test ./...") {
		t.Errorf("/spec show does not list the grant:\n%s", m.specStatus())
	}
}

// Not offered, or no spec to hold it: 's' is a stray key and answers nothing.
func TestSIsAStrayKeyWhereNotOffered(t *testing.T) {
	for name, m := range map[string]chatModel{
		"not offered": activeSpecModel(t),
		"no spec":     func() chatModel { m, _ := specWorkspace(t); return m }(),
	} {
		grant := testGrant
		if name == "not offered" {
			grant = ""
		}
		paused, reply := pausedOn(m, execRequest(grant))
		if strings.Contains(paused.View(), "yes while this spec is active") {
			t.Errorf("%s: the spec answer is on screen", name)
		}
		model, _ := paused.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		select {
		case got := <-reply:
			t.Errorf("%s: s answered %q", name, got)
		default:
		}
		if got := model.(chatModel); got.state != stateToolApproval || len(got.specGrants) != 0 {
			t.Errorf("%s: s changed state=%v grants=%v", name, got.state, got.specGrants)
		}
	}
}

// Switching the spec off, or to another, forgets the grants; re-choosing the
// same spec does not.
func TestChangingTheSpecForgetsItsGrants(t *testing.T) {
	m := activeSpecModel(t)
	m.rememberSpecGrant(execRequest(testGrant))

	model, _ := m.handleSpecCommand("use verbose-flag")
	if len(model.(chatModel).specGrants) != 1 {
		t.Error("re-choosing the same spec forgot its grants")
	}
	model, _ = m.handleSpecCommand("use api/paging")
	if got := model.(chatModel); len(got.specGrants) != 0 || got.specGrantDigests() != nil {
		t.Error("another spec kept the old spec's grants")
	}
	model, _ = m.handleSpecCommand("off")
	if got := model.(chatModel); len(got.specGrants) != 0 || got.specGrantDigests() != nil {
		t.Error("/spec off kept the grants")
	}
	if grantsFor("", []string{testGrant}) != nil {
		t.Error("grants would be sent without a spec")
	}
}

// Nothing about a grant is written anywhere: not in the state dir that
// remembers the active spec, and so not after a restart.
func TestAGrantIsNeverWrittenToDisk(t *testing.T) {
	m := activeSpecModel(t)
	m.rememberSpecGrant(execRequest(testGrant))
	model, _ := m.handleSpecCommand("use verbose-flag") // writes the state file
	m = model.(chatModel)

	state := os.Getenv("XDG_STATE_HOME")
	_ = filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), testGrant) {
				t.Errorf("%s holds a spec grant", p)
			}
		}
		return nil
	})
	restarted := newChatModel("test", m.workspaceRoot, m.workspaceRoot, nil)
	if restarted.activeSpec != "specs/verbose-flag.md" {
		t.Fatalf("setup: the active spec was not remembered (%q)", restarted.activeSpec)
	}
	if restarted.specGrantDigests() != nil {
		t.Error("a grant survived a restart")
	}
}

// On the real wire: the grants go out with the spec they belong to.
func TestSpecGrantsTravelWithTheirSpec(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 2)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()
	defer setLockPathForTest(t, lockPath)()

	ch := make(chan tea.Msg, 8)
	streamPromptWith(context.Background(), "test-client", "", "go on", "", modeBuild, "", "specs/v.md",
		[]string{testGrant}, nil, nil, ch)
	for msg := range ch {
		if _, done := msg.(streamDoneMsg); done {
			break
		}
		if e, bad := msg.(streamErrMsg); bad {
			t.Fatal(e.err)
		}
	}
	select {
	case req := <-requests:
		if len(req.SpecGrants) != 1 || req.SpecGrants[0] != testGrant || req.Spec != "specs/v.md" {
			t.Errorf("sent spec=%q grants=%v", req.Spec, req.SpecGrants)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request reached the daemon")
	}
}
