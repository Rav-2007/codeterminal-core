package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"mochiii/protocol"
)

// supabaseLike is a server shaped like the one that exposed the bug: 29 tools,
// most of them about projects, with list_projects sorting late.
func supabaseLike() []Tool {
	names := []string{
		"apply_migration", "confirm_cost", "create_branch", "create_project", "delete_branch",
		"deploy_edge_function", "execute_sql", "generate_typescript_types", "get_advisors", "get_cost",
		"get_edge_function", "get_logs", "get_organization", "get_project", "get_project_url",
		"get_publishable_keys", "list_branches", "list_edge_functions", "list_extensions", "list_migrations",
		"list_organizations", "list_projects", "list_tables", "merge_branch", "pause_project",
		"query_logs", "rebase_branch", "reset_branch", "restore_project",
	}
	tools := make([]Tool, len(names))
	for i, n := range names {
		tools[i] = Tool{Server: "supabase", Name: n, Description: "Supabase operation " + n + " for a project_id."}
	}
	tools[21].Description = "Lists all Supabase projects for the user."
	return tools
}

// THE MEASURED CASE. With a menu of 11 third-party places (24 minus the
// built-ins), alphabetical order cut list_projects; ranking by the request
// keeps it.
func TestTheRequestDecidesWhichToolsSurviveTheCap(t *testing.T) {
	tools := supabaseLike()
	SortTools(tools)
	RankByRelevance(tools, "which project is actinve tin the supabase")
	kept := map[string]bool{}
	for _, tool := range tools[:11] {
		kept[tool.Name] = true
	}
	if !kept["list_projects"] {
		t.Errorf("list_projects did not survive an 11-place cut for a question about projects: %v", toolNames(tools[:11]))
	}
	if kept["list_edge_functions"] {
		t.Errorf("an edge-function tool outranked project tools for a project question: %v", toolNames(tools[:11]))
	}
}

// An empty request, or one that matches nothing, leaves the order alone: the
// menu stays stable, which is what a provider's prompt cache keys on.
func TestRankingWithNothingToGoOnKeepsTheOrder(t *testing.T) {
	for _, q := range []string{"", "the a of", "zzzz qqqq"} {
		tools := supabaseLike()
		SortTools(tools)
		before := toolNames(tools)
		RankByRelevance(tools, q)
		if fmt.Sprint(before) != fmt.Sprint(toolNames(tools)) {
			t.Errorf("query %q reordered a menu it says nothing about", q)
		}
	}
}

func TestTokensMeetAcrossNamingStyles(t *testing.T) {
	want := []string{"list", "project"}
	for _, in := range []string{"list_projects", "listProjects", "List projects"} {
		if got := relevanceTokens(in); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("relevanceTokens(%q) = %v, want %v", in, got, want)
		}
	}
	if got := stem("branches"); got != "branch" {
		t.Errorf("stem(branches) = %q", got)
	}
	if got := stem("policies"); got != "policy" {
		t.Errorf("stem(policies) = %q", got)
	}
}

func TestSearchDropsWhatMatchesNothingAndHonoursTheLimit(t *testing.T) {
	tools := supabaseLike()
	if got := SearchTools(tools, "kubernetes pods", 5); len(got) != 0 {
		t.Errorf("a query matching nothing found %v", toolNames(got))
	}
	got := SearchTools(tools, "list projects", 3)
	if len(got) != 3 || got[0].Name != "list_projects" {
		t.Errorf("SearchTools(list projects, 3) = %v, want list_projects first of 3", toolNames(got))
	}
	if SearchTools(tools, "projects", 0) != nil {
		t.Error("a limit of 0 returned tools")
	}
}

// The registry keeps what the cap dropped whole, so a lookup can offer it, and
// never keeps a denied tool there: find_tools cannot reach what config forbids.
func TestTheRegistryKeepsDroppedToolsWholeAndNeverDeniedOnes(t *testing.T) {
	r := NewRegistry(denyOne{"supabase", "delete_branch"}, 5)
	if err := r.AddServer("supabase", listOnly{tools: supabaseLike()}); err != nil {
		t.Fatal(err)
	}
	menu, errs := r.AdvertisedFor(context.Background(), "list my projects")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(menu) != 5 {
		t.Fatalf("menu has %d tools, want the cap of 5", len(menu))
	}
	if menu[0].Name != "list_projects" {
		t.Errorf("the best match is not first on the menu: %v", toolNames(menu))
	}
	dropped := r.DroppedTools()
	if len(dropped) != 29-1-5 || len(dropped) != len(r.Dropped()) {
		t.Errorf("dropped %d tools (names: %d), want %d", len(dropped), len(r.Dropped()), 29-1-5)
	}
	for _, d := range dropped {
		if d.Name == "delete_branch" {
			t.Error("a denied tool was kept for lookup")
		}
		if len(d.Schema) == 0 {
			t.Errorf("dropped tool %s lost its schema", d.Name)
		}
	}
}

func toolNames(tools []Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

type denyOne struct{ server, tool string }

func (d denyOne) PolicyFor(server, tool string) Policy {
	if server == d.server && tool == d.tool {
		return PolicyDeny
	}
	return PolicyAsk
}

// listOnly is a Client that lists tools and runs none.
type listOnly struct{ tools []Tool }

func (l listOnly) ListTools(context.Context) ([]Tool, error) {
	out := make([]Tool, len(l.tools))
	for i, t := range l.tools {
		t.Schema = json.RawMessage(`{"type":"object"}`)
		t.Lane = protocol.LaneThirdParty
		out[i] = t
	}
	return out, nil
}

func (listOnly) CallTool(context.Context, string, json.RawMessage) (Result, error) {
	return Result{}, fmt.Errorf("listOnly runs nothing")
}

func (listOnly) Close() error { return nil }

func TestAServerIsNamedOnlyByItsWholeName(t *testing.T) {
	servers := []string{"supabase", "brave-search", "github", "tools"}
	got := serversNamedIn("Is SUPABASE connected? search github", servers)
	if !got["supabase"] || !got["github"] || got["brave-search"] || got["tools"] {
		t.Errorf("serversNamedIn = %v", got)
	}
	if !serversNamedIn("use brave search", servers)["brave-search"] {
		t.Error("a two-word server name was not matched by both words")
	}
}

// The reserve: a named server keeps up to namedServerReserve (and at most a
// quarter of the menu) places, taken from the least relevant built-ins; an
// unnamed one keeps the built-ins whole; and nothing is lost -- every tool is
// either on the menu or after it.
func TestANamedServerKeepsAFewPlacesAndNothingIsLost(t *testing.T) {
	var builtins []Tool
	for _, n := range []string{"grep", "list_directory", "move_path", "read_file", "run_command", "search_code",
		"web_fetch", "web_search", "delete_path", "propose_edit"} {
		builtins = append(builtins, Tool{Server: BuiltinServerName, Name: n})
	}
	SortTools(builtins)
	external := supabaseLike()
	SortTools(external)
	query := "which supabase project is active"
	RankByRelevance(external, query)

	const cap = 12
	out := withNamedServerReserve(builtins, external, query, []string{"supabase"}, cap)
	if len(out) != len(builtins)+len(external) {
		t.Fatalf("%d tools in, %d out", len(builtins)+len(external), len(out))
	}
	supa := 0
	for _, tool := range out[:cap] {
		if tool.Server == "supabase" {
			supa++
		}
	}
	if supa != cap/4 {
		t.Errorf("the named server kept %d places on a menu of %d, want %d", supa, cap, cap/4)
	}

	out = withNamedServerReserve(builtins, external, "which project is active", []string{"supabase"}, cap)
	for i, tool := range out[:len(builtins)] {
		if tool.Server != BuiltinServerName {
			t.Fatalf("an unnamed request put %s at %d, ahead of a built-in", tool.QualifiedName(), i)
		}
	}

	// Built-ins that leave room already: the named server's tools come first
	// among the third-party ones, and no built-in moves.
	out = withNamedServerReserve(builtins[:2], external, query, []string{"supabase", "other"}, cap)
	if out[0].Server != BuiltinServerName || out[1].Server != BuiltinServerName || out[2].Server != "supabase" {
		t.Errorf("with room to spare the order changed: %v", toolNames(out[:3]))
	}
}
