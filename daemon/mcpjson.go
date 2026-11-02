package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"mochiii/protocol"
)

// ADD FROM ANOTHER CLIENT'S JSON (`mcp add-json`). A server's setup page is
// usually written for Claude Desktop, Cursor or Claude Code, and gives an
// "mcpServers" entry rather than a command:
//
//	{"command": "npx", "args": ["-y", "@x/mcp"], "env": {"X_API_KEY": "sk-..."}}
//
// It is turned into this daemon's block, with one difference that is the point:
// an env value is NOT written into the server list. A literal value becomes a
// key kept for that server in the owner-only key file (mcpsecrets.go); a
// "${VAR}" stays a template (mcpexpand.go); "${X}" for X itself is a plain
// pass-through name. Then it goes through add, warning and typed name included.

// maxAddJSONBytes bounds one entry: a server block is a few hundred bytes.
const maxAddJSONBytes = 64 << 10

type clientServerEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Cwd     string            `json:"cwd"`
	// Other clients' names for type and url: Cline's transportType, and
	// Windsurf's serverUrl.
	TransportType string `json:"transportType"`
	ServerURL     string `json:"serverUrl"`
	Disabled      *bool  `json:"disabled"`
}

// NO FIELD IS DROPPED UNSAID (2026-11-02). A pasted entry's fields are each
// used, read under another client's name for the same thing, or -- for the few
// another client uses that have no meaning here -- left out with a note the
// user is shown before agreeing. Any other field is refused by name: an
// entry that relied on it (VS Code's "envFile", say) would otherwise be added
// without what it needs, and found broken later.
var entryFieldsUsed = map[string]bool{"type": true, "command": true, "args": true, "env": true, "url": true,
	"headers": true, "cwd": true, "transportType": true, "serverUrl": true, "disabled": true}

// entryFieldsLeftOut are other clients' fields with no meaning here, and why.
var entryFieldsLeftOut = map[string]string{
	"autoApprove": "each of its tools still asks before it runs",
	"alwaysAllow": "each of its tools still asks before it runs",
	"timeout":     "Mochiii waits for a server by its own rules",
}

// parseClientServerJSON reads one entry, or a whole {"mcpServers": {...}} block
// holding exactly one. name is the entry's own name when the block gave one.
// Errors never quote the JSON: it may hold a key.
func parseClientServerJSON(raw []byte) (entry clientServerEntry, name string, notes []string, err error) {
	if len(raw) > maxAddJSONBytes {
		return entry, "", nil, fmt.Errorf("the JSON is longer than %d bytes; give one server's entry", maxAddJSONBytes)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return entry, "", nil, fmt.Errorf("that is not a JSON object; give one server's entry, such as " +
			`{"command": "npx", "args": ["-y", "some-mcp-server"]}`)
	}
	if block, ok := probe["mcpServers"]; ok {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(block, &servers); err != nil || len(servers) != 1 {
			return entry, "", nil, fmt.Errorf("the mcpServers block holds %d servers; add them one at a time", len(servers))
		}
		for n, r := range servers {
			name, raw = n, r
		}
		// A fresh map: Unmarshal into the old one would keep "mcpServers".
		probe = nil
		if err := json.Unmarshal(raw, &probe); err != nil {
			return entry, "", nil, fmt.Errorf("the server's entry is not a JSON object")
		}
	}
	for _, k := range sortedRawKeys(probe) {
		if why, leftOut := entryFieldsLeftOut[k]; leftOut {
			notes = append(notes, fmt.Sprintf("the entry's %q is not used here, so it was left out: %s", k, why))
		} else if !entryFieldsUsed[k] {
			return entry, "", nil, fmt.Errorf("the entry's %q field is not one Mochiii can use; remove it to add the server",
				truncateForClient(k, 32))
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&entry); err != nil {
		return entry, "", nil, fmt.Errorf("the entry has a field of the wrong kind (command a string, args a list of " +
			"strings, env and headers objects of strings)")
	}
	return entry, name, notes, nil
}

// A PLACEHOLDER IS NOT A KEY. Setup pages show "YOUR_API_KEY" where the key
// goes, and a pasted entry often still has it. Kept as the value, the server
// would start with it and fail at its first call; taken as missing, the add
// asks for the real one, hidden. Checked after a header's scheme ("Bearer ")
// is set aside. A value that matches is never repeated: if one ever is a
// real key, it must not be printed for having been mistaken.
var (
	placeholderWords = []string{"your", "placeholder", "changeme", "change_me", "change-me", "replace", "insert",
		"_here", "-here", " here", "redacted"}
	placeholderWhole = map[string]bool{"key": true, "apikey": true, "api_key": true, "api-key": true, "token": true,
		"secret": true, "password": true, "todo": true, "tbd": true, "null": true, "none": true, "undefined": true,
		"xxx": true, "...": true, "…": true}
	placeholderFill = regexp.MustCompile(`^[a-z]{0,8}[-_]?(x{3,}|\*{3,}|\.{3,}|…)$`)
)

func looksLikePlaceholder(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, scheme := range []string{"bearer ", "token ", "basic "} {
		v = strings.TrimSpace(strings.TrimPrefix(v, scheme))
	}
	if v == "" || placeholderWhole[v] || placeholderFill.MatchString(v) {
		return true
	}
	if (strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">")) || (strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]")) {
		return true
	}
	for _, w := range placeholderWords {
		if strings.Contains(v, w) {
			return true
		}
	}
	return false
}

// blockFromClientEntry is the server block and the values to keep for it:
// env values for a program, header values for a remote server.
func blockFromClientEntry(entry clientServerEntry) (srv MCPServerConfig, values, headerValues map[string]string, notes []string, err error) {
	kind := strings.ToLower(strings.TrimSpace(entry.Type))
	if alias := strings.ToLower(strings.TrimSpace(entry.TransportType)); alias != "" {
		if alias == "streamablehttp" {
			alias = "http"
		}
		if kind != "" && kind != alias && !(kind == "streamable-http" && alias == "http") {
			return srv, nil, nil, nil, fmt.Errorf("the entry's \"type\" and \"transportType\" disagree; keep one")
		}
		kind = alias
	}
	if entry.URL == "" {
		entry.URL = entry.ServerURL
	} else if entry.ServerURL != "" && entry.ServerURL != entry.URL {
		return srv, nil, nil, nil, fmt.Errorf("the entry's \"url\" and \"serverUrl\" disagree; keep one")
	}
	if entry.Disabled != nil {
		if *entry.Disabled {
			return srv, nil, nil, nil, fmt.Errorf("the entry is marked \"disabled\": true; remove that to add it " +
				"(a server can be paused after it is added)")
		}
		notes = append(notes, "the entry's \"disabled\": false is left out: a server here starts unpaused")
	}
	// A url and nothing else is how Cursor's and most servers' own setup pages
	// write a remote server (Figma's: {"url": "https://mcp.figma.com/mcp"}).
	// Streamable http is what a current server speaks; an older one that only
	// speaks sse refuses it on the first check, so the note says what to change.
	if kind == "" && strings.TrimSpace(entry.URL) != "" && strings.TrimSpace(entry.Command) == "" {
		kind = "http"
		notes = append(notes, "the entry gives no \"type\", so it was added as a remote http server; "+
			"if it does not answer, add it again with \"type\": \"sse\"")
	}
	switch {
	case kind == "ws":
		return srv, nil, nil, nil, fmt.Errorf("this is a WebSocket server; this daemon reaches remote servers over " +
			"http or sse only")
	case kind == "http" || kind == "sse" || kind == "streamable-http":
		if strings.TrimSpace(entry.URL) == "" {
			return srv, nil, nil, nil, fmt.Errorf("the entry is type %q but has no \"url\"", kind)
		}
		// A remote server's entry that also names a program says two things;
		// which was meant cannot be told, and dropping either is a guess.
		if len(entry.Env) > 0 || strings.TrimSpace(entry.Command) != "" || len(entry.Args) > 0 || entry.Cwd != "" {
			return srv, nil, nil, nil, fmt.Errorf("the entry is a remote server (type %q) but also has \"command\", "+
				"\"args\", \"env\" or \"cwd\", which are for a program on this machine; remove them, or the url", kind)
		}
		if kind == "streamable-http" {
			kind = "http"
		}
		srv = MCPServerConfig{Type: kind, URL: strings.TrimSpace(entry.URL), AcknowledgedUnconfined: true}
		headers := map[string]string{}
		for _, h := range sortedStringKeys(entry.Headers) {
			v := entry.Headers[h]
			if v != "" && !strings.Contains(v, "${") && looksLikePlaceholder(v) {
				notes = append(notes, fmt.Sprintf("the value given for the %s header looks like a placeholder, not a "+
					"key, so it was not kept: you are asked for it after the add", h))
				v = ""
			}
			headers[h] = v
		}
		srv.Headers, headerValues = splitHeaders(headers)
		return srv, nil, headerValues, notes, nil
	case strings.TrimSpace(entry.URL) != "":
		// Here with a "command" beside it, or "type": "stdio": which was meant
		// cannot be told, so neither is guessed.
		return srv, nil, nil, nil, fmt.Errorf("the entry has a \"url\" but is not typed as a remote server; for a " +
			"remote server set \"type\": \"http\" (or \"sse\" for an older one), for a program leave out the \"url\"")
	case kind != "" && kind != "stdio":
		return srv, nil, nil, nil, fmt.Errorf("the entry's type %q is not one this daemon knows", truncateForClient(kind, 24))
	case strings.TrimSpace(entry.Command) == "":
		return srv, nil, nil, nil, fmt.Errorf("the entry has no \"command\" to run")
	case len(entry.Headers) > 0:
		return srv, nil, nil, nil, fmt.Errorf("the entry runs a program but has \"headers\", which are for a remote " +
			"server; remove them")
	}
	srv = MCPServerConfig{Command: strings.TrimSpace(entry.Command), Args: entry.Args, AcknowledgedUnconfined: true}
	switch strings.TrimSpace(entry.Cwd) {
	case "":
	case ServerCwdWorkspace, ".", "${CLAUDE_PROJECT_DIR}", "${workspaceFolder}":
		srv.Cwd = ServerCwdWorkspace
	default:
		return srv, nil, nil, nil, fmt.Errorf("the entry's \"cwd\" names a folder; a server here starts in a " +
			"private folder of its own, or in the project with \"cwd\": \"${workspaceFolder}\" -- use that, or remove it")
	}
	for _, k := range sortedStringKeys(entry.Env) {
		v := entry.Env[k]
		switch {
		case v == "${"+k+"}":
			srv.Env = append(srv.Env, k)
		case strings.Contains(v, "${"):
			if srv.EnvMap == nil {
				srv.EnvMap = map[string]string{}
			}
			srv.EnvMap[k] = v
		case v == "":
			srv.Env = append(srv.Env, k)
		case looksLikePlaceholder(v):
			srv.Env = append(srv.Env, k)
			notes = append(notes, fmt.Sprintf("the value given for %s looks like a placeholder, not a key, so it "+
				"was not kept: you are asked for it after the add", k))
		default:
			srv.Env = append(srv.Env, k)
			if values == nil {
				values = map[string]string{}
			}
			values[k] = v
		}
	}
	return srv, values, nil, notes, nil
}

// mcpAddJSON answers MCPActionAddJSON: add's two steps, from an entry.
func (s *Server) mcpAddJSON(ctx context.Context, req protocol.MCPRequest) protocol.MCPResponse {
	entry, entryName, notes, err := parseClientServerJSON(req.JSON)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was added"}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = entryName
	}
	if name == "" {
		return protocol.MCPResponse{Error: "give the server a name; nothing was added"}
	}
	srv, values, headerValues, more, err := blockFromClientEntry(entry)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was added"}
	}
	resp := s.mcpStoreBlockWith(ctx, name, srv, values, headerValues, req.AcknowledgedUnconfined, req.Replace)
	resp.Notes = append(append(notes, more...), resp.Notes...)
	return resp
}
