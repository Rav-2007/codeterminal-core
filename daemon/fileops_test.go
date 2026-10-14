package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// DELETING, MOVING AND RESTORING (fileops.go).

func fileOp(t *testing.T, s *Server, sink *proposalSink, tool string, args map[string]string) mcp.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	var res mcp.Result
	var err error
	switch tool {
	case deleteToolName:
		res, err = s.builtinDelete(raw, sink)
	case moveToolName:
		res, err = s.builtinMove(raw, sink)
	case restoreToolName:
		res, err = s.builtinRestore(raw, sink)
	}
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func trashIDOf(t *testing.T, content string) string {
	t.Helper()
	i := strings.Index(content, "trash as ")
	if i < 0 {
		t.Fatalf("no trash id in %q", content)
	}
	return strings.Fields(content[i+len("trash as "):])[0][:len("20060102-150405-")+8]
}

// A delete goes to the trash, and restore puts it back.
func TestADeleteIsKeptAndCanBeRestored(t *testing.T) {
	_, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}
	file := filepath.Join(project, "notes", "a.txt")
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	_ = os.WriteFile(file, []byte("keep me"), 0o644)

	res := fileOp(t, s, nil, deleteToolName, map[string]string{"path": "notes"})
	if res.IsError || pathExists(file) || !strings.Contains(res.Content, "Deleted the folder notes") {
		t.Fatalf("delete: %+v", res)
	}
	id := trashIDOf(t, res.Content)
	list := fileOp(t, s, nil, restoreToolName, map[string]string{})
	if !strings.Contains(list.Content, id) || !strings.Contains(list.Content, "notes") {
		t.Errorf("the trash list: %q", list.Content)
	}
	back := fileOp(t, s, nil, restoreToolName, map[string]string{"id": id})
	if back.IsError || !pathExists(file) {
		t.Fatalf("restore: %+v", back)
	}
	if data, _ := os.ReadFile(file); string(data) != "keep me" {
		t.Errorf("restored content %q", data)
	}
}

func TestAMoveNeverOverwrites(t *testing.T) {
	_, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}
	_ = os.WriteFile(filepath.Join(project, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(project, "b.txt"), []byte("b"), 0o644)
	if res := fileOp(t, s, nil, moveToolName, map[string]string{"from": "a.txt", "to": "b.txt"}); !res.IsError {
		t.Fatal("a move overwrote a file")
	}
	res := fileOp(t, s, nil, moveToolName, map[string]string{"from": "a.txt", "to": "sub/dir/c.txt"})
	if res.IsError || !pathExists(filepath.Join(project, "sub", "dir", "c.txt")) || pathExists(filepath.Join(project, "a.txt")) {
		t.Fatalf("move: %+v", res)
	}
	_ = os.MkdirAll(filepath.Join(project, "d"), 0o755)
	if res := fileOp(t, s, nil, moveToolName, map[string]string{"from": "d", "to": "d/e"}); !res.IsError {
		t.Error("a folder was moved into itself")
	}
}

// The places an edit may never write are the places these never touch.
func TestFileOperationsAreRefusedWhereAnEditIs(t *testing.T) {
	home, project := homeAndProject(t)
	s := &Server{cfg: &Config{}, workspace: project, logger: quietLogger()}
	for _, p := range []string{".git/config", ".mochiii/x", ".env", "sub/.git", "sub"} {
		full := filepath.Join(project, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if !strings.HasSuffix(p, "sub") {
			_ = os.WriteFile(full, []byte("x"), 0o600)
		}
	}
	_ = os.MkdirAll(filepath.Join(project, "sub", ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("k"), 0o600)
	_ = os.MkdirAll(filepath.Join(home, "Desktop", "link"), 0o755)

	for _, p := range []string{
		".git", ".git/config", ".mochiii", ".env", // protected, a secret
		"sub",           // holds a .git
		".",             // the project
		"../",           // its folder
		"~/.ssh/id_rsa", // hidden in home
		"~",             // home itself
		"/etc/hostname", // outside home
		"~/bin/x",       // ~/bin
	} {
		res := fileOp(t, s, nil, deleteToolName, map[string]string{"path": p})
		if !res.IsError {
			t.Errorf("deleting %q was allowed: %s", p, res.Content)
		}
	}
	if !pathExists(filepath.Join(project, ".git", "config")) || !pathExists(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Fatal("a refused path was deleted anyway")
	}

	// A link that looks harmless is judged by where it leads.
	link := filepath.Join(home, "Desktop", "innocent")
	if err := os.Symlink(filepath.Join(home, ".ssh"), link); err == nil {
		if res := fileOp(t, s, nil, deleteToolName, map[string]string{"path": "~/Desktop/innocent/id_rsa"}); !res.IsError {
			t.Error("a delete through a link into ~/.ssh was allowed")
		}
	}

	// In the home folder, where an edit may write, it works.
	doc := filepath.Join(home, "Documents", "old.txt")
	_ = os.MkdirAll(filepath.Dir(doc), 0o755)
	_ = os.WriteFile(doc, []byte("x"), 0o644)
	if res := fileOp(t, s, nil, deleteToolName, map[string]string{"path": "~/Documents/old.txt"}); res.IsError || pathExists(doc) {
		t.Errorf("deleting ~/Documents/old.txt: %+v", res)
	}
	if !pathExists(filepath.Join(home, ".mochiii", trashDirName)) {
		t.Error("a home-folder delete did not go to ~/.mochiii/trash")
	}
}

// With a working copy, the copy follows: an edit the agent made to a file it
// then moved is offered at the new path, and a deleted file is not reported as
// "deleted in the copy".
func TestTheWorkingCopyFollowsAMoveAndADelete(t *testing.T) {
	s, project := stageProject(t, map[string]string{"a.go": "package a\n", "gone.go": "package g\n"})
	sink := stagedSink(t, s)
	if _, isErr := propose(t, s, sink, "a.go", "package a\n", "package a // edited\n"); isErr {
		t.Fatal("the edit was refused")
	}
	if res := fileOp(t, s, sink, moveToolName, map[string]string{"from": "a.go", "to": "pkg/a.go"}); res.IsError {
		t.Fatalf("move: %+v", res)
	}
	if res := fileOp(t, s, sink, deleteToolName, map[string]string{"path": "gone.go"}); res.IsError {
		t.Fatalf("delete: %+v", res)
	}
	if !pathExists(filepath.Join(project, "pkg", "a.go")) || pathExists(filepath.Join(project, "gone.go")) {
		t.Fatal("the real project did not change")
	}
	blocks, info, _ := sink.finish()
	if len(blocks) != 1 || blocks[0].FilePath != "pkg/a.go" || !strings.Contains(blocks[0].Replace, "edited") {
		t.Errorf("offered %+v", blocks)
	}
	if info != nil {
		for _, n := range info.NotOffered {
			if strings.Contains(n, "gone.go") || strings.Contains(n, "a.go: deleted") {
				t.Errorf("a file the user agreed to delete or move is reported: %q", n)
			}
		}
	}
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// Each file operation asks every time, even under a config that allows it.
func TestFileOperationsAskEvenWhenAllowed(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		deleteToolName: PolicyAllow, moveToolName: PolicyAllow, restoreToolName: PolicyAllow, "read_file": PolicyAllow,
	}}}}
	p := configPolicy{cfg: cfg}
	for _, name := range []string{deleteToolName, moveToolName, restoreToolName} {
		if got := p.PolicyFor(mcp.BuiltinServerName, name); got != mcp.PolicyAsk {
			t.Errorf("%s under allow resolves to %v, want ask", name, got)
		}
	}
	if got := p.PolicyFor(mcp.BuiltinServerName, "read_file"); got != mcp.PolicyAllow {
		t.Errorf("read_file under allow resolves to %v; only the file operations are held to ask", got)
	}
	// A turn's "yes" to one path is not yes to another.
	tool := mcp.Tool{Name: deleteToolName, Destructive: true}
	if grantKey(tool, "builtin__delete_path", `{"path":"a"}`) == grantKey(tool, "builtin__delete_path", `{"path":"b"}`) {
		t.Error("a grant to delete one path covers another")
	}
}
