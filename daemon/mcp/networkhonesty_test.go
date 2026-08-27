package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"codeterminal/protocol"
)

type allowAll struct{}

func (allowAll) PolicyFor(string, string) Policy { return PolicyAllow }

func noopHandler(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }

// THE BUG THIS TEST EXISTS TO PREVENT IS A REPEAT OF A KNOWN ONE.
//
// RegisterBuiltin used to stamp Confined = true on everything that did not
// execute code, because "first-party implies confined" held for every built-in
// there was. sandbox_exec broke that assumption by running subprocesses, and
// the comment in this package records what it cost: an approval prompt that
// told the user their call was governed by the edit-review pipeline while it
// ran shell on the host.
//
// A web tool breaks the SAME assumption in a direction the ExecutesCode fix did
// not cover. It spawns nothing and writes nothing, so every structural test
// passes it -- and the prompt would have said "anything it changes goes through
// the same review you use for edits" about a call that ships the user's words
// to a third party and pulls an attacker-controlled document into the model's
// context. Nothing is changed, so the sentence is not false. It is just not the
// answer to the question the user is asking at that moment.
func TestANetworkToolIsNeverStampedConfined(t *testing.T) {
	r := NewRegistry(allowAll{}, 10)
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "web_search", ReachesNetwork: true, ReadOnlyHint: true},
		Handler: noopHandler,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}

	spec, _, err := r.Lookup(context.Background(), "builtin__web_search")
	if err != nil {
		t.Fatalf("the tool did not register: %v", err)
	}
	if spec.Confined {
		t.Fatal("a network-reaching built-in was stamped Confined; the approval prompt would tell the user " +
			"their query is governed by the edit-review pipeline")
	}
	if !spec.ReachesNetwork {
		t.Error("ReachesNetwork was dropped during registration; the prompt has nothing honest to print")
	}
	// The lane is still first-party, and that is correct: it IS this daemon's
	// own code. Confinement and provenance are different questions, which is
	// the whole reason they are different fields.
	if spec.Lane != protocol.LaneFirstParty {
		t.Errorf("Lane = %q, want the built-in lane", spec.Lane)
	}
}

// The ordinary built-ins must keep their assertion. If the exemption were
// written too broadly, every read_file would start claiming to be unconfined
// and the prompt's one strong signal would go quiet.
func TestOrdinaryBuiltinsAreStillAssertedConfined(t *testing.T) {
	r := NewRegistry(allowAll{}, 10)
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "read_file", ReadOnlyHint: true},
		Handler: noopHandler,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	spec, _, _ := r.Lookup(context.Background(), "builtin__read_file")
	if !spec.Confined {
		t.Fatal("an ordinary built-in lost its confinement assertion")
	}
}

// A caller must not be able to register a network tool that reports itself
// confined by setting the field itself.
func TestAToolCannotClaimConfinementItDoesNotHave(t *testing.T) {
	r := NewRegistry(allowAll{}, 10)
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "web_fetch", ReachesNetwork: true, Confined: true},
		Handler: noopHandler,
	}); err != nil {
		t.Fatalf("RegisterBuiltin: %v", err)
	}
	if spec, _, _ := r.Lookup(context.Background(), "builtin__web_fetch"); spec.Confined {
		t.Fatal("a tool that reaches the network kept a self-asserted Confined:true")
	}
}
