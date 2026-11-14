package main

import (
	"errors"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// What a server's row says after its tools were listed. A list that stops
// short (mcp.ErrToolListIncomplete) is a running server whose tools are in use,
// with the shortfall in the field a client already shows -- not "tools failed",
// which would hide tools the model really has.
func TestListedOutcomesBecomeServerStates(t *testing.T) {
	var b mcpStateBook
	for _, name := range []string{"ok", "short", "broken"} {
		b.started(name, nil)
	}
	b.listed(map[string]mcp.ListOutcome{
		"ok":     {Tools: 3},
		"short":  {Tools: 20, Err: errors.New("mcp tool list incomplete: more than 20 pages")},
		"broken": {Err: errors.New("listing failed")},
	})

	if got := b.get("ok"); got.state != protocol.MCPStateRunning || got.tools != 3 || got.toolsErr != "" {
		t.Errorf("complete list: %+v", got)
	}
	got := b.get("short")
	if got.state != protocol.MCPStateRunning || got.tools != 20 || !got.counted {
		t.Errorf("an incomplete list is a running server with its tools counted: %+v", got)
	}
	if got.toolsErr == "" {
		t.Error("an incomplete list must say what is missing")
	}
	if got := b.get("broken"); got.state != protocol.MCPStateToolsFailed || got.tools != 0 {
		t.Errorf("a failed listing: %+v", got)
	}
}
