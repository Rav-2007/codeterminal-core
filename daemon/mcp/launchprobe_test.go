package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// A TOOL THAT CAN START A PROGRAM MUST SAY WHEN IT WILL (register item 32).
//
// The consent prompt can only ask about a launch if something tells the loop,
// before the call runs, that this call would launch. Leaving that to whoever
// writes the next language-server tool is how the original defect happened:
// the flag existed, the prompt was built from it, and nothing connected "this
// tool CAN start gopls" to "this call WILL". So registration refuses the
// combination that would silently reintroduce it.
//
// Neuter check: delete either half of the check in RegisterBuiltin and the
// matching case below registers cleanly.
func TestALaunchCapableBuiltinMustDeclareItsLaunchProbe(t *testing.T) {
	probe := func(json.RawMessage) LaunchPlan { return LaunchPlan{} }

	for name, tc := range map[string]struct {
		b       Builtin
		wantErr string
	}{
		"launch-capable, no probe": {
			b:       Builtin{Tool: Tool{Name: "query_x", LaunchesSubprocess: true}, Handler: noopHandler},
			wantErr: "no Launch probe",
		},
		"a probe on a tool that declares no launch": {
			b:       Builtin{Tool: Tool{Name: "read_x"}, Handler: noopHandler, Launch: probe},
			wantErr: "does not declare LaunchesSubprocess",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := NewRegistry(allowAll{}, 10).RegisterBuiltin(tc.b)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RegisterBuiltin = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}

	// And the honest pairing registers, or no language-server tool could exist.
	r := NewRegistry(allowAll{}, 10)
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "query_y", LaunchesSubprocess: true},
		Handler: noopHandler,
		Launch:  probe,
	}); err != nil {
		t.Fatalf("a launch-capable tool WITH its probe was refused: %v", err)
	}
}

// Registry.LaunchPlan answers only for built-ins, and asks the tool's own probe.
// A Lane B server was started when it connected; what it does afterwards is
// stated to the user as "unconfined", not probed.
func TestLaunchPlanComesFromTheBuiltinsOwnProbe(t *testing.T) {
	r := NewRegistry(allowAll{}, 10)
	want := LaunchPlan{Needed: true, Program: "gopls", Key: "go"}
	var sawArgs string
	if err := r.RegisterBuiltin(Builtin{
		Tool:    Tool{Name: "query_z", LaunchesSubprocess: true},
		Handler: noopHandler,
		Launch: func(args json.RawMessage) LaunchPlan {
			sawArgs = string(args)
			return want
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBuiltin(Builtin{Tool: Tool{Name: "read_z"}, Handler: noopHandler}); err != nil {
		t.Fatal(err)
	}

	if got := r.LaunchPlan("builtin__query_z", json.RawMessage(`{"path":"a.go"}`)); got != want {
		t.Errorf("LaunchPlan = %+v, want the probe's %+v", got, want)
	}
	if sawArgs != `{"path":"a.go"}` {
		t.Errorf("the probe was handed %q, not the call's own arguments", sawArgs)
	}
	for _, q := range []string{"builtin__read_z", "builtin__no_such_tool", "someserver__query_z", "not qualified"} {
		if got := r.LaunchPlan(q, nil); got != (LaunchPlan{}) {
			t.Errorf("LaunchPlan(%q) = %+v, want the zero plan", q, got)
		}
	}
}
