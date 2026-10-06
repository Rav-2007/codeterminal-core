package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// Reading outside the workspace: allowed, but only with a person's yes for
// the exact path, and never from a credential store. See outsideread.go.

// outsideTree makes a directory OUTSIDE the test workspace holding one
// ordinary file and one private key, and returns the directory.
func outsideTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("DESKTOP-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ssh", "id_rsa"), []byte("PRIVATE-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// allowReads is the shipped agent config's shape: reads run without asking.
func allowReads() MCPConfig {
	return MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		"read_file": "allow", "list_directory": "allow",
	}}}
}

// sentToModel is everything the loop sent upstream after the first request --
// where a tool's result would appear.
func sentToModel(bodies *[][]byte) string {
	var b strings.Builder
	for i, body := range *bodies {
		if i > 0 {
			b.Write(body)
		}
	}
	return b.String()
}

// THE REPORTED FAILURE: "what is on my desktop" could only be answered "I
// can't". An outside read now runs -- after asking, even though reads are
// config-allowed, and with a prompt that does not call it confined.
func TestAnOutsideReadRunsAfterTheUserIsAsked(t *testing.T) {
	dir := outsideTree(t)
	file := filepath.Join(dir, "notes.txt")
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":`+quote(file)+`}`),
		textSSE("done"),
	)
	s := loopServer(t, base, allowReads())
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	prompts := appr.prompts()
	if len(prompts) != 1 {
		t.Fatalf("got %d prompt(s), want 1: config allow must not cover a read outside the project", len(prompts))
	}
	if prompts[0].Confined {
		t.Error("the prompt called an outside read confined")
	}
	if prompts[0].OutsidePath != resolveOutsidePath(file) {
		t.Errorf("the prompt names %q as the path being read, want %q", prompts[0].OutsidePath, resolveOutsidePath(file))
	}
	if !strings.Contains(sentToModel(bodies), "DESKTOP-CONTENT") {
		t.Error("the approved outside read did not reach the model")
	}
}

func TestADeclinedOutsideReadReadsNothing(t *testing.T) {
	dir := outsideTree(t)
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":`+quote(filepath.Join(dir, "notes.txt"))+`}`),
		textSSE("done"),
	)
	s := loopServer(t, base, allowReads())
	if _, _, err := runLoopWith(t, s, &capturingApprover{decision: protocol.ApprovalDeny}); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if strings.Contains(sentToModel(bodies), "DESKTOP-CONTENT") {
		t.Error("a declined outside read still reached the model")
	}
}

// Credential stores are refused BEFORE anyone is asked: a yes given at speed
// is exactly what an injected "read ~/.ssh/id_rsa" is fishing for.
func TestCredentialStoresAreRefusedWithoutAsking(t *testing.T) {
	dir := outsideTree(t)
	for _, path := range []string{
		filepath.Join(dir, ".ssh", "id_rsa"),
		filepath.Join(dir, ".ssh"),
		"/proc/self/environ", // the daemon's own API key lives here
	} {
		t.Run(path, func(t *testing.T) {
			tool := "read_file"
			if !strings.Contains(filepath.Base(path), "id_rsa") && path != "/proc/self/environ" {
				tool = "list_directory"
			}
			base, _, bodies := agentUpstream(t,
				toolCallSSE("c1", tool, `{"path":`+quote(path)+`}`),
				textSSE("done"),
			)
			s := loopServer(t, base, allowReads())
			appr := &capturingApprover{decision: protocol.ApprovalApprove}
			if _, _, err := runLoopWith(t, s, appr); err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			if n := len(appr.prompts()); n != 0 {
				t.Errorf("the user was asked %d time(s) about a refused path; it must be refused outright", n)
			}
			sent := sentToModel(bodies)
			if strings.Contains(sent, "PRIVATE-KEY") || strings.Contains(sent, "id_rsa") && tool == "list_directory" {
				t.Error("a credential store reached the model")
			}
			if !strings.Contains(sent, "refused") {
				t.Error("the model was not told the read was refused")
			}
		})
	}
}

// A symlink that looks harmless but points into a credential store is judged
// by where it leads.
func TestAnOutsideSymlinkIntoACredentialStoreIsRefused(t *testing.T) {
	dir := outsideTree(t)
	link := filepath.Join(dir, "harmless.txt")
	if err := os.Symlink(filepath.Join(dir, ".ssh", "id_rsa"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":`+quote(link)+`}`),
		textSSE("done"),
	)
	s := loopServer(t, base, allowReads())
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if strings.Contains(sentToModel(bodies), "PRIVATE-KEY") {
		t.Error("a symlink led the read into ~/.ssh")
	}
	if n := len(appr.prompts()); n != 0 {
		t.Errorf("asked %d time(s) about a path that resolves into a credential store", n)
	}
}

// "For this turn" covers the approved PATH for the turn -- not every path.
// Approving ~/Desktop is not approving ~.
func TestAnOutsideTurnGrantCoversOnlyThatPath(t *testing.T) {
	dir := outsideTree(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "b.txt"), []byte("OTHER"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := quote(filepath.Join(dir, "notes.txt"))
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":`+a+`}`),
		toolCallSSE("c2", "read_file", `{"path":`+a+`}`),
		toolCallSSE("c3", "read_file", `{"path":`+quote(filepath.Join(other, "b.txt"))+`}`),
		textSSE("done"),
	)
	s := loopServer(t, base, allowReads())
	appr := &capturingApprover{decision: protocol.ApprovalApproveForTurn}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := len(appr.prompts()); n != 2 {
		t.Errorf("asked %d time(s), want 2: once for the first path (re-read covered by the grant), once for the second", n)
	}
}

// ~ expands to the home directory, and listing an approved folder works and
// still hides secret-shaped names.
func TestListingTildeDesktopAfterApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	desktop := filepath.Join(home, "Desktop")
	if err := os.MkdirAll(filepath.Join(desktop, "Projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"todo.md", ".env"} {
		if err := os.WriteFile(filepath.Join(desktop, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "list_directory", `{"path":"~/Desktop"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, allowReads())
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	sent := sentToModel(bodies)
	for _, want := range []string{"Projects/", "todo.md"} {
		if !strings.Contains(sent, want) {
			t.Errorf("listing ~/Desktop did not return %q", want)
		}
	}
	if strings.Contains(sent, `.env\n`) || strings.Contains(sent, `\n.env`) {
		t.Error("the listing advertised a secret-shaped file")
	}
}

// The handler does not trust the loop: an outside path is read only with an
// approval for that exact resolved path on the call's context.
func TestTheHandlerRefusesOutsideReadsWithoutThatPathsApproval(t *testing.T) {
	s := builtinTestServer(t)
	dir := outsideTree(t)
	file := filepath.Join(dir, "notes.txt")
	args := []byte(`{"path":` + quote(file) + `}`)

	for name, ctx := range map[string]context.Context{
		"no approval":               context.Background(),
		"approval for another path": withApprovedOutsideRead(context.Background(), filepath.Join(dir, "other.txt")),
	} {
		res, err := s.builtinReadFile(ctx, args)
		if err != nil || !res.IsError || strings.Contains(res.Content, "DESKTOP-CONTENT") {
			t.Errorf("%s: outside read was not refused: %+v %v", name, res, err)
		}
	}

	res, err := s.builtinReadFile(withApprovedOutsideRead(context.Background(), resolveOutsidePath(file)), args)
	if err != nil || res.IsError || res.Content != "DESKTOP-CONTENT" {
		t.Errorf("an approved outside read failed: %+v %v", res, err)
	}

	// Even an approval cannot open a refused place.
	key := filepath.Join(dir, ".ssh", "id_rsa")
	res, _ = s.builtinReadFile(withApprovedOutsideRead(context.Background(), key), []byte(`{"path":`+quote(key)+`}`))
	if !res.IsError || strings.Contains(res.Content, "PRIVATE-KEY") {
		t.Errorf("an approval opened ~/.ssh: %+v", res)
	}
}

// An absolute path that is really INSIDE the workspace is an ordinary
// workspace read: no prompt, same confinement as a relative path.
func TestAnAbsolutePathInsideTheWorkspaceIsNotAskedAbout(t *testing.T) {
	// The workspace path is only known once the server exists, so the
	// upstream is pointed at it afterwards.
	s := loopServer(t, "", allowReads())
	inside := filepath.Join(s.workspace, "inside.txt")
	if err := os.WriteFile(inside, []byte("INSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "read_file", `{"path":`+quote(inside)+`}`),
		textSSE("done"),
	)
	s.apiBase = base
	appr := &capturingApprover{decision: protocol.ApprovalDeny}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if n := len(appr.prompts()); n != 0 {
		t.Errorf("asked %d time(s) about a path inside the workspace", n)
	}
	if !strings.Contains(sentToModel(bodies), "INSIDE") {
		t.Error("an absolute path inside the workspace was not read")
	}
}

// A REFUSED PLACE IS REFUSED HOWEVER ITS PATH IS SPELT. The check split a path
// on the platform's own separator and folded case, and nothing else, so these
// came back readable:
//
//   - "/" on Windows, which honours it beside "\": home+"/.ssh/id_ed25519" was
//     one component, "<home's last folder>/.ssh/id_ed25519", on no list.
//   - "\" anywhere else: the same file on an NTFS or SMB mount.
//   - a trailing dot or space, which Win32 drops: ".ssh." opens .ssh.
//
// FOUND 2026-10-06: CI's build #133 (commit 2ba35a7) reported the first two
// paths below readable on its Windows runner. The "/" rows can fail only on
// Windows; the "\" and trailing-dot rows fail everywhere without the fix.
func TestOutsideReadRefusalHoweverThePathIsSpelt(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder:", err)
	}
	refused := []string{
		home + "/.ssh/id_ed25519",
		home + "/.bash_history",
		home + "/.gnupg/private-keys-v1.d/key",
		home + "/code/.git/config",
		home + `\.ssh\id_ed25519`,
		home + `\.bash_history`,
		home + `\.kube\config`,
		home + `\Desktop\prod.pem`,
		filepath.Join(home, ".ssh.", "id_ed25519"),
		filepath.Join(home, ".SSH ", "known_hosts"),
		filepath.Join(home, ".aws. .", "config"),
		// Names only this file lists (.ssh and .aws are editapply's too, and
		// were normalised there already).
		filepath.Join(home, ".gnupg.", "pubring.kbx"),
		filepath.Join(home, ".kube ", "config"),
		filepath.Join(home, ".config", "Google-Chrome.", "Default", "Cookies"),
		filepath.Join(home, ".bash_history."),
		filepath.Join(home, ".Zsh_History "),
	}
	for _, p := range refused {
		if outsideReadRefusal(p) == "" {
			t.Errorf("outsideReadRefusal(%q) = \"\", want refused", p)
		}
	}
	// And no wider than it was: a readable file stays readable in each spelling.
	for _, p := range []string{
		home + "/Documents/report.md",
		home + `\Documents\report.md`,
		filepath.Join(home, "Documents", "history.md"),
		filepath.Join(home, "ssh", "notes.txt"), // the name without its dot
	} {
		if why := outsideReadRefusal(p); why != "" {
			t.Errorf("outsideReadRefusal(%q) = %q, want readable", p, why)
		}
	}
}

func TestOutsideReadRefusalCatalogue(t *testing.T) {
	home, _ := os.UserHomeDir()
	refused := []string{
		"/proc/self/environ", "/sys/kernel", "/dev/mem", "/run/user/1000/mochiii",
		filepath.Join(home, ".mochiii", "credentials.json"),
		filepath.Join(home, ".local", "state", "mochiii", "memory.db"),
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".config", "gh", "hosts.yml"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, "Desktop", "prod.pem"),
		filepath.Join(home, "Desktop", ".env"),
		filepath.Join(home, "code", ".git", "config"),
		// Secrets typed in the clear (FOUND 2026-10-01: all were readable).
		filepath.Join(home, ".bash_history"),
		filepath.Join(home, ".zsh_history"),
		filepath.Join(home, ".local", "share", "fish", "fish_history"),
		filepath.Join(home, ".psql_history"),
		filepath.Join(home, ".vault-token"),
		filepath.Join(home, ".config", "hub"),
	}
	for _, p := range refused {
		if outsideReadRefusal(p) == "" {
			t.Errorf("outsideReadRefusal(%q) = \"\", want refused", p)
		}
	}
	allowed := []string{
		filepath.Join(home, "Desktop"),
		filepath.Join(home, "Desktop", "notes.txt"),
		filepath.Join(home, "Documents", "report.md"),
		filepath.Join(home, "Documents", "history.md"), // a name that merely mentions history
		"/tmp/build.log",
		"/etc/hostname",
	}
	for _, p := range allowed {
		if why := outsideReadRefusal(p); why != "" {
			t.Errorf("outsideReadRefusal(%q) = %q, want readable", p, why)
		}
	}
}
