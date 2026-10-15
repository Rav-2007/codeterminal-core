package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A REMOTE SERVER, AS THIS DAEMON KEEPS ONE (mcp/remoteclient.go reaches it).
//
// Its block holds the URL and each header's NAME with either a ${VAR} template
// or "" -- "" meaning the value is a key, kept for that server in the owner-only
// key file (mcpsecrets.go). So a token never lands in mcp-servers.json, which is
// shown, copied and pasted into bug reports.

// remoteHeaders is the headers srv is sent, filled in, and the names of those
// with no value: a "" header with nothing stored. A template's own missing
// ${VAR}s are reported by templateMissing.
func (s *Server) remoteHeaders(name string, srv MCPServerConfig) (headers map[string]string, missing []string) {
	headers = map[string]string{}
	lookup := s.templateLookup(name)
	for _, h := range sortedStringKeys(srv.Headers) {
		tmpl := srv.Headers[h]
		if tmpl == "" {
			if v, ok := s.mcpSecrets.header(name, h); ok {
				headers[h] = v
			} else {
				missing = append(missing, h)
			}
			continue
		}
		v, _ := expandTemplate(tmpl, lookup)
		if strings.TrimSpace(v) != "" {
			headers[h] = v
		}
	}
	return headers, missing
}

// connectRemote reaches one remote server for a turn, a check or a list.
func (s *Server) connectRemote(ctx context.Context, name string, srv MCPServerConfig, cfg *Config, logger *log.Logger) (*mcp.RemoteClient, error) {
	headers, _ := s.remoteHeaders(name, srv)
	return mcp.ConnectRemote(ctx, mcp.RemoteConfig{
		Name: name, URL: srv.URL, Transport: srv.Type, Headers: headers,
		OAuth:           s.oauthHandlerFor(name, srv),
		Logf:            logger.Printf,
		MaxMessageBytes: cfg.MCP.Budget.resolvedMaxMessageBytes(),
		ConnectTimeout:  srv.connectTimeout(cfg.MCP.Budget),
	})
}

// shownURL is a URL for a person: its query values hidden, since a careless
// endpoint carries a key in its query string.
func shownURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			q.Set(k, "…")
		}
		u.RawQuery = q.Encode()
	}
	u.User = nil
	return u.String()
}

// remoteHost is the host a remote server's URL names.
func remoteHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return raw
}

// remoteWarning is what a user agrees to when they add a remote server.
func remoteWarning(lead string, srv MCPServerConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s will connect this daemon to:\n\n    %s (%s)\n\n", lead, shownURL(srv.URL), strings.ToLower(srv.Type))
	fmt.Fprintf(&b, "Every tool call you approve SENDS its arguments to %s. Mochiii cannot see or limit what that "+
		"service does with them, and what it answers is text it wrote, shown to the model as such. "+
		"Each of its tools asks before it runs, and every call is in the audit log.", remoteHost(srv.URL))
	if len(srv.Headers) > 0 {
		var parts []string
		for _, h := range sortedStringKeys(srv.Headers) {
			switch v := srv.Headers[h]; {
			case v == "":
				parts = append(parts, h+" (a key kept for this server)")
			default:
				parts = append(parts, h+" = "+v)
			}
		}
		fmt.Fprintf(&b, "\n\nEvery request carries these headers: %s.", strings.Join(parts, ", "))
	}
	b.WriteString("\n\nOnly add a server whose operator you trust with what you will send it.")
	return b.String()
}

// mcpSetHeader is set_env for a remote server's header: its value kept as a key.
func (s *Server) mcpSetHeader(req protocol.MCPRequest) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	server, header := strings.TrimSpace(req.Name), strings.TrimSpace(req.HeaderName)
	srv, ok := cfg.MCP.Servers[server]
	if !ok || !srv.remote() {
		return protocol.MCPResponse{Error: fmt.Sprintf("%s is not a remote server; a header is for a server reached at a URL", server)}
	}
	if tmpl, sends := srv.Headers[header]; !sends || tmpl != "" {
		return protocol.MCPResponse{Error: fmt.Sprintf("%s does not keep a value for its %s header; change the server to send it", server, header)}
	}
	value := strings.TrimSpace(req.EnvValue)
	if err := mcp.ValidateHeader(header, value); err != nil {
		return protocol.MCPResponse{Error: err.Error()}
	}
	if err := validMCPEnvValue(value); err != nil {
		return protocol.MCPResponse{Error: header + ": " + err.Error()}
	}
	if err := s.mcpSecrets.setHeader(server, header, value); err != nil {
		return protocol.MCPResponse{Error: "storing the " + header + " header: " + err.Error()}
	}
	s.rebuildCredScrubber()
	s.mcpStates.forget(server)
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems}
	if value == "" {
		resp.Detail = fmt.Sprintf("Forgot %s's %s header.", server, header)
	} else {
		resp.Detail = fmt.Sprintf("%s's %s header is set, from its next connection.", server, header)
	}
	s.logger.Printf("mcp: a value for %s's %s header was %s by the user", server, header,
		map[bool]string{true: "forgotten", false: "given"}[value == ""])
	return resp
}
