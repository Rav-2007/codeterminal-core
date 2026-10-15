package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// FOUND 2026-10-15, live, with the Supabase MCP server connected and a real
// account holding projects. Asked whether a project existed, the agent said
// "there is no active project" and then "no project ID in the daemon logs or
// config" -- having called no tool. Three causes, each pinned below:
//
//  1. The built-ins took 22 of the menu's 24 places, so two Supabase tools
//     were offered (query_logs, search_docs) and list_projects was not.
//  2. The setup section listed only those two, so the model told the user
//     Supabase had two tools, and had no reason to call find_tools.
//  3. A failed or empty third-party result looked like any other, so "I could
//     not see it" was easy to turn into "it does not exist".

const testRef = "abcdefghijklmnopqrst" // twenty lower-case letters: a valid ref

// fakeSupabase is a third-party server shaped like Supabase's: many tools,
// list_projects among them, answering calls from a table and recording them.
type fakeSupabase struct {
	mu      sync.Mutex
	answers map[string]func(args json.RawMessage) (mcp.Result, error)
	calls   []string
	args    []string
}

func (f *fakeSupabase) ListTools(context.Context) ([]mcp.Tool, error) {
	names := []string{
		"apply_migration", "confirm_cost", "create_branch", "create_project", "delete_branch",
		"deploy_edge_function", "execute_sql", "generate_typescript_types", "get_advisors", "get_cost",
		"get_edge_function", "get_logs", "get_organization", "get_project", "get_project_url",
		"get_publishable_keys", "list_branches", "list_edge_functions", "list_extensions", "list_migrations",
		"list_organizations", "list_projects", "list_tables", "merge_branch", "pause_project",
		"query_logs", "rebase_branch", "reset_branch", "restore_project", "search_docs",
	}
	tools := make([]mcp.Tool, len(names))
	for i, n := range names {
		desc := "Supabase " + strings.ReplaceAll(n, "_", " ") + "."
		if n == "list_projects" {
			desc = "Lists all Supabase projects for the user."
		}
		tools[i] = mcp.Tool{Server: "supabase", Name: n, Description: desc,
			Schema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
			Lane:   protocol.LaneThirdParty}
	}
	return tools, nil
}

func (f *fakeSupabase) CallTool(_ context.Context, name string, args json.RawMessage) (mcp.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.args = append(f.args, string(args))
	answer := f.answers[name]
	f.mu.Unlock()
	if answer == nil {
		return mcp.Result{IsError: true, Content: "not scripted"}, nil
	}
	return answer(args)
}

func (f *fakeSupabase) Close() error { return nil }

func (f *fakeSupabase) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// supabaseServer is a loop server with a "supabase" server configured --
// acknowledged, list_projects and get_project allowed unless policies says
// otherwise -- and a registry with fake connected under that name.
func supabaseServer(t *testing.T, base string, maxTools int, fake *fakeSupabase, policies map[string]string) (*Server, *mcp.Registry) {
	t.Helper()
	if policies == nil {
		policies = map[string]string{"list_projects": PolicyAllow, "get_project": PolicyAllow}
	}
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Budget:  MCPBudgetConfig{MaxAdvertisedTools: maxTools},
		Servers: map[string]MCPServerConfig{"supabase": {
			Command: "npx", Args: []string{"-y", "@supabase/mcp-server-supabase@0.13.0"},
			AcknowledgedUnconfined: true, Tools: policies,
		}},
	})
	registry := s.builtinRegistry(s.cfg, s.logger, &proposalSink{}, "")
	if err := registry.AddServer("supabase", fake); err != nil {
		t.Fatal(err)
	}
	return s, registry
}

func runWithRegistry(t *testing.T, s *Server, registry *mcp.Registry, prompt string) (agentResult, []protocol.ToolActivity) {
	t.Helper()
	var activity []protocol.ToolActivity
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: prompt}}, providerRouting{}, nil,
		func(string) error { return nil },
		func(a protocol.ToolActivity) { activity = append(activity, a) },
		nil, nil, func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return res, activity
}

func builtinCount(t *testing.T, s *Server) int {
	t.Helper()
	return len(s.builtinTools(&proposalSink{}, ""))
}

// 2 + 3: list_projects is not on the menu, find_tools brings it, it runs, and
// the real project refs it returned reach the model.
func TestListProjectsIsReachedThroughFindToolsAndItsRefsReachTheModel(t *testing.T) {
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", findToolsQualified, `{"query":"list projects"}`),
		toolCallSSE("c2", "supabase__list_projects", `{}`),
		textSSE("You have one project."),
	)
	fake := &fakeSupabase{answers: map[string]func(json.RawMessage) (mcp.Result, error){
		"list_projects": func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: `[{"id":"` + testRef + `","name":"shop","organization_id":"org1","status":"ACTIVE_HEALTHY"}]`}, nil
		},
	}}
	s, _ := supabaseServer(t, base, 0, fake, nil)
	s.cfg.MCP.Budget.MaxAdvertisedTools = builtinCount(t, s) // no room for any third-party tool
	registry := s.builtinRegistry(s.cfg, s.logger, &proposalSink{}, "")
	if err := registry.AddServer("supabase", fake); err != nil {
		t.Fatal(err)
	}

	runWithRegistry(t, s, registry, "which project is active?")
	if len(*bodies) != 3 {
		t.Fatalf("made %d requests, want 3", len(*bodies))
	}
	if strings.Contains(string((*bodies)[0]), `"name":"supabase__list_projects"`) {
		t.Fatal("list_projects was on the first menu; this test needs it left off")
	}
	if !strings.Contains(string((*bodies)[1]), `"name":"supabase__list_projects"`) {
		t.Error("find_tools did not put list_projects on the next menu")
	}
	if got := fake.called(); len(got) != 1 || got[0] != "list_projects" {
		t.Errorf("the server was called %v, want [list_projects]", got)
	}
	if !strings.Contains(string((*bodies)[2]), testRef) {
		t.Error("the project ref list_projects returned did not reach the model")
	}
}

// 1 (the menu half): naming the server reserves its best tools a place even
// when the built-ins would fill the menu.
func TestNamingTheServerPutsItsBestToolsOnTheMenu(t *testing.T) {
	fake := &fakeSupabase{}
	s, _ := supabaseServer(t, "http://unused", 0, fake, nil)
	n := builtinCount(t, s)
	s.cfg.MCP.Budget.MaxAdvertisedTools = n + 2 // the live shape: two places left over
	registry := s.builtinRegistry(s.cfg, s.logger, &proposalSink{}, "")
	if err := registry.AddServer("supabase", fake); err != nil {
		t.Fatal(err)
	}

	menu, _ := registry.AdvertisedFor(context.Background(), "which supabase project is active?")
	var supa []string
	for _, tool := range menu {
		if tool.Server == "supabase" {
			supa = append(supa, tool.Name)
		}
	}
	if len(supa) <= 2 {
		t.Errorf("naming supabase still left it %d place(s): %v", len(supa), supa)
	}
	if !contains(supa, "list_projects") {
		t.Errorf("list_projects is not among the reserved places: %v", supa)
	}
	if len(menu) != n+2 {
		t.Errorf("the menu has %d tools, want the cap %d", len(menu), n+2)
	}
	// Not naming it changes nothing: the built-ins keep their places.
	menu, _ = registry.AdvertisedFor(context.Background(), "which project is active?")
	builtins := 0
	for _, tool := range menu {
		if tool.Server == mcp.BuiltinServerName {
			builtins++
		}
	}
	if builtins != n {
		t.Errorf("an unnamed request displaced built-ins: %d of %d kept", builtins, n)
	}
}

// 1 (the model half): the setup section names every tool the server has, says
// which are findable, and carries the evidence rule.
func TestTheSetupListsEveryToolTheServerHas(t *testing.T) {
	fake := &fakeSupabase{}
	s, _ := supabaseServer(t, "http://unused", 0, fake, nil)
	s.cfg.MCP.Budget.MaxAdvertisedTools = builtinCount(t, s) + 2
	registry := s.builtinRegistry(s.cfg, s.logger, &proposalSink{}, "")
	if err := registry.AddServer("supabase", fake); err != nil {
		t.Fatal(err)
	}
	msgs := s.withAgentSetup(context.Background(), registry, "", "m",
		[]chatMessage{{Role: "system", Content: "base"}, {Role: "user", Content: "have you connected to mcp?"}})
	got := msgs[0].Content
	for _, want := range []string{"supabase: running, with 30 tools", "not on your list until you call find_tools",
		"list_projects", "never evidence that the thing is absent", "never pick one of several yourself"} {
		if !strings.Contains(got, want) {
			t.Errorf("the setup section does not say %q:\n%s", want, got)
		}
	}
}

// 6 + 7: a failure, an empty success and a real answer reach the model as
// three different things -- and a failure is never phrased as an absence.
func TestFailedEmptyAndFoundResultsAreToldApart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(json.RawMessage) (mcp.Result, error)
		want   string
		forbid []string
	}{
		{"auth failure", func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{IsError: true, Content: "Unauthorized. Please provide a valid access token"}, nil
		}, "this call FAILED", []string{"returned nothing"}},
		{"permission denied", func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{IsError: true, Content: "permission denied for this organization"}, nil
		}, "this call FAILED", []string{"returned nothing"}},
		{"empty success", func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: ""}, nil
		}, "succeeded and returned nothing", []string{"FAILED"}},
		{"projects found", func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: `[{"id":"` + testRef + `"}]`}, nil
		}, testRef, []string{"FAILED", "returned nothing"}},
		{"server unreachable", func(json.RawMessage) (mcp.Result, error) {
			return mcp.Result{}, errors.Join(mcp.ErrServerUnavailable, errors.New("EOF"))
		}, "the tool's server is unavailable", []string{"returned nothing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _, bodies := agentUpstream(t,
				toolCallSSE("c1", "supabase__list_projects", `{}`),
				textSSE("done"),
			)
			fake := &fakeSupabase{answers: map[string]func(json.RawMessage) (mcp.Result, error){"list_projects": tc.answer}}
			s, registry := supabaseServer(t, base, 0, fake, nil)
			runWithRegistry(t, s, registry, "list my supabase projects")
			if len(*bodies) < 2 {
				t.Fatalf("made %d requests", len(*bodies))
			}
			second := string((*bodies)[1])
			if !strings.Contains(second, tc.want) {
				t.Errorf("the model was not told %q", tc.want)
			}
			for _, f := range tc.forbid {
				if strings.Contains(second, f) {
					t.Errorf("the model was told %q for a %s", f, tc.name)
				}
			}
		})
	}
}

// 8: the ref the user chose goes to the next tool exactly as the model sent it.
func TestTheChosenRefReachesTheNextTool(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "supabase__get_project", `{"id":"`+testRef+`"}`),
		textSSE("done"),
	)
	fake := &fakeSupabase{answers: map[string]func(json.RawMessage) (mcp.Result, error){
		"get_project": func(json.RawMessage) (mcp.Result, error) { return mcp.Result{Content: `{"status":"ACTIVE"}`}, nil },
	}}
	s, registry := supabaseServer(t, base, 0, fake, nil)
	runWithRegistry(t, s, registry, "is supabase project "+testRef+" healthy?")
	if len(fake.args) != 1 || fake.args[0] != `{"id":"`+testRef+`"}` {
		t.Errorf("get_project received %v", fake.args)
	}
}

// 10: a tool found through find_tools asks like any other. With nobody to ask,
// it is refused and never reaches the server.
func TestAFoundToolStillNeedsApproval(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", findToolsQualified, `{"query":"list projects"}`),
		toolCallSSE("c2", "supabase__list_projects", `{}`),
		textSSE("done"),
	)
	fake := &fakeSupabase{answers: map[string]func(json.RawMessage) (mcp.Result, error){
		"list_projects": func(json.RawMessage) (mcp.Result, error) { return mcp.Result{Content: "[]"}, nil },
	}}
	s, _ := supabaseServer(t, base, 0, fake, map[string]string{}) // every tool: ask
	s.cfg.MCP.Budget.MaxAdvertisedTools = builtinCount(t, s)
	registry := s.builtinRegistry(s.cfg, s.logger, &proposalSink{}, "")
	if err := registry.AddServer("supabase", fake); err != nil {
		t.Fatal(err)
	}
	_, activity := runWithRegistry(t, s, registry, "which project?")
	if got := fake.called(); len(got) != 0 {
		t.Errorf("an ask-policy tool ran with nobody to ask: %v", got)
	}
	denied := false
	for _, a := range activity {
		if a.Tool == "list_projects" && a.Phase == protocol.ToolPhaseDenied {
			denied = true
		}
	}
	if !denied {
		t.Errorf("list_projects was not reported as refused: %+v", activity)
	}
}

// 1, 5, 9: configured refs are stated, a project-scoped server is said to be
// scoped, an invalid ref is refused, and nothing else from the arguments --
// which can hold an --access-token -- is repeated.
func TestProjectHintsStateOnlyValidConfiguredRefs(t *testing.T) {
	server := func(args ...string) protocol.MCPServerInfo {
		return protocol.MCPServerInfo{Name: "supabase", Program: "npx", Args: args}
	}
	ws := t.TempDir()

	got := strings.Join(projectHints(ws, []protocol.MCPServerInfo{
		server("-y", "@supabase/mcp-server-supabase", "--project-ref", testRef, "--access-token", "sbp_secret123")}), "\n")
	if !strings.Contains(got, "--project-ref "+testRef) || !strings.Contains(got, "cannot list the account's other projects") {
		t.Errorf("a scoped server was not described as scoped: %q", got)
	}
	if strings.Contains(got, "sbp_secret") {
		t.Errorf("the hint repeated the access token: %q", got)
	}
	if got := strings.Join(projectHints(ws, []protocol.MCPServerInfo{server("--project-ref=" + testRef)}), ""); !strings.Contains(got, testRef) {
		t.Errorf("--project-ref=<ref> was not read: %q", got)
	}
	for _, bad := range []string{"ABCDEFGHIJKLMNOPQRST", "short", "abcdefghijklmnopqrst; rm -rf /", "sbp_0123456789abcdef0123"} {
		got := strings.Join(projectHints(ws, []protocol.MCPServerInfo{server("--project-ref", bad)}), "")
		if strings.Contains(got, bad) || !strings.Contains(got, "not a valid project reference") {
			t.Errorf("invalid ref %q: %q", bad, got)
		}
	}
	if got := projectHints(ws, []protocol.MCPServerInfo{server("-y", "@supabase/mcp-server-supabase")}); len(got) != 0 {
		t.Errorf("an unscoped server with no linked folder produced hints: %v", got)
	}

	// The folder `supabase link` linked.
	temp := filepath.Join(ws, "supabase", ".temp")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	refFile := filepath.Join(temp, "project-ref")
	if err := os.WriteFile(refFile, []byte(testRef+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(projectHints(ws, []protocol.MCPServerInfo{server()}), ""); !strings.Contains(got, "linked to Supabase project "+testRef) {
		t.Errorf("the linked project was not stated: %q", got)
	}
	// No Supabase server configured: the linked folder is not mentioned.
	if got := projectHints(ws, []protocol.MCPServerInfo{{Name: "github", Program: "docker"}}); len(got) != 0 {
		t.Errorf("a linked ref was stated with no Supabase server: %v", got)
	}
	// An invalid or oversized file, or a link, is ignored.
	for _, content := range []string{"not-a-ref\n", strings.Repeat("a", 100)} {
		if err := os.WriteFile(refFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := projectHints(ws, []protocol.MCPServerInfo{server()}); len(got) != 0 {
			t.Errorf("file %q produced %v", content, got)
		}
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(testRef), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(refFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, refFile); err == nil {
		if got := projectHints(ws, []protocol.MCPServerInfo{server()}); len(got) != 0 {
			t.Errorf("a symlinked project-ref was followed: %v", got)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
