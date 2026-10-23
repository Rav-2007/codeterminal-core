package main

import (
	"context"
	"fmt"
	"strings"

	"mochiii/protocol"
)

// CONNECTING TO A MODEL SERVER ON THIS MACHINE, WITH NO KEY (1.3.0).
//
// /connect with an empty key used to be refused outright (validateKeyShape),
// so the only way to a local server was MOCHIII_API_BASE before the daemon
// started. A ConnectRequest with no key and a loopback address now connects
// to it, and nothing else changes: an empty key for any other address is still
// refused, so no provider is ever sent a request with no credential by this.
//
// No model is tried. setUpProvider tries several of a provider's models with
// real requests, which against a server on this machine means loading each of
// them into memory in turn. The server's own list is taken as it is, its first
// model is the default, and the person picks the one they want next.

// connectLocal connects to the server at base, which is on this machine.
func (s *Server) connectLocal(ctx context.Context, base, path string, envOverride bool) protocol.ConnectResponse {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	// The caller checked, and this checks again: a request with no key goes
	// to this machine and nowhere else, whoever calls this.
	if !isLoopbackBase(base) {
		return protocol.ConnectResponse{Error: fmt.Sprintf("%s is not on this computer, so it needs a key; "+
			"nothing was sent or saved", base)}
	}
	if err := validateConnectBase(base); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}
	lctx, cancel := context.WithTimeout(ctx, localDiscoverTimeout)
	models, status, _, err := listModels(lctx, localHTTP, base, "")
	cancel()
	switch {
	case err != nil:
		return protocol.ConnectResponse{Error: fmt.Sprintf("nothing answered at %s -- is the model server running? "+
			"(for Ollama: ollama serve); nothing was saved", base)}
	case refusesKey(status, ""):
		return protocol.ConnectResponse{Error: fmt.Sprintf("the server at %s asks for a key (HTTP %d); "+
			"paste it with /connect %s; nothing was saved", base, status, base)}
	}
	ids := chatModelIDs(models)
	if len(ids) == 0 {
		return protocol.ConnectResponse{Error: fmt.Sprintf("the server at %s answered (HTTP %d) but lists no models -- "+
			"download one first (for Ollama: ollama pull qwen2.5-coder:7b); nothing was saved", base, status)}
	}
	if err := saveCredential(path, storedCredential{
		APIBase: base, Verified: true, DefaultModel: ids[0], Models: ids,
	}); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}
	resp := protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted,
		Detail:  "the model server on this machine answered; it needs no key",
		APIBase: base, InUse: !envOverride, EnvOverride: envOverride,
		Model: ids[0], ModelCount: len(ids),
	}
	if !envOverride {
		s.setProvider("", base, providerTierConfig(s.cfg, providerName(base), ids, nil, ids[0]))
	}
	return resp
}
