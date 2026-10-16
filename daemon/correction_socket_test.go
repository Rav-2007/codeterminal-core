package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE JUDGE, THROUGH THE DAEMON'S OWN FRONT DOOR. A /fix sent over the socket
// as a client sends it, its sandbox_exec asked about and answered "yes for this
// task": the second check in a row worse than the first puts the first's code
// back, the restore leaves that check stale, the daemon runs it again on the
// task's grant -- asking nobody -- and the client's task status says both, so
// the person watching knows why their files changed. The audit log records the
// re-check as the judge's, allowed by the grant.
func TestTheJudgeThroughTheSocket(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	base, bodies := worseningScript(t)
	cfg := taskPolicies()
	cfg.Builtin.Tools["checkpoint"] = PolicyAllow
	cfg.Correction.Mode = correctionOn
	sockAddr, auditPath, srv := agentSocketServer(t, base, cfg)
	for rel, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go":   "package m\n\nfunc A() int { return 0 }\n\nfunc B() int { return 2 }\n\nfunc C() int { return 3 }\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\n" +
			"func TestA(t *testing.T) { if A() != 1 { t.Fatal(\"A\") } }\n\n" +
			"func TestB(t *testing.T) { if B() != 2 { t.Fatal(\"B\") } }\n\n" +
			"func TestC(t *testing.T) { if C() != 3 { t.Fatal(\"C\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(srv.workspace, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	msgs := taskRequest(t, sockAddr, protocol.PromptRequest{Prompt: "make the tests pass", Mode: modeFix})
	st := lastTaskStatus(t, msgs)
	if st.State != protocol.TaskStateBlocked || st.Judge != "1 restore · 1 re-check" {
		t.Fatalf("status %+v after %d calls; want blocked, judge \"1 restore · 1 re-check\"", st, len(*bodies))
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	var judged []string
	for _, line := range strings.Split(string(audit), "\n") {
		if strings.Contains(line, `"initiator":"judge"`) {
			judged = append(judged, line)
		}
	}
	if len(judged) != 1 || !strings.Contains(judged[0], `"source":"turn_grant"`) || !strings.Contains(judged[0], "sandbox_exec") {
		t.Errorf("the judge's audit records: %q", judged)
	}
	if got, err := os.ReadFile(filepath.Join(srv.workspace, "m.go")); err != nil || strings.Contains(string(got), "return 20") {
		t.Errorf("the project itself changed before review: %q %v", got, err)
	}
}
