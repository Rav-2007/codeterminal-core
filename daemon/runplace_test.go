package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// ONE VIEW OF THE PROJECT (runplace.go).

// What a line does decides where it runs: everything in the working copy but
// git, which has no .git there, and package installers, which change the
// environment. A line that mixes them is refused.
func TestShellLinesAreSortedByWhereTheyMayRun(t *testing.T) {
	for _, tc := range []struct {
		line string
		want lineKind
	}{
		{"mvn test", lineAnywhere},
		{"cd /tmp/w && mvn test -Dtest=FuzzFixesTest -q 2>&1", lineAnywhere},
		{"python -m pytest tests/ -x", lineAnywhere},
		{"timeout 60 pytest -x", lineAnywhere},
		{"env CC=gcc make", lineAnywhere},
		{"CTEST_OUTPUT_ON_FAILURE=1 ctest --test-dir build", lineAnywhere},
		{"npm test", lineAnywhere},
		{"npm run build", lineAnywhere},
		{"uv run pytest", lineAnywhere},
		{"pip show more-itertools", lineAnywhere},
		{"cargo test", lineAnywhere},
		{"go test ./...", lineAnywhere},
		{"echo 'a && git push'", lineAnywhere},
		{"cat > /tmp/t.c << 'EOF'\n#include <stdio.h>\ngit commit -am x\nEOF\ngcc /tmp/t.c", lineAnywhere},

		{"git status --porcelain && git diff --stat && git log --oneline -3", lineGitRead},
		{"git -C sub log -1", lineGitRead},
		{"git branch", lineGitRead},
		{"git branch -a", lineGitRead},
		{"git stash list", lineGitRead},

		{"git commit -am x", lineGitWrite},
		{"git -C sub checkout main", lineGitWrite},
		{"git branch feature", lineGitWrite},
		{"git stash", lineGitWrite},
		{"FOO=1 git push", lineGitWrite},
		{"git add . && git commit -m x", lineGitWrite},
		{"git status && git commit -m x", lineGitWrite},

		{"pip install -e .", lineInstall},
		{"python3 -m pip install requests", lineInstall},
		{"pip3 install pytest", lineInstall},
		{"npm install lodash", lineInstall},
		{"npm ci", lineInstall},
		{"yarn", lineInstall},
		{"uv pip install x", lineInstall},
		{"sudo make install", lineInstall},
		{"go get example.com/x", lineInstall},
		{"brew install cmake", lineInstall},

		{"npm install && npm test", lineMixed},
		{"git add . && make", lineMixed},
		{"pip install -e . && pytest", lineMixed},
	} {
		if got := classifyLine(tc.line); got != tc.want {
			t.Errorf("classifyLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// The project's own path, written into a command, means the copy. Only a whole
// path or its start is replaced: /home/u/proj2 is another folder.
func TestProjectPathsInACommandArePointedAtTheCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(home, "proj")
	st := &stagedWorkspace{real: real, root: "/cache/st-1"}
	for _, tc := range []struct{ in, want string }{
		{"cd " + real + " && mvn test", "cd /cache/st-1 && mvn test"},
		{"cat '" + real + "/src/a.c'", "cat '/cache/st-1/src/a.c'"},
		{"cmake --install-prefix=" + real + "/out", "cmake --install-prefix=/cache/st-1/out"},
		{"ls " + real, "ls /cache/st-1"},
		{"cd " + filepath.ToSlash(real) + "/src && make", "cd /cache/st-1/src && make"},
		{"ls " + real + "2/x", "ls " + real + "2/x"},
		{"ls " + real + ".bak", "ls " + real + ".bak"},
		{"ls /x" + real, "ls /x" + real},
		{"ls ~/project", "ls ~/project"},
	} {
		if got := st.toCopy(tc.in); got != tc.want {
			t.Errorf("toCopy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The ~/ form a shell expands (POSIX shells only).
	if runtime.GOOS != "windows" {
		if got := st.toCopy("cd ~/proj && make"); got != "cd /cache/st-1 && make" {
			t.Errorf("the ~/ form of the project was not pointed at the copy: %q", got)
		}
	}
	// A second spelling of the project the daemon knows (s.workspace before
	// symlinks are resolved) is pointed at the copy as well.
	alias := filepath.Join(home, "link", "proj")
	if got := st.toCopy("cd "+alias+" && make", alias); got != "cd /cache/st-1 && make" {
		t.Errorf("an alias of the project was not pointed at the copy: %q", got)
	}
}

// A command starts in a folder of the project, named relative to it or by its
// real path. "/etc" is refused on every platform -- Windows reads it as the
// root of the drive, not as absolute -- never run in a folder of the copy that
// happens to share its name.
func TestACommandStartsOnlyInAFolderOfTheProject(t *testing.T) {
	s, real := stageProject(t, map[string]string{"etc/x": "", "src/a.c": ""})
	st, err := stagedSink(t, s).workingCopy()
	if err != nil || st == nil {
		t.Fatalf("no working copy: %v", err)
	}
	for _, cwd := range []string{"src", filepath.Join(real, "src")} {
		if dir, err := st.commandDir(cwd); err != nil || dir != filepath.Join(st.root, "src") {
			t.Errorf("commandDir(%q) = %q, %v; want the copy's src", cwd, dir, err)
		}
	}
	for _, cwd := range []string{"/etc", "..", "../x"} {
		if dir, err := st.commandDir(cwd); err == nil {
			t.Errorf("commandDir(%q) = %q, not refused", cwd, dir)
		}
	}
}

// posixRunPlace is a staged project whose commands run through /bin/sh.
func posixRunPlace(t *testing.T, files map[string]string) (*Server, string, *proposalSink) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("these commands are POSIX shell")
	}
	t.Setenv("SHELL", "/bin/sh")
	s, real := stageProject(t, files)
	return s, real, stagedSink(t, s)
}

func runIn(t *testing.T, s *Server, sink *proposalSink, args map[string]any) mcp.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.builtinRunCommandIn(context.Background(), raw, sink)
	if err != nil {
		t.Fatalf("run_command %v: %v", args, err)
	}
	return res
}

// THE FOUR FAILURES OF THE TWO COPIES, each made impossible: a command sees the
// agent's edit (wrong-copy tests); a rewrite through the shell comes back with
// its diff, named as the command's (no review, no undo); a heredoc's file is not
// offered (stray files); and an absolute path into the project means the copy.
func TestRunCommandWorksOnTheSameFilesAsTheEdits(t *testing.T) {
	s, real, sink := posixRunPlace(t, map[string]string{"a.txt": "old\n", "b.txt": "keep\n"})
	if out, failed := propose(t, s, sink, "a.txt", "old", "new"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}

	if res := runIn(t, s, sink, map[string]any{"command": "cat a.txt"}); !strings.Contains(res.Content, "new") ||
		!strings.Contains(res.Content, "working copy") {
		t.Errorf("run_command did not see the edit, or did not say where it ran: %q", res.Content)
	}
	if res := runIn(t, s, sink, map[string]any{"command": "cd " + real + " && cat a.txt"}); !strings.Contains(res.Content, "new") {
		t.Errorf("an absolute path into the project ran on the real files: %q", res.Content)
	}
	runIn(t, s, sink, map[string]any{"command": "echo rewritten > b.txt"})
	res := runIn(t, s, sink, map[string]any{"command": "cat > Stray.java <<'EOF'\nclass Stray {}\nEOF"})
	if !strings.Contains(res.Content, "Stray.java") || !strings.Contains(res.Content, "not offered") {
		t.Errorf("the model was not told a created file is not offered: %q", res.Content)
	}

	for rel, want := range map[string]string{"a.txt": "old\n", "b.txt": "keep\n"} {
		if got, _ := os.ReadFile(filepath.Join(real, rel)); string(got) != want {
			t.Errorf("the real %s changed before any review: %q", rel, got)
		}
	}
	if pathExists(filepath.Join(real, "Stray.java")) {
		t.Error("a heredoc left a file in the real project")
	}

	blocks, info, _ := sink.finish()
	offered := map[string]bool{}
	for _, b := range blocks {
		offered[b.FilePath] = true
	}
	if !offered["a.txt"] || !offered["b.txt"] || offered["Stray.java"] {
		t.Errorf("offered %v; want a.txt and b.txt, never Stray.java", offered)
	}
	if info == nil || !strings.Contains(strings.Join(info.ByCommand, "\n"), "b.txt (echo rewritten > b.txt)") {
		t.Errorf("the shell's rewrite of b.txt is not named as the command's: %+v", info)
	}
}

// What a command changes in the REAL project anyway -- an installer, or a path
// the mapping cannot see -- is named to the model and in the review.
func TestAChangeToTheRealProjectIsNamed(t *testing.T) {
	s, real, sink := posixRunPlace(t, map[string]string{"package.json": "{}\n", "a.txt": "old\n"})
	if out, failed := propose(t, s, sink, "a.txt", "old", "new"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}
	// A stand-in npm: an installer runs in the real project.
	bin := t.TempDir()
	script := "#!/bin/sh\necho '{\"dependencies\":{}}' > package.json\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	res := runIn(t, s, sink, map[string]any{"command": "npm install lodash"})
	if !strings.Contains(res.Content, "package.json") || !strings.Contains(res.Content, "outside review") ||
		!strings.Contains(res.Content, "does not have your 1 edited file (a.txt)") {
		t.Errorf("an install in the real project was not described: %q", res.Content)
	}
	if got, _ := os.ReadFile(filepath.Join(real, "package.json")); !strings.Contains(string(got), "dependencies") {
		t.Errorf("the installer did not run in the real project: %q", got)
	}

	// Spelt so the mapping cannot see it: the shell builds the path.
	parent, base := filepath.Dir(real), filepath.Base(real)
	res = runIn(t, s, sink, map[string]any{"command": "P=" + parent + "; echo x > $P/" + base + "/escaped.txt"})
	if !strings.Contains(res.Content, "escaped.txt") || !strings.Contains(res.Content, "REAL project") {
		t.Errorf("a write into the real project was not named: %q", res.Content)
	}
	_, info, _ := sink.finish()
	if info == nil || len(info.OutsideReview) != 2 ||
		!strings.HasPrefix(info.OutsideReview[0], "package.json (npm install lodash)") ||
		!strings.HasPrefix(info.OutsideReview[1], "escaped.txt (") {
		t.Errorf("the review does not name the changes outside it: %+v", info)
	}
}

// A git command that writes would act on the repository underneath the copy,
// so it is refused while the turn has one -- before anyone is asked. One that
// reads runs in the real project, and a line mixing places is refused.
func TestGitWritesWaitForTheReview(t *testing.T) {
	s, _, sink := posixRunPlace(t, map[string]string{"a.txt": "old\n"})
	arg := func(command string) json.RawMessage {
		raw, _ := json.Marshal(map[string]string{"command": command})
		return raw
	}
	// No copy yet: a git write runs where it always ran.
	if inPlace, why := s.runCommandPlacement(arg("git commit -m x"), sink); !inPlace || why != "" {
		t.Errorf("before any copy, git commit: in place %v, refused %q", inPlace, why)
	}
	if out, failed := propose(t, s, sink, "a.txt", "old", "new"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}
	for command, refused := range map[string]bool{
		"git commit -am x": true, "git stash": true, "git checkout main": true,
		"npm install && npm test": true, "git status": false, "git log -3": false,
	} {
		inPlace, why := s.runCommandPlacement(arg(command), sink)
		if (why != "") != refused || !inPlace {
			t.Errorf("%q: in place %v, refused %q; want in place, refused %v", command, inPlace, why, refused)
		}
	}
	if _, why := s.runCommandPlacement(arg("git commit -am x"), sink); !strings.Contains(why, "a.txt") {
		t.Errorf("the refusal does not name the edits it protects: %q", why)
	}
	if inPlace, why := s.runCommandPlacement(arg("make test"), sink); inPlace || why != "" {
		t.Errorf("make test: in place %v, refused %q; want the working copy", inPlace, why)
	}
}

// The refusal reaches the model through the loop, and the user is never asked.
func TestARefusedCommandIsNeverPutToTheUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__run_command", `{"command":"git commit -am wip"}`), textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true})
	s.workspace = t.TempDir()
	sink := &proposalSink{stageFrom: s.workingCopySource("auto")}
	t.Cleanup(sink.discard)
	if _, err := sink.workingCopy(); err != nil {
		t.Fatal(err)
	}
	registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, 50)
	t.Cleanup(func() { _ = registry.Close() })
	for _, b := range s.runCommandTools("auto", sink) {
		if err := registry.RegisterBuiltin(b); err != nil {
			t.Fatal(err)
		}
	}
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	var activity []protocol.ToolActivity
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "commit it"}}, providerRouting{}, appr,
		func(string) error { return nil }, func(a protocol.ToolActivity) { activity = append(activity, a) }, nil, nil,
		func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if len(appr.prompts()) != 0 {
		t.Errorf("the user was asked about a call that could not run: %+v", appr.prompts())
	}
	denied := false
	for _, a := range activity {
		denied = denied || (a.Phase == protocol.ToolPhaseDenied && strings.Contains(a.Detail, "working in a private copy"))
	}
	if !denied || res.FinalText != "done" {
		t.Errorf("the refusal did not reach the model as a refused call: denied=%v final=%q", denied, res.FinalText)
	}
}

// An editable install of the real project would otherwise win, and the tests
// the agent runs in the copy would import the code without its edits.
func TestPythonInTheCopyImportsTheCopysCode(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("NOT RUN: no python3 on this machine")
	}
	s, real, sink := posixRunPlace(t, map[string]string{"src/pkg/__init__.py": "VALUE = 1\n"})
	// What `pip install -e .` amounts to for a src/ layout: the REAL src on the path.
	t.Setenv("PYTHONPATH", filepath.Join(real, "src"))
	if out, failed := propose(t, s, sink, "src/pkg/__init__.py", "VALUE = 1", "VALUE = 2"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}
	res := runIn(t, s, sink, map[string]any{"command": python + ` -c "import pkg; print('VALUE', pkg.VALUE)"`})
	if !strings.Contains(res.Content, "VALUE 2") {
		t.Errorf("Python in the copy imported the real project's code: %q", res.Content)
	}
	if pathExists(filepath.Join(sink.stage.root, "src", "pkg", "__pycache__")) {
		t.Error("Python wrote a bytecode cache into the copy")
	}
}

// With no working copy to be had, run_command runs where it always ran.
func TestWithNoCopyRunCommandRunsInTheProject(t *testing.T) {
	s, real, _ := posixRunPlace(t, map[string]string{"a.txt": "old\n"})
	sink := &proposalSink{} // plan-like: no copy
	if inPlace, why := s.runCommandPlacement(json.RawMessage(`{"command":"make"}`), sink); !inPlace || why != "" {
		t.Errorf("no copy: in place %v, refused %q", inPlace, why)
	}
	runIn(t, s, sink, map[string]any{"command": "echo made > new.txt"})
	if !pathExists(filepath.Join(real, "new.txt")) {
		t.Error("with no copy, the command did not run in the real project")
	}
}

// The same for sandbox_exec, which runs in the same copy: with the real src/
// on the path (an editable install), the copy's edited code is what imports.
func TestSandboxExecInTheCopyImportsTheCopysCode(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("NOT RUN: no python3 on this machine")
	}
	s, real := stageProject(t, map[string]string{
		"pyproject.toml":      "[project]\nname = \"pkg\"\nversion = \"0\"\n",
		"src/pkg/__init__.py": "VALUE = 1\n",
	})
	if !s.sandboxExecConfined() {
		t.Skip("NOT RUN: this host does not confine sandbox_exec, so python3 is not offered to it")
	}
	t.Setenv("PYTHONPATH", filepath.Join(real, "src"))
	sink := stagedSink(t, s)
	if out, failed := propose(t, s, sink, "src/pkg/__init__.py", "VALUE = 1", "VALUE = 2"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}
	res, err := s.builtinSandboxExecStaged(context.Background(),
		json.RawMessage(`{"command":"python3 -c \"import pkg; print('VALUE', pkg.VALUE)\""}`), sink)
	if err != nil || !strings.Contains(res.Content, "VALUE 2") {
		t.Errorf("sandbox_exec in the copy imported the real project's code (%v): %q", err, res.Content)
	}
}

// A build or a test run in the working copy checks the working copy, whichever
// tool ran it: it is the check line above the review, as a sandbox_exec run is.
// Reading a file is not a check.
func TestARunInTheCopyIsACheckOfIt(t *testing.T) {
	makefile := "check:\n\tgrep -q new a.txt\nstale:\n\tgrep -q old a.txt\n"
	s, _, sink := posixRunPlace(t, map[string]string{"a.txt": "old\n", "Makefile": makefile})
	if out, failed := propose(t, s, sink, "a.txt", "old", "new"); failed {
		t.Fatalf("the edit did not land in the working copy: %s", out)
	}
	runIn(t, s, sink, map[string]any{"command": "make check"})
	if c := sink.lastCheck(); c == nil || c.command != "make check" || !c.passed || c.edits != 1 {
		t.Fatalf("a passing test run in the copy was not recorded as a check of the final version: %+v", c)
	}
	runIn(t, s, sink, map[string]any{"command": "cat a.txt"})
	if c := sink.lastCheck(); c == nil || c.command != "make check" {
		t.Errorf("reading a file was recorded as a check: %+v", c)
	}
	runIn(t, s, sink, map[string]any{"command": "make stale"})
	_, info, _ := sink.finish()
	if info == nil || info.Checked != "make stale" || info.Passed {
		t.Errorf("the failing run is not the last check reported: %+v", info)
	}
}
