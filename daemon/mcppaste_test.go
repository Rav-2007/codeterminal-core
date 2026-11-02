package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"mochiii/protocol"
)

// A PASTED ENTRY, AS THE TERMINAL'S /mcp PANEL SENDS IT (2026-11-02): one
// add_json per server, first without the acknowledgement for the panel's
// review, then -- once the user typed the name -- with it.

func addJSON(name, entry string) protocol.MCPRequest {
	return protocol.MCPRequest{MCP: true, Action: protocol.MCPActionAddJSON, Name: name, JSON: json.RawMessage(entry)}
}

// No field is dropped unsaid: each is used, read as another client's name for
// the same thing, left out with a note the review shows, or refused by name.
func TestAPastedEntrysFieldsAreUsedNotedOrRefused(t *testing.T) {
	for _, c := range []struct {
		entry       string
		want        MCPServerConfig
		note, error string
	}{
		{entry: `{"transportType":"sse","url":"https://x.example/sse"}`, want: MCPServerConfig{Type: "sse", URL: "https://x.example/sse"}},
		{entry: `{"transportType":"streamableHttp","url":"https://x.example/mcp"}`, want: MCPServerConfig{Type: "http", URL: "https://x.example/mcp"}},
		{entry: `{"serverUrl":"https://x.example/mcp"}`, want: MCPServerConfig{Type: "http", URL: "https://x.example/mcp"}},
		{entry: `{"command":"npx","args":["-y","s"],"autoApprove":["read"],"timeout":60}`, want: MCPServerConfig{Command: "npx"},
			note: `"autoApprove" is not used here, so it was left out: each of its tools still asks`},
		{entry: `{"command":"npx","envFile":"${workspaceFolder}/.env"}`, error: `"envFile" field is not one Mochiii can use`},
		{entry: `{"command":"npx","disabled":true}`, error: `"disabled": true`},
		{entry: `{"type":"http","url":"https://x.example/mcp","command":"npx"}`, error: "also has \"command\""},
		{entry: `{"command":"npx","headers":{"A":"b"}}`, error: `has "headers", which are for a remote server`},
		{entry: `{"command":"npx","cwd":"/home/me/elsewhere"}`, error: `"cwd" names a folder`},
		{entry: `{"type":"sse","transportType":"stdio","command":"npx"}`, error: `disagree`},
	} {
		entry, _, notes, err := parseClientServerJSON([]byte(c.entry))
		var srv MCPServerConfig
		if err == nil {
			var more []string
			srv, _, _, more, err = blockFromClientEntry(entry)
			notes = append(notes, more...)
		}
		if c.error != "" {
			if err == nil || !strings.Contains(err.Error(), c.error) {
				t.Errorf("%s: err %v, want one saying %q", c.entry, err, c.error)
			}
			continue
		}
		if err != nil || srv.Type != c.want.Type || srv.URL != c.want.URL || srv.Command != c.want.Command {
			t.Errorf("%s: %+v, %v", c.entry, srv, err)
		}
		if c.note != "" && !strings.Contains(strings.Join(notes, "\n"), c.note) {
			t.Errorf("%s: notes %v, want one saying %q", c.entry, notes, c.note)
		}
	}
}

func TestAPlaceholderIsNotAKey(t *testing.T) {
	for _, v := range []string{"YOUR_API_KEY", "<your-token>", "your-api-key-here", "sk-...", "xxxx", "ghp_xxxxxxxx",
		"Bearer YOUR_TOKEN", "Bearer <token>", "API_KEY", "token", "REPLACE_ME", "[your key]", "", "insert-key-here"} {
		if !looksLikePlaceholder(v) {
			t.Errorf("%q was taken for a key", v)
		}
	}
	for _, v := range []string{secretSentinel, "ghp_2b8Kd9Zq4LmN7pR1sT3vW5xY", "Bearer eyJhbGciOiJIUzI1NiJ9.abc.def",
		"postgres://u:p@db/x", "AKIAIOSFODNN7EXAMPLE"} {
		if looksLikePlaceholder(v) {
			t.Errorf("%q was taken for a placeholder", v)
		}
	}
}

// A placeholder, in env or in a header, is a key the server is missing: not
// kept, said in a note that does not repeat it, and named in the preview as
// missing so the add asks for it.
func TestAPastedPlaceholderIsAMissingKey(t *testing.T) {
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	t.Setenv("PASTE_TOKEN", "")
	resp := s.mcpResult(context.Background(), addJSON("local",
		`{"command":"`+jsonEscape(buildEchoServer(t))+`","env":{"PASTE_TOKEN":"YOUR_API_KEY"}}`))
	if !resp.NeedsAcknowledgement || resp.Preview == nil || strings.Join(resp.Preview.EnvMissing, ",") != "PASTE_TOKEN" ||
		len(resp.Preview.EnvStored) != 0 {
		t.Fatalf("preview %+v", resp.Preview)
	}
	if notes := strings.Join(resp.Notes, "\n"); !strings.Contains(notes, "PASTE_TOKEN looks like a placeholder") ||
		strings.Contains(notes, "YOUR_API_KEY") {
		t.Errorf("notes %v", resp.Notes)
	}

	remote := s.mcpResult(context.Background(), addJSON("remote",
		`{"type":"http","url":"https://x.example/mcp","headers":{"Authorization":"Bearer YOUR_TOKEN"}}`))
	if remote.Preview == nil || strings.Join(remote.Preview.HeadersMissing, ",") != "Authorization" {
		t.Fatalf("preview %+v", remote.Preview)
	}
}

// The review needs every pasted server's facts at once: the exact command,
// or the URL; which keys were given (kept in the key file) and which are
// missing. A given value is named, never shown -- not in the answer, the
// server list, or the daemon's log.
func TestTheReviewPreviewNamesKeysAndShowsNoValue(t *testing.T) {
	s := mcpAdminTestServer(t)
	var logged bytes.Buffer
	s.logger = log.New(&logged, "", 0)
	keys := withKeyFile(t, s)
	t.Setenv("PASTE_MISSING", "")
	echo := buildEchoServer(t)
	req := addJSON("local", `{"command":"`+jsonEscape(echo)+`","args":["--flag","x y"],`+
		`"env":{"PASTE_GIVEN":"`+secretSentinel+`","PASTE_MISSING":""}}`)
	first := s.mcpResult(context.Background(), req)
	p := first.Preview
	if !first.NeedsAcknowledgement || p == nil {
		t.Fatalf("no preview: %+v", first)
	}
	if p.Command != echo+" --flag x y" || p.Program != echo || strings.Join(p.EnvStored, ",") != "PASTE_GIVEN" ||
		strings.Join(p.EnvMissing, ",") != "PASTE_MISSING" {
		t.Errorf("preview %+v", p)
	}
	if !strings.Contains(first.Detail, echo+" --flag x y") {
		t.Errorf("the warning does not show the exact command:\n%s", first.Detail)
	}

	req.AcknowledgedUnconfined = true
	if done := s.mcpResult(context.Background(), req); !done.Ok {
		t.Fatalf("add: %+v", done)
	}
	for where, text := range map[string]string{"the preview answer": answerJSON(t, first), "the server list": readFileString(t, s.userServersPath),
		"the daemon log": logged.String()} {
		if strings.Contains(text, secretSentinel) {
			t.Errorf("the key is in %s", where)
		}
	}
	if !strings.Contains(readFileString(t, keys), secretSentinel) {
		t.Error("the key was not kept in the key file")
	}
}

// A pasted entry never overwrites a server unasked: the name is refused, and
// with replace -- what the panel sends once the user chose to replace it -- it
// is a change, warned about and agreed to as one.
func TestAPastedEntryReplacesOnlyWhenAsked(t *testing.T) {
	s := mcpAdminTestServer(t)
	withKeyFile(t, s)
	echo := buildEchoServer(t)
	add := addJSON("dup", `{"command":"`+jsonEscape(echo)+`"}`)
	add.AcknowledgedUnconfined = true
	if r := s.mcpResult(context.Background(), add); !r.Ok {
		t.Fatalf("first add: %+v", r)
	}
	again := addJSON("dup", `{"command":"`+jsonEscape(echo)+`","args":["--second"]}`)
	if r := s.mcpResult(context.Background(), again); !strings.Contains(r.Error, "already added") {
		t.Fatalf("the same name was not refused: %+v", r)
	}
	again.Replace = true
	if r := s.mcpResult(context.Background(), again); !r.NeedsAcknowledgement || !strings.Contains(r.Detail, `Changing "dup"`) {
		t.Fatalf("replace did not warn as a change: %+v", r)
	}
	again.AcknowledgedUnconfined = true
	if r := s.mcpResult(context.Background(), again); !r.Ok {
		t.Fatalf("replace: %+v", r)
	}
	if got := storedServers(t, s)["dup"].Args; strings.Join(got, " ") != "--second" {
		t.Errorf("stored args %v", got)
	}
}

func answerJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
