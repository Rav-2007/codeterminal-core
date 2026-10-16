package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
)

func checkpointCall(t *testing.T, s *Server, p *proposalSink, action, name string) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"action": action, "name": name})
	res, err := s.builtinCheckpoint(raw, p)
	return res.Content, err == nil && !res.IsError
}

func stageFile(t *testing.T, st *stagedWorkspace, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(st.root, rel))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

// A HYPOTHESIS TRIED AND ABANDONED: back to the baseline (the changed file is
// the project's again, the created one is gone), then forward to a later
// checkpoint -- and a restore is an edit, so the finish gate wants a new run.
//
// Neuter check: skip the os.Remove branch in restoreCheckpoint, and the created
// file survives the restore.
func TestACheckpointTakesTheWorkingCopyBackAndForth(t *testing.T) {
	s := builtinTestServer(t)
	if err := os.WriteFile(filepath.Join(s.workspace, "calc.go"), []byte("package calc\n\nfunc Add(a, b int) int { return a - b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	p := &proposalSink{stageFrom: root}
	t.Cleanup(p.discard)
	if _, ok := checkpointCall(t, s, p, "save", "baseline"); !ok {
		t.Fatal("saving a baseline with no changes failed")
	}
	st := p.stage
	if _, err := st.apply(editapply.EditBlock{FilePath: "calc.go", Search: "a - b", Replace: "a + b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.apply(editapply.EditBlock{FilePath: "calc_test.go", Search: "", Replace: "package calc\n"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := checkpointCall(t, s, p, "save", "fixed"); !ok {
		t.Fatal("saving a second checkpoint failed")
	}
	p.checks = []ranCheck{{command: "go test ./...", passed: true, edits: p.editCount()}}

	out, ok := checkpointCall(t, s, p, "restore", "baseline")
	if !ok {
		t.Fatalf("restore baseline: %s", out)
	}
	if got, _ := stageFile(t, st, "calc.go"); !strings.Contains(got, "a - b") {
		t.Errorf("calc.go after restoring the baseline: %q", got)
	}
	if _, exists := stageFile(t, st, "calc_test.go"); exists {
		t.Error("a file created after the baseline survived restoring it")
	}
	if why := finishRefusal(modeFix, &taskLedger{}, p); why == "" {
		t.Error("the finish gate accepted a pass from before the restore")
	}

	if out, ok := checkpointCall(t, s, p, "restore", "fixed"); !ok {
		t.Fatalf("restore fixed: %s", out)
	}
	if got, _ := stageFile(t, st, "calc.go"); !strings.Contains(got, "a + b") {
		t.Errorf("calc.go after restoring the fix: %q", got)
	}
	if got, exists := stageFile(t, st, "calc_test.go"); !exists || got != "package calc\n" {
		t.Errorf("calc_test.go after restoring the fix: %q, %v", got, exists)
	}
	if out, _ := checkpointCall(t, s, p, "list", ""); !strings.Contains(out, `"baseline"`) || !strings.Contains(out, `"fixed"`) {
		t.Errorf("list = %q", out)
	}
	if _, ok := checkpointCall(t, s, p, "restore", "nonesuch"); ok {
		t.Error("restoring a checkpoint that does not exist succeeded")
	}
}

// A LINK A COMMAND LEFT WHERE A FILE WAS IS NEVER WRITTEN THROUGH: a build can
// replace a changed file with a link to anywhere, and a restore must not
// follow it out of the working copy.
//
// Neuter check: drop the regular-file check in readStageFile.
func TestACheckpointNeverWritesThroughALink(t *testing.T) {
	s := builtinTestServer(t)
	if err := os.WriteFile(filepath.Join(s.workspace, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	p := &proposalSink{stageFrom: root}
	t.Cleanup(p.discard)
	st, err := p.workingCopy()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.apply(editapply.EditBlock{FilePath: "a.txt", Search: "one", Replace: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := checkpointCall(t, s, p, "save", "two"); !ok {
		t.Fatal("save failed")
	}
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(st.root, "a.txt")
	_ = os.Remove(target)
	if err := os.Symlink(outside, target); err != nil {
		t.Skip("symlinks unavailable here")
	}
	out, _ := checkpointCall(t, s, p, "restore", "two")
	if data, _ := os.ReadFile(outside); string(data) != "untouched\n" {
		t.Fatalf("a restore wrote through a link out of the working copy: %q", data)
	}
	// Refused at the snapshot read itself, before the edit gate -- which would
	// refuse the write too (defense in depth: either one keeps the file safe).
	if !strings.Contains(out, "NOT restored") || !strings.Contains(out, "no longer a regular file") {
		t.Errorf("the refused file was not reported as a non-regular file: %q", out)
	}
}

// A CHECKPOINT NEVER BRINGS BACK WHAT THE PROJECT DELETED OR MOVED. A file
// edited and saved in a checkpoint, then deleted or moved in the project
// (delete_path, move_path, which the user agreed to), was written back into
// the copy by a restore -- and offered for review as a new file, beside the
// moved one. Restoring past a delete or a move is refused.
//
// Neuter checks: drop the reshaped stamp check in restore; drop the count in
// mirrorDelete; drop it in mirrorMove.
func TestACheckpointNeverUndoesADeleteOrMove(t *testing.T) {
	for _, op := range []string{"delete", "move"} {
		s, dir := stageProject(t, map[string]string{"old.go": "package m\n\nvar Old = 1\n"})
		p := stagedSink(t, s)
		if msg, isErr := propose(t, s, p, "old.go", "var Old = 1", "var Old = 2"); isErr {
			t.Fatal(msg)
		}
		if msg, ok := checkpointCall(t, s, p, "save", "mine"); !ok {
			t.Fatal(msg)
		}
		var err error
		if op == "delete" {
			err = os.Remove(filepath.Join(dir, "old.go"))
			p.mirrorDelete("old.go")
		} else {
			err = os.Rename(filepath.Join(dir, "old.go"), filepath.Join(dir, "new.go"))
			p.mirrorMove("old.go", "new.go")
		}
		if err != nil {
			t.Fatal(err)
		}
		if msg, ok := checkpointCall(t, s, p, "restore", "mine"); ok || !strings.Contains(msg, "deleted or moved") {
			t.Errorf("%s: the checkpoint was restored past it: %v %q", op, ok, msg)
		}
		st, _ := p.workingCopy()
		if _, err := os.Lstat(filepath.Join(st.root, "old.go")); !os.IsNotExist(err) {
			t.Errorf("%s: old.go is back in the copy (%v)", op, err)
		}
	}
}
