package mcp

import (
	"context"
	"testing"
)

// Canonicalize accepts a bare tool name for the first-party lane, and for
// NOTHING ELSE. These tests are about the second half of that sentence.

func TestBareNameResolvesToABuiltin(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if err := r.RegisterBuiltin(newBuiltin("search_code")); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	got, aliased := r.Canonicalize("search_code")
	if !aliased {
		t.Fatalf("a bare built-in name was not resolved; got %q -- the whole tax this exists to remove is still being paid", got)
	}
	if got != "builtin__search_code" {
		t.Errorf("Canonicalize(\"search_code\") = %q, want %q", got, "builtin__search_code")
	}
	// And it must actually dispatch.
	if _, _, err := r.Lookup(context.Background(), got); err != nil {
		t.Errorf("the canonical name does not resolve: %v", err)
	}
}

// THE SECURITY PROPERTY. A bare name must never reach a third-party server,
// even when that server is the ONLY thing offering a tool by that name.
//
// The rejected alternative was "resolve to whichever server uniquely offers
// it", which would pass this test's setup happily. It is rejected because the
// meaning of a bare name would then depend on which servers happen to be
// installed -- PATH shadowing, with the resolution target moving whenever a
// user adds a server.
func TestBareNameNeverReachesAThirdPartyServer(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	// No builtin by this name. One external server offers it, unambiguously.
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{{Name: "run_anything"}}}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	got, aliased := r.Canonicalize("run_anything")
	if aliased {
		t.Fatalf("a bare name was resolved to %q -- a third-party server captured an unqualified name", got)
	}
	if got != "run_anything" {
		t.Errorf("an unresolvable bare name was rewritten to %q; it must be refused BY NAME, not become something else", got)
	}
	// It must be refused, not dispatched.
	if _, _, err := r.Lookup(context.Background(), got); err == nil {
		t.Error("an unqualified third-party tool name resolved; it must be refused")
	}
}

// SHADOWING. An external server offering a tool with the same name as a
// built-in must not be able to intercept the bare name.
//
// AddServer already refuses a server literally called "builtin"
// (ValidateServerName). This pins the other half: the collision on the TOOL
// name, where the external server is legitimately named.
func TestAThirdPartyServerCannotShadowABuiltinsBareName(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if err := r.RegisterBuiltin(newBuiltin("read_file")); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{{Name: "read_file"}}}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	got, aliased := r.Canonicalize("read_file")
	if !aliased || got != "builtin__read_file" {
		t.Fatalf("Canonicalize(\"read_file\") = %q (aliased=%t), want the CONFINED built-in", got, aliased)
	}

	tool, _, err := r.Lookup(context.Background(), got)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !tool.Confined {
		t.Error("a bare name resolved to an UNCONFINED tool; the alias must only ever reach the confined first-party lane")
	}
	if tool.Server != BuiltinServerName {
		t.Errorf("resolved to server %q, want %q", tool.Server, BuiltinServerName)
	}
}

// An ALREADY-QUALIFIED name is the model addressing a specific server. Never
// second-guessed, or the alias could redirect a call away from the server the
// model named.
func TestAQualifiedNameIsNeverRewritten(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if err := r.RegisterBuiltin(newBuiltin("read_file")); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if err := r.AddServer("ext", &fakeClient{name: "ext", tools: []Tool{{Name: "read_file"}}}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	for _, name := range []string{"ext__read_file", "builtin__read_file", "nosuch__read_file"} {
		got, aliased := r.Canonicalize(name)
		if aliased || got != name {
			t.Errorf("Canonicalize(%q) = %q (aliased=%t), want it untouched", name, got, aliased)
		}
	}
}

// EXACT MATCH ONLY. No case folding, no trimming, no fuzzy matching. A tool
// name is dispatched on, and this codebase has been bitten by case-insensitive
// matching before (the .GIT prune bypass).
func TestCanonicalizeIsExactMatchOnly(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if err := r.RegisterBuiltin(newBuiltin("search_code")); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	for _, name := range []string{"Search_Code", "SEARCH_CODE", " search_code", "search_code ", "searchcode", "search-code"} {
		if got, aliased := r.Canonicalize(name); aliased {
			t.Errorf("Canonicalize(%q) = %q, aliased -- only an exact match may resolve", name, got)
		}
	}
}

// A registry with no builtins at all must not resolve anything.
func TestCanonicalizeResolvesNothingWithoutBuiltins(t *testing.T) {
	r := NewRegistry(mapPolicy{}, 0)
	if got, aliased := r.Canonicalize("anything"); aliased {
		t.Errorf("Canonicalize on an empty registry resolved %q", got)
	}
}
