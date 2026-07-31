package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// fakeClient is a Lane B server without a subprocess. The whole point of the
// Client interface being three methods is that this is cheap.
type fakeClient struct {
	name     string
	tools    []Tool
	listErr  error
	called   []string
	callResp Result
	callErr  error
	closed   bool
}

func (f *fakeClient) ListTools(context.Context) ([]Tool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Tool, len(f.tools))
	for i, t := range f.tools {
		t.Server = f.name
		t.Lane = protocol.LaneThirdParty
		t.Confined = false
		out[i] = t
	}
	return out, nil
}

func (f *fakeClient) CallTool(_ context.Context, name string, _ json.RawMessage) (Result, error) {
	f.called = append(f.called, name)
	return f.callResp, f.callErr
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

// mapPolicy resolves from an explicit table. Anything unlisted is ask, which is
// the daemon config's own default.
type mapPolicy map[string]Policy

func (m mapPolicy) PolicyFor(server, tool string) Policy {
	if p, ok := m[server+"__"+tool]; ok {
		return p
	}
	return PolicyAsk
}

func newBuiltin(name string) Builtin {
	return Builtin{
		Tool: Tool{Name: name, Description: name},
		Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{Content: "ran " + name}, nil
		},
	}
}

// A registry with no policy resolver must deny everything. "Nobody said" is not
// "yes", and a registry constructed wrong should fail closed rather than open.
func TestNilPolicyResolverDeniesEverything(t *testing.T) {
	r := NewRegistry(nil, 10)
	if err := r.RegisterBuiltin(newBuiltin("read_file")); err != nil {
		t.Fatal(err)
	}

	tools, _ := r.Advertised(context.Background())
	if len(tools) != 0 {
		t.Errorf("a registry with no policy resolver advertised %d tool(s); it must deny everything", len(tools))
	}

	if _, err := r.Call(context.Background(), "builtin__read_file", nil); err == nil {
		t.Error("a registry with no policy resolver dispatched a call")
	}
}

// An invented policy value is deny. The config loader refuses these already, so
// reaching here means a resolver made one up -- and inventing a permission must
// not work.
func TestUnrecognisedPolicyIsDeny(t *testing.T) {
	r := NewRegistry(mapPolicy{"builtin__read_file": Policy("sure_why_not")}, 10)
	if err := r.RegisterBuiltin(newBuiltin("read_file")); err != nil {
		t.Fatal(err)
	}

	if tools, _ := r.Advertised(context.Background()); len(tools) != 0 {
		t.Errorf("a tool with an invented policy was advertised: %v", tools)
	}
	if _, err := r.Call(context.Background(), "builtin__read_file", nil); err == nil {
		t.Error("a tool with an invented policy was dispatched")
	}
}

// Denied tools are not merely uncallable -- they are never put on the menu.
// Phase 0 found the model reaches for a plausible-looking tool when uncertain,
// so an uncallable entry is actively harmful, not just useless.
func TestDeniedToolsAreNotAdvertised(t *testing.T) {
	r := NewRegistry(mapPolicy{
		"builtin__read_file":    PolicyAllow,
		"builtin__propose_edit": PolicyDeny,
	}, 10)
	for _, name := range []string{"read_file", "propose_edit"} {
		if err := r.RegisterBuiltin(newBuiltin(name)); err != nil {
			t.Fatal(err)
		}
	}

	tools, _ := r.Advertised(context.Background())
	var got []string
	for _, tool := range tools {
		got = append(got, tool.QualifiedName())
	}
	if !slices.Equal(got, []string{"builtin__read_file"}) {
		t.Errorf("advertised %v, want only builtin__read_file -- a denied tool must not be offered", got)
	}

	// And it still cannot be called, even if something asks directly.
	if _, err := r.Call(context.Background(), "builtin__propose_edit", nil); err == nil {
		t.Error("a denied tool was dispatched")
	}
}

// The cap is a quality control, and when it bites the confined half must
// survive: dropping built-ins in favour of unconfined external tools would be
// exactly backwards.
func TestAdvertisedCapKeepsBuiltinsAndReportsDrops(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 3)
	for _, name := range []string{"a_read", "b_list"} {
		if err := r.RegisterBuiltin(newBuiltin(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{
		{Name: "one"}, {Name: "two"}, {Name: "three"},
	}}); err != nil {
		t.Fatal(err)
	}

	tools, errs := r.Advertised(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(tools) != 3 {
		t.Fatalf("advertised %d tools, want the cap of 3", len(tools))
	}

	var got []string
	for _, tool := range tools {
		got = append(got, tool.QualifiedName())
	}
	want := []string{"builtin__a_read", "builtin__b_list", "ext__one"}
	if !slices.Equal(got, want) {
		t.Errorf("advertised %v, want %v -- built-ins are the confined, non-mutating half "+
			"and must survive the cap", got, want)
	}

	dropped := r.Dropped()
	if !slices.Equal(dropped, []string{"ext__three", "ext__two"}) &&
		!slices.Equal(dropped, []string{"ext__two", "ext__three"}) {
		t.Errorf("Dropped() = %v, want the two omitted ext tools -- silent truncation is "+
			"indistinguishable from a server that never offered them", dropped)
	}
}

// A dead server costs the user its tools, not their turn.
func TestUnavailableServerDegradesRatherThanFails(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 10)
	if err := r.RegisterBuiltin(newBuiltin("read_file")); err != nil {
		t.Fatal(err)
	}
	if err := r.AddServer("broken", &fakeClient{name: "broken", listErr: ErrServerUnavailable}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddServer("fine", &fakeClient{name: "fine", tools: []Tool{{Name: "ok"}}}); err != nil {
		t.Fatal(err)
	}

	tools, errs := r.Advertised(context.Background())
	if len(errs) != 1 || !errors.Is(errs[0], ErrServerUnavailable) {
		t.Fatalf("errs = %v, want one ErrServerUnavailable to surface as a degradation", errs)
	}

	var got []string
	for _, tool := range tools {
		got = append(got, tool.QualifiedName())
	}
	if !slices.Equal(got, []string{"builtin__read_file", "fine__ok"}) {
		t.Errorf("advertised %v; a broken server must not cost the user the working ones", got)
	}
}

// A caller cannot register a tool that lies about which lane it is in.
func TestRegisterBuiltinForcesConfinedFirstParty(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 10)
	b := newBuiltin("sneaky")
	b.Tool.Server = "pretending_to_be_someone_else"
	b.Tool.Lane = protocol.LaneThirdParty
	b.Tool.Confined = false
	if err := r.RegisterBuiltin(b); err != nil {
		t.Fatal(err)
	}

	tool, _, err := r.Lookup(context.Background(), "builtin__sneaky")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if tool.Server != BuiltinServerName || tool.Lane != protocol.LaneFirstParty || !tool.Confined {
		t.Errorf("registered builtin came back as %+v; RegisterBuiltin must force the first-party "+
			"values so a caller cannot register a tool that misreports its lane", tool)
	}
}

// The model supplies the qualified name back to us, so an unknown server is the
// shape a prompt-injection attempt takes. It must not resolve to anything.
func TestUnknownServerIsRefused(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 10)

	for _, name := range []string{"nonexistent__tool", "builtin__no_such_builtin", "garbage"} {
		if _, policy, err := r.Lookup(context.Background(), name); err == nil {
			t.Errorf("Lookup(%q) succeeded", name)
		} else if policy != PolicyDeny {
			t.Errorf("Lookup(%q) failed but returned policy %q, want %q", name, policy, PolicyDeny)
		}
		if _, err := r.Call(context.Background(), name, nil); err == nil {
			t.Errorf("Call(%q) succeeded on an unknown target", name)
		}
	}
}

func TestCallDispatchesToTheRightLane(t *testing.T) {
	ext := &fakeClient{name: "ext", tools: []Tool{{Name: "remote"}}, callResp: Result{Content: "from ext"}}
	r := NewRegistry(mapPolicy{}, 10)
	if err := r.RegisterBuiltin(newBuiltin("local")); err != nil {
		t.Fatal(err)
	}
	if err := r.AddServer("ext", ext); err != nil {
		t.Fatal(err)
	}

	res, err := r.Call(context.Background(), "builtin__local", nil)
	if err != nil || res.Content != "ran local" {
		t.Errorf("builtin call = (%+v, %v)", res, err)
	}

	res, err = r.Call(context.Background(), "ext__remote", nil)
	if err != nil || res.Content != "from ext" {
		t.Errorf("external call = (%+v, %v)", res, err)
	}
	if !slices.Equal(ext.called, []string{"remote"}) {
		t.Errorf("external server saw calls %v, want [remote] -- and note the name is UNqualified "+
			"on the wire to the server, which knows nothing of our namespacing", ext.called)
	}
}

func TestDuplicateRegistrationIsRefused(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 10)
	if err := r.RegisterBuiltin(newBuiltin("dup")); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBuiltin(newBuiltin("dup")); err == nil {
		t.Error("a builtin was registered twice; the second would silently shadow the first")
	}

	if err := r.AddServer("s", &fakeClient{name: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddServer("s", &fakeClient{name: "s"}); err == nil {
		t.Error("a server was registered twice")
	}
	if err := r.AddServer(BuiltinServerName, &fakeClient{}); err == nil {
		t.Error("a server claimed the reserved builtin name")
	}
}

func TestCloseShutsDownEveryServer(t *testing.T) {
	a := &fakeClient{name: "a"}
	b := &fakeClient{name: "b"}
	r := NewRegistry(mapPolicy{}, 10)
	for name, c := range map[string]*fakeClient{"a": a, "b": b} {
		if err := r.AddServer(name, c); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !a.closed || !b.closed {
		t.Error("Close did not shut down every server")
	}
	// Idempotent: the daemon may close a registry whose servers already died.
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// A server that returns an error result is not a transport failure: the model
// should see it and get a chance to recover.
func TestToolErrorResultsReachTheModel(t *testing.T) {
	ext := &fakeClient{
		name:     "ext",
		tools:    []Tool{{Name: "flaky"}},
		callResp: Result{Content: "file not found", IsError: true},
	}
	r := NewRegistry(mapPolicy{}, 10)
	if err := r.AddServer("ext", ext); err != nil {
		t.Fatal(err)
	}

	res, err := r.Call(context.Background(), "ext__flaky", nil)
	if err != nil {
		t.Fatalf("a tool-level error must not surface as a Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "file not found") {
		t.Errorf("result = %+v, want the tool's own error text preserved for the model", res)
	}
}

func TestAdvertisedOrderIsStableAcrossCalls(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 50)
	for i := range 8 {
		if err := r.RegisterBuiltin(newBuiltin(fmt.Sprintf("tool_%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.AddServer("z", &fakeClient{name: "z", tools: []Tool{{Name: "b"}, {Name: "a"}}}); err != nil {
		t.Fatal(err)
	}

	first, _ := r.Advertised(context.Background())
	for range 20 {
		next, _ := r.Advertised(context.Background())
		if len(next) != len(first) {
			t.Fatalf("advertised set changed size between identical calls")
		}
		for i := range first {
			if first[i].QualifiedName() != next[i].QualifiedName() {
				t.Fatalf("advertised order is unstable at %d: %q then %q. Go map iteration is "+
					"randomised, and a reshuffling tool list defeats prompt caching and makes noise "+
					"indistinguishable from a real change",
					i, first[i].QualifiedName(), next[i].QualifiedName())
			}
		}
	}
}
