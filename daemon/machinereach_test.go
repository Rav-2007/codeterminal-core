package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// The lines of the reach section, by what each one claims.
const (
	reachReadAnywhere = "ANYWHERE on the machine, not only in the project"
	reachWriteHome    = "ANYWHERE IN THE USER'S HOME FOLDER"
	reachSpecsOnly    = "under specs/ only"
	reachNoWrite      = "You cannot create or change files in this turn."
	reachNoDelete     = "You CANNOT delete, move or rename a file or folder"
	reachRun          = "RUN only build and test commands ("
	reachNoRun        = "You cannot run commands in this turn."
)

// reachFor is the section a real turn in mode gets: the real registry, built
// the way runAgentTurn builds it, under cfg's policy.
func reachFor(t *testing.T, s *Server, mode string) (reach string, offered func(string) bool) {
	t.Helper()
	registry, errs := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, mode)
	if len(errs) != 0 {
		t.Fatalf("building the registry for mode %q: %v", mode, errs)
	}
	t.Cleanup(func() { _ = registry.Close() })
	const base = "BASE"
	in := []chatMessage{{Role: "system", Content: base}, {Role: "user", Content: "hello"}}
	msgs := s.withMachineReach(context.Background(), registry, mode, in)
	if len(msgs) != 2 || msgs[1].Content != in[1].Content || !strings.HasPrefix(msgs[0].Content, base+"\n\n") {
		t.Fatalf("mode %q: the section should follow the system prompt and touch nothing else, got %+v", mode, msgs)
	}
	if in[0].Content != base {
		t.Fatalf("mode %q: the caller's own message list was changed: %q", mode, in[0].Content)
	}
	// With no system message there is nothing to add it to, as for every note.
	if bare := s.withMachineReach(context.Background(), registry, mode, in[1:]); len(bare) != 1 || bare[0].Content != in[1].Content {
		t.Fatalf("mode %q: a request with no system message was changed: %+v", mode, bare)
	}
	advertised, _ := registry.Advertised(context.Background())
	names := map[string]bool{}
	for _, tool := range advertised {
		if tool.Server == mcp.BuiltinServerName {
			names[tool.Name] = true
		}
	}
	return strings.TrimPrefix(msgs[0].Content, base+"\n\n"), func(tool string) bool { return names[tool] }
}

// EVERY SENTENCE IS TRUE OF THE TOOLS THE TURN REALLY HAS, in every mode. The
// section exists because the model described abilities it did not have and
// denied ones it did; a section that promised propose_edit to plan mode, or
// kept promising a tool the config denies, would be that defect again with the
// daemon's own authority behind it.
func TestMachineReachSaysOnlyWhatTheToolsDo(t *testing.T) {
	_, project := homeAndProject(t)
	modes := []string{"", modeAuto, modeManual, modePlan, modeSpec, modeCheck, modeBuild,
		modeTask, modeDebug, modeFix, modeRefactor, modeHunt}

	for _, mode := range modes {
		s := &Server{cfg: &Config{MCP: MCPConfig{Enabled: true}}, workspace: project, logger: quietLogger()}
		reach, offered := reachFor(t, s, mode)
		if !strings.HasPrefix(reach, machineReachHeader) {
			t.Errorf("mode %q: the section does not open by saying what it is:\n%s", mode, reach)
		}
		// Able to reach is not a reason to go: told it could read anywhere, the
		// model went through the user's home folder for a file that was simply
		// missing from the project (run live 2026-10-06).
		if !strings.Contains(reach, "Work inside the project unless the request points somewhere else") {
			t.Errorf("mode %q: nothing tells the model to stay in the project unless asked:\n%s", mode, reach)
		}

		canRead := offered(builtinReadFileName) || offered(builtinListDirectoryName)
		if got := strings.Contains(reach, reachReadAnywhere); got != canRead {
			t.Errorf("mode %q: says it reads anywhere = %v, but read tools offered = %v", mode, got, canRead)
		}

		writesHome := offered(builtinProposeEditName) && !isSpecMode(mode)
		if got := strings.Contains(reach, reachWriteHome); got != writesHome {
			t.Errorf("mode %q: says it writes in the home folder = %v, but propose_edit does = %v", mode, got, writesHome)
		}
		if got := strings.Contains(reach, reachSpecsOnly); got != (offered(builtinProposeEditName) && isSpecMode(mode)) {
			t.Errorf("mode %q: says it writes under specs/ only = %v", mode, got)
		}
		if got := strings.Contains(reach, reachNoWrite); got != !offered(builtinProposeEditName) {
			t.Errorf("mode %q: says it cannot write = %v, but propose_edit offered = %v", mode, got, offered(builtinProposeEditName))
		}

		if got := strings.Contains(reach, reachRun); got != offered(builtinSandboxExecName) {
			t.Errorf("mode %q: says it runs commands = %v, but sandbox_exec offered = %v", mode, got, offered(builtinSandboxExecName))
		}
		if got := strings.Contains(reach, reachNoRun); got != !offered(builtinSandboxExecName) {
			t.Errorf("mode %q: says it cannot run commands = %v", mode, got)
		}
		if offered(builtinSandboxExecName) && !strings.Contains(reach, reachRun+strings.Join(s.execPrograms(), ", ")+")") {
			t.Errorf("mode %q: the programs named are not the ones sandbox_exec starts (%v):\n%s", mode, s.execPrograms(), reach)
		}

		// "No tool does it" -- in every mode, and by looking at the menu.
		if !strings.Contains(reach, reachNoDelete) {
			t.Errorf("mode %q: nothing says files cannot be deleted, moved or renamed", mode)
		}
		for _, b := range s.builtinTools(&proposalSink{}, mode) {
			if b.Tool.Name == "rename_symbol" {
				continue // renames an identifier across the code, through propose_edit's review; no file moves
			}
			for _, verb := range []string{"delete", "remove", "move", "rename", "unlink", "trash"} {
				if strings.Contains(b.Tool.Name, verb) {
					t.Errorf("mode %q offers %s: the section still tells the model no tool can %s a file -- "+
						"update machineReach along with the tool", mode, b.Tool.Name, verb)
				}
			}
		}
	}

	// ANTI-VACUITY: the modes above must actually differ, or every "iff" passed
	// by both sides being constant.
	s := &Server{cfg: &Config{MCP: MCPConfig{Enabled: true}}, workspace: project, logger: quietLogger()}
	plain, _ := reachFor(t, s, "")
	plan, _ := reachFor(t, s, modePlan)
	if !strings.Contains(plain, reachWriteHome) || !strings.Contains(plain, reachRun) {
		t.Fatalf("an ordinary turn is not told it can edit or run:\n%s", plain)
	}
	if !strings.Contains(plan, reachNoWrite) || !strings.Contains(plan, reachNoRun) || strings.Contains(plan, reachWriteHome) {
		t.Fatalf("plan mode is told it can edit or run:\n%s", plan)
	}
}

// A tool the user's own config denies is not promised either.
func TestMachineReachFollowsTheConfiguredPolicy(t *testing.T) {
	_, project := homeAndProject(t)
	denied := MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		builtinReadFileName: "deny", builtinListDirectoryName: "deny", builtinProposeEditName: "deny",
	}}}
	s := &Server{cfg: &Config{MCP: denied}, workspace: project, logger: quietLogger()}
	reach, offered := reachFor(t, s, "")
	if offered(builtinReadFileName) || offered(builtinProposeEditName) {
		t.Fatal("the policy under test denies nothing; the test proves nothing")
	}
	for _, promised := range []string{reachReadAnywhere, reachWriteHome} {
		if strings.Contains(reach, promised) {
			t.Errorf("a denied tool is still promised (%q):\n%s", promised, reach)
		}
	}
	for _, want := range []string{"You cannot read files or list folders in this turn.", reachNoWrite} {
		if !strings.Contains(reach, want) {
			t.Errorf("the section does not say %q:\n%s", want, reach)
		}
	}

	// One of the two readers: only the one offered is named.
	one := MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{builtinListDirectoryName: "deny"}}}
	s = &Server{cfg: &Config{MCP: one}, workspace: project, logger: quietLogger()}
	reach, _ = reachFor(t, s, "")
	if !strings.Contains(reach, "READ files ANYWHERE") || strings.Contains(reach, "list_directory") {
		t.Errorf("with list_directory denied the section reads:\n%s", reach)
	}
}

// THE CLAIMS THEMSELVES, AGAINST THE CODE THAT DECIDES THEM. The section says
// "the home folder, except hidden files, ~/bin and .desktop files, and nothing
// outside it" and "anywhere, except keys, credential stores and histories".
// Those are the tools' rules restated; if a rule moves, this fails and the
// sentence is the thing to go and read.
func TestMachineReachMatchesWhatTheToolsAcceptAndRefuse(t *testing.T) {
	home, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}

	propose := func(path string) bool {
		args, _ := json.Marshal(map[string]string{"path": path, "search": "", "replace": "x\n"})
		res, err := s.builtinProposeEdit(context.Background(), args, &proposalSink{})
		if err != nil {
			t.Fatalf("propose_edit(%s): %v", path, err)
		}
		return !res.IsError
	}
	for _, ok := range []string{"notes.md", "~/Desktop/notes/todo.md", "~/Documents/plan.md", "~/new-folder/deep/er/file.txt"} {
		if !propose(ok) {
			t.Errorf("the section promises %s can be created, and propose_edit refuses it", ok)
		}
	}
	for _, refused := range []string{"~/.bashrc", "~/.config/app/settings.json", "~/bin/tool", "~/Desktop/app.desktop", "/etc/hosts", "/tmp/outside-home.txt"} {
		if propose(refused) {
			t.Errorf("the section says %s is always refused, and propose_edit accepts it", refused)
		}
	}

	for _, tool := range []string{builtinReadFileName, builtinListDirectoryName} {
		if !outsideReadTools[tool] {
			t.Errorf("the section says %s reaches anywhere on the machine, and it is not an outside-read tool", tool)
		}
	}
	// Joined, not glued with "/": these are the resolved paths the tools hand
	// the check, and on Windows home+"/.ssh" is not one (the first version of
	// this test did that and failed there, 2026-10-06).
	state, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, refused := range []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".bash_history"),
		filepath.Join(home, ".mochiii", "credentials.json"),
		filepath.Join(state, "memory.db"),
		filepath.Join(home, ".config", "Code", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, ".local", "share", "keyrings", "login.keyring"),
		"/proc/self/environ", "/sys/kernel", "/dev/fd/0", "/run/user/1000",
	} {
		if reason := outsideReadRefusal(refused); reason == "" {
			t.Errorf("the section says keys, credential stores, histories and /proc /sys /dev /run are always refused, and %s is readable", refused)
		}
	}
	section := machineReach(func(string) bool { return true }, "auto", nil)
	for _, claim := range []string{"Mochiii's own key", "editors' secret stores", "keyrings", "/proc, /sys, /dev and /run"} {
		if !strings.Contains(section, claim) {
			t.Errorf("the section no longer names %q as refused", claim)
		}
	}
	for _, allowed := range []string{filepath.Join(home, "Documents", "report.txt"), "/etc/hostname", filepath.Join(home, ".bashrc")} {
		if reason := outsideReadRefusal(allowed); reason != "" {
			t.Errorf("the section says files anywhere can be read after a yes, and %s is refused outright: %s", allowed, reason)
		}
	}
}

// OVER THE REAL SOCKET: an agent turn's request opens with the base prompt and
// then the section; a client that cannot answer approvals gets no tools, so it
// gets no section, and its request is the one it always was.
func TestAnAgentTurnIsToldWhatItCanReachAndAPlainTurnIsUnchanged(t *testing.T) {
	const base = "BASE PROMPT, as configured."
	systemOf := func(caps []string) string {
		upstream, _, bodies := agentUpstream(t, textSSE("an answer"))
		sockAddr, _, _ := agentSocketServerWith(t, upstream, MCPConfig{Enabled: true}, discardLogger(), base)
		_, dec, conn := agentClient(t, sockAddr, "can you work anywhere on my machine?", caps)
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			var tok protocol.TokenResponse
			if err := dec.Decode(&tok); err != nil {
				t.Fatalf("reading the stream: %v", err)
			}
			if tok.Done {
				break
			}
		}
		if len(*bodies) == 0 {
			t.Fatal("no request reached the provider")
		}
		var body struct {
			Messages []chatMessage `json:"messages"`
		}
		if err := json.Unmarshal((*bodies)[0], &body); err != nil {
			t.Fatalf("upstream body: %v", err)
		}
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
			t.Fatalf("the request does not open with a system message: %+v", body.Messages)
		}
		for _, m := range body.Messages[1:] {
			if m.Role == "system" {
				t.Errorf("a second system message was sent: %q", m.Content)
			}
		}
		return body.Messages[0].Content
	}

	agent := systemOf([]string{protocol.CapToolApproval})
	if !strings.HasPrefix(agent, base+"\n\n") || strings.Count(agent, machineReachHeader) != 1 {
		t.Fatalf("an agent turn's system message is not the base prompt with the section in it once:\n%s", agent)
	}
	for _, want := range []string{reachReadAnywhere, reachWriteHome, reachNoDelete, reachRun} {
		if !strings.Contains(agent, want) {
			t.Errorf("an agent turn is not told %q:\n%s", want, agent)
		}
	}

	if plain := systemOf(nil); plain != base {
		t.Errorf("a turn with no tools was sent more than the base prompt:\n%s", plain)
	}
}
