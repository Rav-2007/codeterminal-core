package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// What a command the agent runs can leave behind, and how the user is told.
//
// MEASURED 2026-10-07 by running a hostile test file through the real
// sandbox_exec, on bwrap and on Landlock, with every approval answered yes:
// it could not read the home folder, an ignored .env or the daemon's key; it
// could not write outside its working copy; the project's .git was out of its
// reach. Two things were left, and these tests are about them:
//
//   - the one change it COULD make -- to a file of the project, in the copy --
//     was offered for review like the agent's own edit, with nothing to say a
//     stranger wrote it (stagedWorkspace.noteCommandChanges);
//   - a turn with no working copy runs its commands in the project itself, and
//     the prompt read exactly as it does for a command that cannot touch it
//     (mcp.Builtin.InPlace).

// A CHANGE A COMMAND MADE IS OFFERED BY NAME. The command here is simulated --
// files written into the copy between the two measurements, which is all a
// real one is to this code -- so the test runs everywhere; the next test runs a
// real one.
func TestAChangeACommandMadeIsOfferedByName(t *testing.T) {
	s, dir := stageProject(t, map[string]string{
		"agent.txt": "one\n", "both.txt": "two\n", "command.txt": "three\n", "untouched.txt": "four\n",
	})
	sink := stagedSink(t, s)
	propose(t, s, sink, "both.txt", "two", "TWO") // makes the copy; the agent's edit
	st := sink.stage
	if st == nil {
		t.Fatal("no working copy was made")
	}

	before := st.fileStates()
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(st.root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("command.txt", "three\nadded by the command\n")
	write("both.txt", "TWO\nand the command added this after the agent's edit\n")
	write("build.log", "made by the command\n")
	st.noteCommandChanges(before, "make generate")

	propose(t, s, sink, "agent.txt", "one", "ONE") // the agent's, after the command

	blocks, info, _ := sink.finish()
	offered := map[string]bool{}
	for _, b := range blocks {
		offered[b.FilePath] = true
	}
	for _, want := range []string{"agent.txt", "both.txt", "command.txt"} {
		if !offered[want] {
			t.Errorf("%s changed and is not offered: %+v", want, blocks)
		}
	}
	if info == nil {
		t.Fatal("no working-copy report")
	}
	got := strings.Join(info.ByCommand, "\n")
	for _, want := range []string{"command.txt (make generate)", "both.txt (make generate)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the review does not say a command changed %q; it says:\n%s", want, got)
		}
	}
	for _, not := range []string{"agent.txt", "untouched.txt", "build.log"} {
		if strings.Contains(got, not) {
			t.Errorf("%s is named as changed by a command and was not (or is not offered):\n%s", not, got)
		}
	}
	if len(info.ByCommand) != 2 {
		t.Errorf("named %d file(s), want 2: %v", len(info.ByCommand), info.ByCommand)
	}
	if text := fileText(t, filepath.Join(dir, "command.txt")); text != "three\n" {
		t.Error("the project changed before review")
	}

	// A turn in which no command changed anything says nothing.
	sink = stagedSink(t, s)
	propose(t, s, sink, "agent.txt", "one", "ONE")
	if _, info, _ := sink.finish(); info == nil || len(info.ByCommand) != 0 {
		t.Errorf("an agent's edit alone is reported as a command's: %+v", info)
	}
}

// THE SAME, WITH A REAL COMMAND: the finding as it was made. A test file that
// rewrites a source file of the project it is run in.
func TestARealCommandsChangeToAProjectFileIsNamed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	// Written as joined lines, not as one raw string: a raw string holding a
	// Go file puts "func TestTwo" at the start of a line of THIS file, which
	// the chunker's line heuristic reads as a declaration
	// (TestTheHeuristicAgreesWithTheCompiler says so, by name).
	hostile := strings.Join([]string{
		"package m",
		"",
		"import (",
		"\t\"os\"",
		"\t\"testing\"",
		")",
		"",
		"func TestTwo(t *testing.T) {",
		"\tf, err := os.OpenFile(\"m.go\", os.O_APPEND|os.O_WRONLY, 0o644)",
		"\tif err != nil {",
		"\t\tt.Fatal(err)",
		"\t}",
		"\tdefer f.Close()",
		"\tif _, err := f.WriteString(\"\\n// ADDED BY THE TEST, NOT BY THE AGENT\\n\"); err != nil {",
		"\t\tt.Fatal(err)",
		"\t}",
		"\tif err := os.WriteFile(\"planted.sh\", []byte(\"#!/bin/sh\\n\"), 0o755); err != nil {",
		"\t\tt.Fatal(err)",
		"\t}",
		"}",
		"",
	}, "\n")
	s, dir := stageProject(t, map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.22\n",
		"m/m.go":      "package m\n\nfunc Two() int { return 2 }\n",
		"m/m_test.go": hostile,
	})
	sink := stagedSink(t, s)
	res, err := s.builtinSandboxExecStaged(context.Background(), json.RawMessage(`{"command":"go test ./..."}`), sink)
	if err != nil || res.IsError {
		t.Fatalf("sandbox_exec: %v %s", err, res.Content)
	}
	if strings.HasPrefix(res.Content, "Command exited with error") {
		t.Fatalf("the test could not write in its working copy, so this proves nothing:\n%s", res.Content)
	}

	// The project is as it was: the command ran in the copy.
	if got := fileText(t, filepath.Join(dir, "m", "m.go")); strings.Contains(got, "ADDED BY THE TEST") {
		t.Fatal("the command changed the real project")
	}
	if _, err := os.Stat(filepath.Join(dir, "m", "planted.sh")); err == nil {
		t.Fatal("a file the command created reached the real project")
	}

	blocks, info, _ := sink.finish()
	var offersIt bool
	for _, b := range blocks {
		if b.FilePath == "m/m.go" && strings.Contains(b.Replace, "ADDED BY THE TEST") {
			offersIt = true
		}
		if strings.Contains(b.FilePath, "planted.sh") {
			t.Errorf("a file the command created is offered: %+v", b)
		}
	}
	if !offersIt {
		t.Fatalf("the command's change to m.go is not among the offered edits, so there is nothing to name: %+v", blocks)
	}
	if info == nil || len(info.ByCommand) != 1 || info.ByCommand[0] != "m/m.go (go test ./...)" {
		t.Fatalf("the review does not name the command's change: %+v", info)
	}
}

// WHERE THIS TURN'S COMMANDS RUN, as the real sandbox_exec reports it before
// anyone is asked.
func TestSandboxExecSaysWhetherItWouldRunInTheRealProject(t *testing.T) {
	probe := func(s *Server, sink *proposalSink) func() bool {
		t.Helper()
		for _, b := range s.builtinTools(sink, "auto") {
			if b.Tool.Name == "sandbox_exec" {
				if b.InPlace == nil {
					t.Fatal("sandbox_exec has no InPlace probe")
				}
				return b.InPlace
			}
		}
		t.Fatal("sandbox_exec is not among the built-in tools")
		return nil
	}

	// An ordinary project: a copy is made, and the command will run in it.
	s, _ := stageProject(t, map[string]string{"a.txt": "one\n"})
	sink := stagedSink(t, s)
	if probe(s, sink)() {
		t.Error("an ordinary project's commands are reported as running in the real project")
	}
	if sink.stage == nil {
		t.Error("the probe said a copy would be used and did not make one")
	}

	// Too large to copy.
	s, _ = stageProject(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	prev := stageMaxFiles
	stageMaxFiles = 1
	t.Cleanup(func() { stageMaxFiles = prev })
	if sink = stagedSink(t, s); !probe(s, sink)() {
		t.Error("a project too large to copy is not reported as running its commands in place")
	}
	stageMaxFiles = prev

	// Working copies switched off.
	s, _ = stageProject(t, map[string]string{"a.txt": "one\n"})
	s.cfg.MCP.NoWorkingCopy = true
	sink = &proposalSink{stageFrom: s.workingCopySource("auto")}
	if !probe(s, sink)() {
		t.Error("with working copies switched off, commands are not reported as running in place")
	}

	// And the description the approving human can read says both cases.
	desc := s.sandboxExecDescription()
	for _, want := range []string{"private copy of the project", "named as the command's", "project itself", "the prompt says so"} {
		if !strings.Contains(desc, want) {
			t.Errorf("sandbox_exec's description does not say %q:\n%s", want, desc)
		}
	}
}

// THE RULE IN THE LOOP: a command that would run in the real project is asked
// about whatever the configuration says, and the prompt carries the fact.
func TestACommandThatWouldRunInTheRealProjectIsAlwaysAskedAbout(t *testing.T) {
	run := func(inPlace bool, decision string) (prompts []protocol.ToolApprovalRequest, ran int) {
		t.Helper()
		base, _, _ := agentUpstream(t,
			toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go test ./..."}`), textSSE("done"))
		cfg := MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{"sandbox_exec": PolicyAllow}}}
		s := loopServer(t, base, cfg)
		registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, s.cfg.MCP.Budget.resolvedMaxAdvertisedToolsFor("auto"))
		t.Cleanup(func() { _ = registry.Close() })
		if err := registry.RegisterBuiltin(mcp.Builtin{
			Tool: mcp.Tool{Name: "sandbox_exec", Description: "stub", Schema: schema(`{"type":"object"}`), ExecutesCode: true, Confined: true},
			Handler: func(context.Context, json.RawMessage) (mcp.Result, error) {
				ran++
				return mcp.Result{Content: "ok"}, nil
			},
			InPlace: func() bool { return inPlace },
		}); err != nil {
			t.Fatal(err)
		}
		appr := &capturingApprover{decision: decision}
		if _, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
			[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "run the tests"}}, providerRouting{}, appr,
			func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
			func(protocol.Degradation) {}, nil, nil); err != nil {
			t.Fatalf("runAgentLoop: %v", err)
		}
		return appr.prompts(), ran
	}

	// In a working copy: "allow" means what it always meant.
	if prompts, ran := run(false, protocol.ApprovalDeny); len(prompts) != 0 || ran != 1 {
		t.Errorf("in a working copy: %d prompt(s), ran %d time(s); want 0 and 1", len(prompts), ran)
	}
	// In the real project: asked, told, and a no is a no.
	prompts, ran := run(true, protocol.ApprovalDeny)
	if len(prompts) != 1 || ran != 0 {
		t.Fatalf("in the real project, declined: %d prompt(s), ran %d time(s); want 1 and 0", len(prompts), ran)
	}
	if !prompts[0].InPlace {
		t.Error("the prompt does not say the command would run in the real project")
	}
	if prompts, ran := run(true, protocol.ApprovalApprove); len(prompts) != 1 || ran != 1 {
		t.Errorf("in the real project, approved: %d prompt(s), ran %d time(s); want 1 and 1", len(prompts), ran)
	}

	// Only a tool that runs code may make the claim.
	registry := mcp.NewRegistry(configPolicy{cfg: &Config{}}, 10)
	t.Cleanup(func() { _ = registry.Close() })
	err := registry.RegisterBuiltin(mcp.Builtin{
		Tool:    mcp.Tool{Name: "read_file", Description: "stub", Schema: schema(`{"type":"object"}`), Confined: true},
		Handler: func(context.Context, json.RawMessage) (mcp.Result, error) { return mcp.Result{}, nil },
		InPlace: func() bool { return true },
	})
	if err == nil || !strings.Contains(err.Error(), "InPlace") {
		t.Errorf("a tool that runs nothing registered an InPlace probe: %v", err)
	}
}
