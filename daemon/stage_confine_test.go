package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// NOTHING IN THE WORKING COPY IS READ, WRITTEN OR REMOVED PAST A LINK A COMMAND
// PLANTED. A command runs in the copy -- `make` runs whatever recipe the model
// wrote -- and can replace a folder with a link to one outside. FOUND
// 2026-10-01, all three reproduced before the fix: a checkpoint read the
// outside file (and a restore brought its content in, where read_file shows
// it to the model), a restore DELETED one, and a spec tick wrote one.

// plantFolderLink replaces the copy's folder rel with a link to outside, as a
// command could.
func plantFolderLink(t *testing.T, st *stagedWorkspace, rel, outside string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(st.root, rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(st.root, rel)); err != nil {
		t.Fatal(err)
	}
}

// Neuter check: read with os.ReadFile(filepath.Join(st.root, rel)) in
// readCopyFile, and the outside file's content is checkpointed and restored.
func TestACheckpointNeverReadsPastAFolderLink(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"cfg/config": "project config\n"})
	sink := stagedSink(t, s)
	propose(t, s, sink, "cfg/config", "project config", "edited config")
	st := sink.stage
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "config"), []byte("OUTSIDE-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plantFolderLink(t, st, "cfg", outside)

	res, _ := st.saveCheckpoint("probe")
	if cp := st.checkpoint("probe"); cp != nil {
		if c := cp.files["cfg/config"]; c != nil && strings.Contains(*c, "OUTSIDE-SECRET") {
			t.Fatal("the checkpoint read a file outside the working copy through a planted folder link")
		}
	}
	if !res.IsError {
		t.Errorf("saving past a planted link should be refused, got %q", res.Content)
	}
}

// Neuter check, TWO LAYERS: the restore reads the file (readCopyFile) before it
// removes it (removeCopyFile), and each refuses the escape on its own. Join
// both to st.root with os.Lstat/os.ReadFile and os.Remove, and the user's file
// outside is deleted.
func TestARestoreNeverDeletesPastAFolderLink(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"main.go": "package main\n"})
	sink := stagedSink(t, s)
	propose(t, s, sink, "main.go", "package main", "package main // edited")
	st := sink.stage
	if r, _ := st.saveCheckpoint("base"); r.IsError {
		t.Fatalf("save: %q", r.Content)
	}
	// Created after the checkpoint, so a restore removes it...
	propose(t, s, sink, "out/report.txt", "", "task output\n")
	// ...and its folder now leads to one outside holding a file of that name.
	victim := t.TempDir()
	report := filepath.Join(victim, "report.txt")
	if err := os.WriteFile(report, []byte("the user's own report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plantFolderLink(t, st, "out", victim)

	r, _ := st.restoreCheckpoint(st.checkpoint("base"))
	if _, err := os.Stat(report); err != nil {
		t.Fatalf("restoring a checkpoint deleted a file OUTSIDE the working copy (%v); restore said %q", err, r.Content)
	}
	if !strings.Contains(r.Content, "NOT restored") {
		t.Errorf("the restore should report the file it could not put back: %q", r.Content)
	}
}

// Neuter check, TWO LAYERS: the tick reads (readCopyFile) and writes
// (writeCopyFile), and each refuses the escape on its own. Join both to
// st.root with os.Stat/os.ReadFile and os.WriteFile, and the file outside is
// ticked.
func TestASpecTickNeverWritesPastALink(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"specs/task.md": "# Task\n\n- [ ] C1: it works\n"})
	sink := stagedSink(t, s)
	propose(t, s, sink, "specs/task.md", "# Task", "# Task (building)")
	st := sink.stage
	outside := filepath.Join(t.TempDir(), "other.md")
	if err := os.WriteFile(outside, []byte("- [ ] C1: someone else's checklist\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.root, "specs", "task.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(st.root, "specs", "task.md")); err != nil {
		t.Fatal(err)
	}
	tickSpecInCopy(st, &activeSpec{Path: "specs/task.md"}, map[string]specVerdict{"C1": {Status: protocol.SpecMet}})
	if got, _ := os.ReadFile(outside); strings.Contains(string(got), "[x]") {
		t.Errorf("ticking the spec wrote into a file OUTSIDE the working copy: %q", got)
	}
	// The tick itself still works on the copy's own spec (anti-vacuity: the
	// refusal above is not a tick that never writes anything).
	s2, _ := stageProject(t, map[string]string{"specs/task.md": "# Task\n\n- [ ] C1: it works\n"})
	sink2 := stagedSink(t, s2)
	propose(t, s2, sink2, "specs/task.md", "# Task", "# Task (building)")
	tickSpecInCopy(sink2.stage, &activeSpec{Path: "specs/task.md"}, map[string]specVerdict{"C1": {Status: protocol.SpecMet}})
	if got, _ := os.ReadFile(filepath.Join(sink2.stage.root, "specs", "task.md")); !strings.Contains(string(got), "[x] C1") {
		t.Errorf("the copy's own spec was not ticked: %q", got)
	}
}
