package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	known := map[string]bool{"type": true, "command": true, "args": true, "env": true, "url": true, "headers": true, "cwd": true}
	for k := range probe {
		if !known[k] {
			notes = append(notes, fmt.Sprintf("the entry's %q field is not used here and was left out", truncateForClient(k, 32)))
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&entry); err != nil {
		return entry, "", nil, fmt.Errorf("the entry has a field of the wrong kind (command a string, args a list of " +
			"strings, env an object of strings)")
	}
	return entry, name, notes, nil
}

// blockFromClientEntry is the server block and the values to keep for it.
func blockFromClientEntry(entry clientServerEntry) (srv MCPServerConfig, values map[string]string, notes []string, err error) {
	kind := strings.ToLower(strings.TrimSpace(entry.Type))
	switch {
	case kind == "http" || kind == "sse" || kind == "streamable-http" || kind == "ws":
		return srv, nil, nil, fmt.Errorf("this is a remote server (type %q); this daemon runs servers that are "+
			"programs on your machine only", kind)
	case strings.TrimSpace(entry.URL) != "":
		return srv, nil, nil, fmt.Errorf("the entry has a \"url\" but no \"type\"; a remote server needs \"type\": " +
			"\"http\" (or \"sse\"), and this daemon runs servers that are programs on your machine only")
	case kind != "" && kind != "stdio":
		return srv, nil, nil, fmt.Errorf("the entry's type %q is not one this daemon knows", truncateForClient(kind, 24))
	case strings.TrimSpace(entry.Command) == "":
		return srv, nil, nil, fmt.Errorf("the entry has no \"command\" to run")
	}
	srv = MCPServerConfig{Command: strings.TrimSpace(entry.Command), Args: entry.Args, AcknowledgedUnconfined: true}
	switch strings.TrimSpace(entry.Cwd) {
	case "":
	case ServerCwdWorkspace, ".", "${CLAUDE_PROJECT_DIR}", "${workspaceFolder}":
		srv.Cwd = ServerCwdWorkspace
	default:
		notes = append(notes, "the entry's \"cwd\" names a folder; servers here start either in a private folder "+
			"of their own or in the project, so it starts in a private folder")
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
		default:
			srv.Env = append(srv.Env, k)
			if values == nil {
				values = map[string]string{}
			}
			values[k] = v
		}
	}
	if len(entry.Headers) > 0 {
		notes = append(notes, "the entry's \"headers\" are for a remote server and were left out")
	}
	return srv, values, notes, nil
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
	srv, values, more, err := blockFromClientEntry(entry)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was added"}
	}
	resp := s.mcpStoreBlock(ctx, name, srv, values, req.AcknowledgedUnconfined, false)
	resp.Notes = append(append(notes, more...), resp.Notes...)
	return resp
}
