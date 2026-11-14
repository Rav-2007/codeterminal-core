package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// rawMCP is the smallest streamable-HTTP MCP server that answers with plain
// application/json, so a test controls every byte of a response -- which the
// SDK's own server never lets it do. It is stateless on the wire (it issues no
// session id) but models one thing a real server does: ending a session.
type rawMCP struct {
	// pad is inserted just inside the opening brace of every response: JSON
	// allows whitespace there, so it grows a message without changing it.
	pad string
	// toolsJSON is the array tools/list answers with ("" means none).
	toolsJSON string
	// listFn, when set, scripts tools/list: it is given the request's cursor and
	// the 1-based count of tools/list requests so far, and returns the result
	// object -- or, if rpcErr is not empty, a JSON-RPC error message instead.
	listFn func(cursor string, n int) (result, rpcErr string)

	// expired makes every tools/* request answer 404 ("session not found"),
	// as a server does for a session it has ended, until a client sends a new
	// initialize -- which clears it, as opening a session does.
	expired atomic.Bool

	listHits atomic.Int32 // tools/list requests
	hits     atomic.Int32 // every POST
	initHits atomic.Int32 // initialize requests: how many sessions were opened
}

func (m *rawMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
			Cursor          string `json:"cursor"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	m.hits.Add(1)
	if len(req.ID) == 0 { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "initialize" && m.expired.Load() {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	var result string
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "initialize":
		m.initHits.Add(1)
		m.expired.Store(false)
		result = `{"protocolVersion":"` + req.Params.ProtocolVersion +
			`","capabilities":{"tools":{}},"serverInfo":{"name":"raw","version":"v0"}}`
	case "tools/list":
		if m.listFn != nil {
			res, rpcErr := m.listFn(req.Params.Cursor, int(m.listHits.Add(1)))
			if rpcErr != "" {
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(req.ID)+
					`,"error":{"code":-32603,"message":`+strconv.Quote(rpcErr)+`}}`)
				return
			}
			result = res
			break
		}
		result = `{"tools":` + orDefault(m.toolsJSON, "[]") + `}`
	case "tools/call":
		result = `{"content":[{"type":"text","text":"ok"}]}`
	default:
		result = `{}`
	}
	_, _ = io.WriteString(w, `{`+m.pad+`"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":`+result+`}`)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
