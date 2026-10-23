package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// The daemon side of the terminal app's local model servers (protocol/local.go).

// localDiscoverTimeout bounds a discovery: four loopback addresses, each told
// apart with at most a few small requests.
const localDiscoverTimeout = 10 * time.Second

// isLocalRequest recognises a LocalRequest by its discriminator.
func isLocalRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "local")
}

// handleLocal answers a LocalRequest.
func (s *Server) handleLocal(ctx context.Context, enc *json.Encoder, req protocol.LocalRequest) {
	var resp protocol.LocalResponse
	switch {
	case req.Discover:
		dctx, cancel := context.WithTimeout(ctx, localDiscoverTimeout)
		resp = protocol.LocalResponse{Ok: true, Servers: discoverLocalServers(dctx)}
		cancel()
	case req.Fit:
		resp = s.fitLocal(ctx)
	default:
		resp = protocol.LocalResponse{Error: "a local request asks to discover or to fit"}
	}
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("writing local response: %v", err)
	}
}

// NO INTERNET TOOLS FOR A CLIENT THAT ASKED FOR NONE.
//
// A client that declares protocol.CapNoNetworkTools -- the terminal app, for a
// model on this machine, until the user turns web tools on -- is offered no
// tool that reaches the internet, and a call to one is refused. Both, because
// the menu is not a fence: a tool call is resolved against every tool the
// daemon has (resolveExecutable), so a model that names web_search without
// being offered it would otherwise run it.

type noNetworkToolsKey struct{}

// withClientCaps carries what the client asked for into the turn's context.
func withClientCaps(ctx context.Context, hs protocol.HandshakeRequest) context.Context {
	if hs.HasCapability(protocol.CapNoNetworkTools) {
		return context.WithValue(ctx, noNetworkToolsKey{}, true)
	}
	return ctx
}

// noNetworkTools reports whether this turn's client asked for no internet tools.
func noNetworkTools(ctx context.Context) bool {
	off, _ := ctx.Value(noNetworkToolsKey{}).(bool)
	return off
}

// dropsNetworkTool reports whether tool is left off this turn's menu.
func dropsNetworkTool(ctx context.Context, tool mcp.Tool) bool {
	return tool.ReachesNetwork && noNetworkTools(ctx)
}

// errNetworkToolOff is the lookup error for an internet tool in such a turn.
var errNetworkToolOff = errors.New("internet tools are off for this model")

// lookupForTurn is registry.Lookup for one turn: an internet tool, in a turn
// whose client asked for none, does not exist.
func lookupForTurn(ctx context.Context, registry *mcp.Registry, qualified string) (mcp.Tool, mcp.Policy, error) {
	tool, policy, err := registry.Lookup(ctx, qualified)
	if err == nil && dropsNetworkTool(ctx, tool) {
		return mcp.Tool{}, mcp.PolicyDeny, errNetworkToolOff
	}
	return tool, policy, err
}
